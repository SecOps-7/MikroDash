package server

// The shared asset tree, served by Go when there is no Node to proxy it from.
//
// See Options.StaticDir for why this exists. In short: `web/build.mjs` points
// the ported SPA at eight assets it does not copy — `/vendor/tabler.min.css`,
// `/vendor/fonts/fonts.css`, `/vendor/chart.umd.min.js`, `/css/app-fonts.css`,
// `/css/dashboard-grid.css`, `/css/topology.css`, `/logo.png` and
// `/preflight.js` — plus the login page itself. Node serves all of them today.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticOrProxy serves a file from StaticDir when one exists, and hands
// everything else to the proxy.
//
// ── FALL THROUGH RATHER THAN 404 ────────────────────────────────────────────
//
// A path the directory does not hold goes to the proxy, so a partially
// populated tree degrades to today's behaviour instead of a wall of 404s. In
// standalone the proxy answers 502 with a sentence saying Node is unreachable,
// which is a better failure than a bare 404: it names the cause.
//
// ── AND IT REFUSES TO ESCAPE THE DIRECTORY ──────────────────────────────────
//
// `filepath.Clean` on a rooted path collapses `..` before the join, which is the
// standard defence. It is written out rather than left to http.Dir because this
// serves an OPERATOR-SUPPLIED directory next to a router-management app, and a
// traversal here reads any file the process can.
func (s *Server) staticOrProxy() http.Handler {
	fileServer := http.FileServer(http.Dir(s.staticDir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// `/login` IS A ROUTE, NOT A FILE. `index.js` answers it with
		// `res.sendFile(public/login.html)`; there is no file called `login`,
		// so a purely static handler 404s the one page an operator needs when
		// they cannot get in. Mapped here rather than by asking the operator to
		// rename anything in a tree this process only reads.
		if r.URL.Path == "/login" {
			if full, ok := s.staticPath("/login.html"); ok {
				if st, err := os.Stat(full); err == nil && !st.IsDir() {
					http.ServeFile(w, r, full)
					return
				}
			}
		}
		if rel, ok := s.staticPath(r.URL.Path); ok {
			if st, err := os.Stat(rel); err == nil && !st.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// ── STANDALONE HAS NOTHING TO FALL THROUGH TO ───────────────────────
		//
		// The fall-through is what keeps a PARTIAL asset tree from turning into
		// a wall of 404s mid-migration: anything this directory does not hold is
		// still Node's. With no Node the target is empty, `httputil` fails on
		// the scheme, and the operator gets a **502** — which says "the upstream
		// failed" when the truth is "there is no upstream, and this path was
		// never ported".
		//
		// That is what `/settings` and the Devices page answer today, and it is
		// the third time in one session that a technically-correct response has
		// read as a broken app. 404 is the honest answer and the one a browser
		// renders sensibly.
		//
		// Added 2026-08-28 while the operator was testing standalone.
		if s.standalone {
			http.NotFound(w, r)
			return
		}
		s.proxy.ServeHTTP(w, r)
	})
}

// staticPath resolves a request path inside StaticDir, or reports that it does
// not belong there.
func (s *Server) staticPath(urlPath string) (string, bool) {
	if s.staticDir == "" {
		return "", false
	}
	// A DIRECTORY LISTING IS NOT AN ASSET. Without this, `/vendor/` would serve
	// an index of everything the tree holds.
	if urlPath == "" || strings.HasSuffix(urlPath, "/") {
		return "", false
	}
	clean := filepath.Clean("/" + strings.TrimPrefix(urlPath, "/"))
	full := filepath.Join(s.staticDir, clean)
	// Belt as well as braces: after Clean and Join the result must still be
	// inside the directory. Cheap, and the one check that survives a mistake in
	// either of the two above.
	root := filepath.Clean(s.staticDir)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", false
	}
	return full, true
}

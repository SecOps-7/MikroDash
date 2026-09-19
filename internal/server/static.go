package server

// The shared asset tree: `/vendor/*`, `/css/*`, `/logo.png` and the login page.
// See Options.StaticDir.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticOrNotFound serves a file from StaticDir when one exists, and 404s
// everything else. It is the mux's catch-all.
//
// ── AND IT REFUSES TO ESCAPE THE DIRECTORY ──────────────────────────────────
//
// `filepath.Clean` on a rooted path collapses `..` before the join, which is the
// standard defence. It is written out rather than left to http.Dir because this
// serves an OPERATOR-SUPPLIED directory next to a router-management app, and a
// traversal here reads any file the process can.
func (s *Server) staticOrNotFound() http.Handler {
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
					w.Header().Set("Cache-Control", "no-cache")
					http.ServeFile(w, r, full)
					return
				}
			}
		}
		if rel, ok := s.staticPath(r.URL.Path); ok {
			if st, err := os.Stat(rel); err == nil && !st.IsDir() {
				// ── REVALIDATED ON EVERY LOAD, LIKE THE SHELL ───────────────
				//
				// `spa()` sets this for index.html, app.js and app.css, and
				// these assets were left out: `/css/dashboard-grid.css` kept its
				// name across builds and carried only Last-Modified, so a browser
				// kept its old copy after a deploy. The operator saw the new
				// Agent Overview markup rendered with none of its new styles,
				// because the markup is in the shell and the styles are here.
				// `no-cache` still reuses the copy once the server confirms it is
				// current, so an unchanged file costs a 304, not a download.
				w.Header().Set("Cache-Control", "no-cache")
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		// NOT FOUND, and honestly so. This fell through to a reverse proxy to
		// the Node app while one ran beside this process; with none, a path
		// nothing serves is a 404, which a browser renders sensibly.
		http.NotFound(w, r)
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

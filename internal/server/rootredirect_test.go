package server

// The app is reachable from the ROOT URL, and there is no second mount point.
//
// ── WHAT WAS HERE BEFORE ────────────────────────────────────────────────────
//
// `Prefix` ("/next") was COEXISTENCE SCAFFOLDING: it let Node keep `/` while
// this port took one page at a time. Two defects came out of it in one day, and
// both are worth keeping written down because they are the same shape:
//
//  1. The root fell through to a proxy with an EMPTY TARGET and answered 502.
//     Every server-side check said the app was healthy — because every one of
//     them asked for `/next/`. "It works if you know the path" is not working.
//  2. `navigate()` sent an unported page to `/` on the reasoning that Node still
//     owned it. With no Node, `/` is this app, so Devices and Settings bounced
//     straight back to the dashboard.
//
// The operator's instruction on 2026-08-28 was "remove /next/ entirely, we won't
// use it", and it was removed rather than aliased: an alias nobody uses is a
// second code path nobody tests, which is how (1) survived as long as it did.
//
// Coexistence itself (serving APIs and proxying the rest to a Node app beside
// this one) was retired on 2026-09-19, so the root is always this app's.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTheAppIsServedFromTheRoot(t *testing.T) {
	srv := &Server{staticDir: t.TempDir()}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	// 401 rather than 200 here because this Server has no auth wired — what is
	// asserted is that the root reaches the SESSION-GATED app rather than
	// falling through to a proxy with no target, which answered 502 and is what
	// the operator hit.
	if rec.Code == http.StatusBadGateway {
		t.Fatalf("GET / answered 502 — it is still falling through to the proxy")
	}
	// A redirect to the LOGIN PAGE is right and is what an unauthenticated root
	// request gets — `requireSession` doing its job. A redirect to a MOUNT POINT
	// is the stopgap this replaced, and is what must not come back.
	if loc := rec.Header().Get("Location"); rec.Code == http.StatusFound && loc != "/login" {
		t.Errorf("GET / redirects to %q. The root serves the app itself; a redirect to a mount "+
			"point was the stopgap that existed while the built markup still referenced ./app.js "+
			"relative to one.", loc)
	}
}

// TestTheStranglerPrefixIsGone.
//
// The property the operator asked for, pinned so it cannot come back by accident
// — a re-added alias would be a second, untested path to the same app.
func TestTheStranglerPrefixIsGone(t *testing.T) {
	srv := &Server{staticDir: t.TempDir()}
	h := srv.Handler()

	for _, p := range []string{"/next/", "/next/dns", "/next/app.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404. The /next prefix was removed outright on the "+
				"operator's instruction; if it serves again, something re-mounted it.", p, rec.Code)
		}
	}
}

// TestTheAppAssetsAreServedAtTheirAbsolutePaths.
//
// The document names `/app.js` and `/app.css`, and that is exactly what let the
// prefix go. If either stopped being served the page would load and the bundle
// would not — a blank shell that looks like the app, which is a worse failure
// than a 404.
func TestTheAppAssetsAreServedAtTheirAbsolutePaths(t *testing.T) {
	srv := &Server{staticDir: t.TempDir()}
	h := srv.Handler()

	for _, p := range []string{"/app.js", "/app.css"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusBadGateway || rec.Code == http.StatusNotFound {
			t.Errorf("%s answered %d — the document references it absolutely, so this is the "+
				"bundle failing to load on a page that otherwise renders", p, rec.Code)
		}
	}
}

// TestAnUnservedPathIs404. The static handler fell through to a proxy for
// anything its directory did not hold, which was right while a Node app owned
// the rest; with none, that answered 502 ("the upstream failed") where the
// truth is "nothing serves this". 404 is the honest answer.
func TestAnUnservedPathIs404(t *testing.T) {
	srv := &Server{staticDir: t.TempDir()}
	h := srv.Handler()

	for _, p := range []string{"/nothing-here", "/also-not-here"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusBadGateway {
			t.Errorf("%s answered 502. There is no upstream to have failed; 404 is "+
				"the honest answer and the one a browser renders sensibly.", p)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", p, rec.Code)
		}
	}
}

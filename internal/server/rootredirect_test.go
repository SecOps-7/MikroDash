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
// ── COEXISTENCE IS STILL A MODE, AND IT STILL KEEPS ITS HANDS OFF `/` ───────
//
// Not as a migration step any more — `tools/live-diff.sh` runs this binary
// beside the live app and logs in THROUGH its proxy to compare payloads
// endpoint by endpoint. Taking `/` away from Node would break the only tool
// that measures the two implementations against each other. With a Node URL
// configured this process serves APIs and proxies the rest; it offers no
// frontend of its own.

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

// deadProxy is a real ReverseProxy pointing nowhere. A nil one PANICS when the
// catch-all serves, which is a crash rather than the assertion this test is
// making — the first version of this file did exactly that.
func deadProxy(t *testing.T) *httputil.ReverseProxy {
	t.Helper()
	u, err := url.Parse("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	p := httputil.NewSingleHostReverseProxy(u)
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.WriteHeader(http.StatusBadGateway)
	}
	return p
}

func TestStandaloneServesTheAppFromTheRoot(t *testing.T) {
	srv := &Server{standalone: true, staticDir: t.TempDir(), proxy: deadProxy(t)}
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
	srv := &Server{standalone: true, staticDir: t.TempDir(), proxy: deadProxy(t)}
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
	srv := &Server{standalone: true, staticDir: t.TempDir(), proxy: deadProxy(t)}
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

// TestCoexistenceLeavesTheRootAlone — the half that matters more.
//
// `tools/live-diff.sh` logs in through this process's proxy. Taking `/` from
// Node breaks the comparison this whole port is verified by.
func TestCoexistenceLeavesTheRootAlone(t *testing.T) {
	srv := &Server{standalone: false, staticDir: t.TempDir(), proxy: deadProxy(t)}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Errorf("GET / answered %d with a Node URL configured; it must reach the proxy (502 "+
			"here, because the test proxy points nowhere)", rec.Code)
	}
}

// TestStandaloneAnswers404ForAnUnportedPath.
//
// The static handler falls through to the proxy for anything its directory does
// not hold — which was right mid-migration, where "not here" meant "still
// Node's". With no Node the target is empty and the operator gets a 502: "the
// upstream failed", when the truth is "there is no upstream".
func TestStandaloneAnswers404ForAnUnportedPath(t *testing.T) {
	srv := &Server{standalone: true, staticDir: t.TempDir(), proxy: deadProxy(t)}
	h := srv.Handler()

	for _, p := range []string{"/nothing-here", "/also-not-here"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusBadGateway {
			t.Errorf("%s answered 502 in standalone. There is no upstream to have failed; 404 is "+
				"the honest answer and the one a browser renders sensibly.", p)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", p, rec.Code)
		}
	}
}

// TestCoexistenceStillFallsThroughToNode — the half that must not change.
func TestCoexistenceStillFallsThroughToNode(t *testing.T) {
	srv := &Server{standalone: false, staticDir: t.TempDir(), proxy: deadProxy(t)}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if rec.Code == http.StatusNotFound {
		t.Error("/settings answered 404 while a Node URL is configured. That path is still Node's " +
			"during a live-diff run, and 404ing it breaks the comparison.")
	}
}

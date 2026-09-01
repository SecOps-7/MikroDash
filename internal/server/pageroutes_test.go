package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"mikrodash/internal/pages"
)

// Every page has a URL, an unknown path does not, and a deep link survives the
// login.
//
// These pin the three properties that make per-page URLs worth having, and each
// one is a thing that broke or nearly broke while they were being added.

// TestEveryPageHasAURL: a request for a page path reaches the session gate.
//
// A page missing from the loop would 404 on a link the app itself renders — the
// nav would offer a URL the server refuses.
func TestEveryPageHasAURL(t *testing.T) {
	srv := &Server{standalone: true, staticDir: t.TempDir(), proxy: deadProxy(t)}
	h := srv.Handler()

	for _, p := range pages.All {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/"+p.URL(), nil))

		if rec.Code == http.StatusNotFound {
			t.Errorf("GET /%s answered 404 — the page has no URL registered", p.URL())
		}
		if rec.Code == http.StatusBadGateway {
			t.Errorf("GET /%s answered 502 — it fell through to the proxy instead of the app", p.URL())
		}
		// No auth is wired here, so the gate redirecting is the pass condition:
		// it proves the path reached `requireSession` rather than the file
		// server or the proxy.
		if rec.Code != http.StatusFound {
			t.Errorf("GET /%s answered %d, want a redirect to the login page", p.URL(), rec.Code)
		}
	}
}

// TestTheDashboardIsServedAtHome pins the one page whose URL is not its key.
//
// It is called `dashboard` in the nav, its markup, its room and the grants in the
// operator's database; only the address bar says `home`. Both halves are asserted
// so neither can drift alone.
func TestTheDashboardIsServedAtHome(t *testing.T) {
	if got := pages.ForURL("home"); got != "dashboard" {
		t.Errorf("/home resolves to %q, want dashboard", got)
	}
	if got := pages.ForURL("dashboard"); got != "" {
		t.Errorf("/dashboard resolves to %q — the dashboard is served at /home, and a second "+
			"path to one page is how bookmarks and history disagree", got)
	}
}

// TestAnUnknownPathIsStillAnHonest404.
//
// The reason the routes are registered one by one rather than as a catch-all: a
// path that names no page must say so, not quietly serve the shell and let the
// frontend decide. `/nothing-here` has been pinned since before URLs existed.
func TestAnUnknownPathIsStillAnHonest404(t *testing.T) {
	srv := &Server{standalone: true, staticDir: t.TempDir(), proxy: deadProxy(t)}
	h := srv.Handler()

	for _, path := range []string{"/nothing-here", "/logs-typo", "/dashboard"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s answered %d, want 404 — an unregistered path must not serve the app",
				path, rec.Code)
		}
	}
}

// TestADeepLinkSurvivesTheLogin: the destination is carried as `?next=`.
//
// Without it, signing in after following a link to `/logs` lands on the
// dashboard, and the link may as well not have been shared.
func TestADeepLinkSurvivesTheLogin(t *testing.T) {
	srv := &Server{standalone: true, staticDir: t.TempDir(), proxy: deadProxy(t)}
	h := srv.Handler()

	for _, tc := range []struct{ req, wantNext string }{
		{"/logs", "/logs"},
		{"/wifi-clients", "/wifi-clients"},
		{"/reports?tab=traffic", "/reports?tab=traffic"},
		{"/", ""}, // the root is exempt: `?next=/` is noise on the commonest redirect
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.req, nil))
		loc := rec.Header().Get("Location")
		if rec.Code != http.StatusFound {
			t.Errorf("GET %s answered %d, want a redirect", tc.req, rec.Code)
			continue
		}
		if tc.wantNext == "" {
			if loc != "/login" {
				t.Errorf("GET %s redirects to %q, want a bare /login", tc.req, loc)
			}
			continue
		}
		if !strings.HasPrefix(loc, "/login?next=") {
			t.Errorf("GET %s redirects to %q — the destination was dropped", tc.req, loc)
			continue
		}
		got, err := url.QueryUnescape(strings.TrimPrefix(loc, "/login?next="))
		if err != nil || got != tc.wantNext {
			t.Errorf("GET %s carries next=%q, want %q", tc.req, got, tc.wantNext)
		}
	}
}

// TestCoexistenceDoesNotClaimThePageURLs.
//
// With a proxy target configured the page routes must not exist, or this build
// would answer for paths the other process owns. The registration sits inside
// `s.standalone` for exactly this reason.
func TestCoexistenceDoesNotClaimThePageURLs(t *testing.T) {
	srv := &Server{standalone: false, staticDir: t.TempDir(), proxy: deadProxy(t)}
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/logs", nil))
	if rec.Code == http.StatusFound {
		t.Error("GET /logs was answered by this process while a proxy target is configured — " +
			"the page routes must stay standalone-only")
	}
}

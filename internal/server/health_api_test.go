package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// An unauthenticated /healthz says whether the app is up and NOTHING else.
//
// The live comment: "version, router ids and collector detail would otherwise be
// free fingerprinting for anyone who can reach the port." The Docker healthcheck
// is unauthenticated and needs only the status code and these two flags, so the
// reduction costs nothing and closes an information leak on a route that is,
// by design, reachable without a session.
func TestUnauthenticatedHealthzDisclosesOnlyOkAndStarting(t *testing.T) {
	s := schedServer(t, `[]`)
	s.auth = NewAuth("", time.Hour)

	mux := http.NewServeMux()
	s.registerHealth(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %s", w.Body.String())
	}
	for k := range body {
		if k != "ok" && k != "starting" {
			t.Errorf("an unauthenticated /healthz disclosed %q: %s", k, w.Body.String())
		}
	}
	if _, ok := body["ok"]; !ok {
		t.Errorf("no `ok` field: %s", w.Body.String())
	}
	if _, ok := body["starting"]; !ok {
		t.Errorf("no `starting` field: %s", w.Body.String())
	}
	// AND NEVER THESE, named individually so a future field addition has to
	// argue with a test rather than slip through the loop above.
	// `version` included: it is disclosed to a SIGNED-IN caller and must not
	// reach an anonymous one. The live comment names it first among the things
	// that would otherwise be "free fingerprinting for anyone who can reach the
	// port", and it is the field most useful to somebody choosing an exploit.
	for _, leak := range []string{"version", "activeRouterId", "checks", "uptime"} {
		if _, present := body[leak]; present {
			t.Errorf("%q reached an unauthenticated caller", leak)
		}
	}
}

// A router that is unreachable is a 503, so an orchestrator can act on it.
// `ok:true` with a 200 while nothing is connected would make the healthcheck
// decorative.
func TestHealthzIsA503WhenNothingIsConnected(t *testing.T) {
	s := schedServer(t, `[]`)
	s.auth = NewAuth("", time.Hour)

	mux := http.NewServeMux()
	s.registerHealth(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d with no router connected, want 503", w.Code)
	}
}

// Shutdown releases everything, in an order that cannot lose data.
//
// ── A SOURCE PIN, BECAUSE THE FAILURE IS AN OMISSION ──────────────────────
//
// `Shutdown` was one line — `s.sessions.Shutdown()` — and three things outlived
// it: both background pools kept their sockets, the backup scheduler kept
// ticking, and the database was never closed. Nothing observable broke, because
// the process exits a moment later; what is lost is a checkpointed WAL and a
// clean close on the router's side.
//
// The ORDER is the part a behavioural test would not catch either. Sessions
// flush the open history minute, so they must run before the database closes;
// the scheduler must stop first or a tick can start a backup into a closing
// database.
func TestShutdownReleasesEverythingInOrder(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Server) Shutdown()")
	if i < 0 {
		t.Fatal("Shutdown is gone — this test is measuring nothing")
	}
	body := src[i:]
	if j := strings.Index(body, "\n}"); j >= 0 {
		body = body[:j]
	}

	want := []struct{ what, call string }{
		{"the backup scheduler", "backupSched.Stop()"},
		{"the sessions", "sessions.Shutdown()"},
		{"the alert pool", "alertPool.Close()"},
		{"the overview pool", "pool.Close()"},
		{"the database", "auditDB.Close()"},
	}
	at := make([]int, len(want))
	for i, w := range want {
		at[i] = strings.Index(body, w.call)
		if at[i] < 0 {
			t.Errorf("Shutdown never releases %s (%s)", w.what, w.call)
		}
	}
	for i := 1; i < len(at); i++ {
		if at[i-1] < 0 || at[i] < 0 {
			continue
		}
		if at[i-1] > at[i] {
			t.Errorf("%s is released after %s; the order is scheduler, sessions, pools, "+
				"database — sessions flush the open history minute and need the database "+
				"still open, and a scheduler tick must not start a backup into a closing one",
				want[i-1].what, want[i].what)
		}
	}
}

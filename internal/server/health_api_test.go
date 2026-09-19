package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	s.auth = NewAuth()

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
//
// ── RE-AIMED, DELIBERATELY: THIS USED TO PASS AN EMPTY FLEET ───────────────
//
// It asserted the right rule through the wrong fixture. `schedServer(t, "[]")`
// is an install with NO DEVICE CONFIGURED, and the test read the resulting 503
// as proof that an unreachable router reports unhealthy — when what it actually
// pinned was a fresh install reporting unhealthy for ever. See
// TestAFreshInstallWithNoDeviceIsHealthy for why that is a bug rather than a
// preference.
//
// The rule this test is FOR is unchanged and is now expressed with a device that
// is configured and cannot be reached, which is the state it always meant.
func TestHealthzIsA503WhenNothingIsConnected(t *testing.T) {
	s := schedServer(t, `[{"id":"r1","label":"One","host":"198.51.100.1","port":8728,
	  "username":"u","password":""}]`)
	s.auth = NewAuth()
	if err := os.WriteFile(filepath.Join(s.store.Dir, "settings.json"),
		[]byte(`{"activeRouterId":"r1"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	s.registerHealth(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d with a configured router that is not connected, want 503", w.Code)
	}
}

// ── A FRESH INSTALL HAS NOTHING TO BE DISCONNECTED FROM ────────────────────
//
// `/healthz` answered 503 for an install with no device configured, because
// `activeRouterHealth` returns false for an empty `activeRouterId` and `ok` was
// simply `connected`. Not just during the grace window — after 90 seconds it was
// not even reported as `starting`, just unhealthy, for ever.
//
// THAT DEADLOCKS THE ROUTEROS APP INSTALL, which is a documented path
// (`docs/routeros-container-install.md`). The App withholds its UI-URL until the
// container is healthy; the container is unhealthy until a device is added; and
// a device can only be added through the UI. Reported on issue #120 by an
// operator who had to find the address by hand to get in at all.
//
// THIS IS THE FOURTH TIME THIS CLASS HAS SHIPPED — "a thing that does not exist
// yet, reported as a failure". 0.8.11 was a missing `.secret`, 0.8.12 a missing
// `users.json` read as an error rather than "no users yet", 0.8.14 a first run
// with no wizard, 0.8.16 a missing `settings.json`.
// `TestEveryConfigReaderSurvivesAFreshInstall` was written to stop a fourth and
// could not see this one: it guards config READERS, and this is the same mistake
// one layer up, in what the reading MEANS.
//
// The distinction being drawn is between three states, not two. Nothing to
// connect to is healthy; something to connect to that has not answered yet is
// starting; something that should be there and is not is unhealthy. Only the
// first changes here.
func TestAFreshInstallWithNoDeviceIsHealthy(t *testing.T) {
	s := schedServer(t, `[]`)
	s.auth = NewAuth()

	mux := http.NewServeMux()
	s.registerHealth(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))

	if w.Code != http.StatusOK {
		t.Errorf("status %d for an install with no device configured, want 200 — "+
			"the RouterOS App withholds its UI-URL until the container is "+
			"healthy, and the UI is the only place a device can be added",
			w.Code)
	}

	var got struct {
		OK       bool `json:"ok"`
		Starting bool `json:"starting"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if !got.OK {
		t.Error("ok:false with no device configured — nothing is wrong with an " +
			"install that has not been set up yet")
	}
	if got.Starting {
		t.Error("starting:true with no device configured — it is not on its way " +
			"to connecting to anything, it is ready and waiting to be set up")
	}
}

// And the moment a device IS configured, the old rule applies again in full: a
// device that has not answered is not healthy just because the fleet is small.
// This is the half that stops the fix above from becoming "always 200".
func TestAConfiguredDeviceThatNeverAnswersIsStillUnhealthy(t *testing.T) {
	s := schedServer(t, `[{"id":"r1","label":"One","host":"198.51.100.1","port":8728,
	  "username":"u","password":""}]`)
	s.auth = NewAuth()
	if err := os.WriteFile(filepath.Join(s.store.Dir, "settings.json"),
		[]byte(`{"activeRouterId":"r1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// Past the grace window, so this is "unhealthy" rather than "starting".
	s.startedAt = time.Now().Add(-2 * healthStartupGrace)

	if code := healthzCode(t, s); code != http.StatusServiceUnavailable {
		t.Errorf("status %d for a configured device that never answered, want 503 — "+
			"a healthcheck that cannot say no is decorative", code)
	}
}

// ── A FLEET FILE THAT WILL NOT PARSE IS NOT A FRESH INSTALL ────────────────
//
// This is the branch that keeps the fix above honest, and the one a naive
// `len(routers) == 0` gets wrong. `store.Routers` answers ZERO routers both when
// there are none and when the file could not be decoded at all, so a count alone
// reports a BROKEN install as a brand new one and returns 200 for ever — a worse
// version of the bug being fixed, because it is silence about a real fault
// rather than noise about a non-fault.
//
// TRUNCATED JSON, not a boolean held as a string. The obvious fixture for this
// is `"disabled": "false"`, and it does not work: `store.Routers` retries
// through `normalizeStoredRouterBools` and repairs its own copy, so that file
// yields a router plus a warning and this test would pass whatever
// `deviceExpected` did. Found by mutating the fix and watching this test survive
// — the fixture has to be something no coercion can rescue.
func TestACorruptFleetFileIsNotMistakenForAFreshInstall(t *testing.T) {
	s := schedServer(t, `[]`)
	s.auth = NewAuth()
	if err := os.WriteFile(filepath.Join(s.store.Dir, "routers.json"),
		[]byte(`[{"id":"r1","label":"One","host":`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.startedAt = time.Now().Add(-2 * healthStartupGrace)

	if code := healthzCode(t, s); code != http.StatusServiceUnavailable {
		t.Errorf("status %d for a fleet file that will not decode, want 503 — "+
			"reading 'no devices' off a failed parse turns a broken install "+
			"into a healthy-looking one", code)
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

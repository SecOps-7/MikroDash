package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/routers"
)

// `/healthz` and the THIRD holder of a router.
//
// ── IT ASKED TWO SOURCES AND GAVE UP ───────────────────────────────────────
//
// `activeRouterHealth` consulted the interactive sessions, then the alert pool,
// then returned false. Three components can hold a router, and the one it did
// not ask is the one that holds it exactly when the other two do not:
// `alertPoolExclusions` removes from the alert pool every router the overview
// pool has ANSWERED for, and the alert pool forgets the status of a router it
// drops.
//
// So while anybody had the Devices page open, `/healthz` reported the active
// router disconnected — a 503 for a healthy install, which is how an
// orchestrator decides to restart a container.
//
// And it never recovered after a router edit: `routerUpdate` calls `syncPool`,
// which dials the whole fleet, and no release is scheduled because nobody was
// watching the Devices page to stop watching it. Measured against the shipped
// 0.8.18 on a real fleet: one edit, then `ok:false` for as long as the process
// ran, while an unmodified build of this change stayed true through the same
// sequence.

// healthPoolServer is a server whose ACTIVE router is held only by the overview
// pool — no interactive session, no alert pool entry.
func healthPoolServer(t *testing.T, dial routers.Dialer) *Server {
	t.Helper()
	s := schedServer(t, `[{"id":"r1","label":"One","host":"198.51.100.1","port":8728,
	  "username":"u","password":""}]`)
	s.auth = NewAuth("", time.Hour)
	// The active router, which is what `activeRouterHealth` looks up.
	if err := os.WriteFile(filepath.Join(s.store.Dir, "settings.json"),
		[]byte(`{"activeRouterId":"r1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.pool = routers.NewPool(dial, time.Hour, nil, nil)
	t.Cleanup(s.pool.Close)
	s.pool.Sync([]routers.RouterConfig{{ID: "r1", Label: "One", Host: "198.51.100.1"}}, nil)
	return s
}

func healthzCode(t *testing.T, s *Server) int {
	t.Helper()
	mux := http.NewServeMux()
	s.registerHealth(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	return w.Code
}

// TestHealthzSeesARouterHeldOnlyByTheOverviewPool is the bug.
func TestHealthzSeesARouterHeldOnlyByTheOverviewPool(t *testing.T) {
	up := &upDialer{}
	s := healthPoolServer(t, up.dial)

	waitUntil(t, "the overview pool answered", func() bool {
		for _, sum := range s.pool.Summaries() {
			if sum.RouterID == "r1" && sum.Known {
				return true
			}
		}
		return false
	})

	if code := healthzCode(t, s); code != http.StatusOK {
		t.Errorf("healthz answered %d for a router the overview pool has connected. "+
			"That is every moment somebody has the Devices page open, and it is a "+
			"503 an orchestrator will act on.", code)
	}
}

// TestHealthzStillReportsAnUnreachableRouter — the other direction. A check
// that answered 200 for everything the pool had heard of would be decorative,
// which is the failure `TestHealthzIsA503WhenNothingIsConnected` guards from
// the other side.
func TestHealthzStillReportsAnUnreachableRouter(t *testing.T) {
	down := &downDialer{}
	s := healthPoolServer(t, down.dial)

	waitUntil(t, "the overview pool gave up", func() bool {
		for _, sum := range s.pool.Summaries() {
			if sum.RouterID == "r1" && sum.Known {
				return true
			}
		}
		return false
	})

	if code := healthzCode(t, s); code != http.StatusServiceUnavailable {
		t.Errorf("healthz answered %d for a router that will not answer, want 503", code)
	}
}

// TestNothingHasAnsweredYetIsA503.
//
// ── AND AN HONEST NOTE ABOUT THE `Known` GATE ──────────────────────────────
//
// A summary exists as soon as `Sync` builds the session, so `Connected: false`
// is the zero value until the first dial returns — the trap that showed routers
// as red Offline on the Devices page, twice reported.
//
// The gate in `activeRouterHealth` is DEFENSIVE, and this test does not prove
// it: with the alert pool consulted first, an unanswered summary and a skipped
// one both end at the same `return false`, so removing `sum.Known` changes no
// outcome and no test can kill that mutation. It is kept because it states the
// invariant every other reader of `Summaries()` uses, and because the order
// these three sources are consulted in is exactly the kind of thing that gets
// rearranged later — at which point the gate starts mattering and nobody would
// think to add it.
//
// What this DOES pin is the outcome: before anything has answered, 503.
func TestNothingHasAnsweredYetIsA503(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	s := healthPoolServer(t, func(routeros.Config) (routers.Conn, error) {
		<-block // never answers, so Known stays false
		return nil, os.ErrClosed
	})

	// A summary exists already; it just has not been answered.
	waitUntil(t, "a summary exists", func() bool { return len(s.pool.Summaries()) == 1 })
	for _, sum := range s.pool.Summaries() {
		if sum.Known {
			t.Fatal("the stub answered; this case is not testing what it means to")
		}
	}
	if code := healthzCode(t, s); code != http.StatusServiceUnavailable {
		t.Errorf("healthz answered %d before anything had answered", code)
	}
}

// ── dialers ────────────────────────────────────────────────────────────────

type upDialer struct{}

func (u *upDialer) dial(routeros.Config) (routers.Conn, error) { return connectedStub{}, nil }

type downDialer struct{}

func (d *downDialer) dial(routeros.Config) (routers.Conn, error) { return nil, os.ErrClosed }

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ── AND THE LEAK THAT MADE IT PERMANENT ────────────────────────────────────
//
// `syncPool` DIALS. Most of its callers are not the Devices page — a router
// edit, a create, a delete, a site change — and each one woke the overview pool
// against the whole fleet and left it there for the life of the process,
// because a release is only scheduled when somebody stops watching a page they
// never started watching.
//
// A SOURCE PIN, because the behaviour is a two-minute timer over a watcher set
// that a unit test would have to fake wholesale. What can be checked cheaply is
// the thing that was missing: that the dial and the release are wired together
// in one place rather than at five call sites, four of which forgot.
func TestSyncPoolSchedulesARelease(t *testing.T) {
	b, err := os.ReadFile("devices.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Server) syncPool()")
	if i < 0 {
		t.Fatal("syncPool is gone — this check is reading nothing")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "s.pool.Sync(") {
		t.Fatal("syncPool no longer dials; this check is reading nothing")
	}
	if !strings.Contains(body, "s.scheduleDevicesRelease()") {
		t.Error("syncPool dials the fleet and schedules no release. Every caller " +
			"that is not the Devices page — a router edit, a create, a delete, a " +
			"site change — then holds a connection to every router for the life of " +
			"the process, and /healthz reports the active router disconnected " +
			"because the alert pool has handed it over and forgotten it.")
	}
}

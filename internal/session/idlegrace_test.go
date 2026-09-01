package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/store"
)

// graceManager is a Manager over a throwaway /data holding one router. The
// router is never reachable — 198.51.100.x is TEST-NET-2 — which is exactly
// right here: every assertion below is about the session's LIFECYCLE, and a
// session exists from the moment it is acquired whether or not it connects.
func graceManager(t *testing.T, grace time.Duration) *Manager {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "test-secret")
	for name, body := range map[string]string{
		"settings.json": `{}`,
		"routers.json": `[{"id":"r1","label":"lab","host":"198.51.100.77","port":8728,
		  "username":"u","password":""}]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, hub.New())
	m.SetIdleGrace(grace)
	t.Cleanup(m.Shutdown)
	return m
}

// waitFor polls until cond or the deadline. The teardown happens on a timer
// goroutine, so a bare read after a sleep is a race in the other direction:
// it can pass by being slow rather than by being right.
func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// THE POINT OF THE WHOLE CHANGE: a refresh is a Release immediately followed by
// an Acquire, and the session — with the traffic and ping history inside it —
// must be the SAME one.
func TestARefreshInsideTheGraceKeepsTheSameSession(t *testing.T) {
	m := graceManager(t, 300*time.Millisecond)

	first, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	m.Release("r1") // the socket closing

	second, err := m.Acquire("r1") // and the new one arriving
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("a refresh inside the grace built a NEW session — the history is gone, " +
			"which is the bug this exists to prevent")
	}

	// AND THE PENDING TEARDOWN IS CANCELLED, not merely outrun. Without the
	// Stop in Acquire the timer still fires and tears down the session this
	// viewer is now using.
	time.Sleep(500 * time.Millisecond)
	if _, live := m.Live()["r1"]; !live {
		t.Error("the session was torn down under a viewer who had come back")
	}
	first.mu.Lock()
	closed := first.closed
	first.mu.Unlock()
	if closed {
		t.Error("the session was marked closed under a live viewer")
	}
}

// The other direction, which is the half that must not be lost: the idle gate
// still closes. A grace that never expires is just a leak.
func TestStayingAwayPastTheGraceStillTearsDown(t *testing.T) {
	m := graceManager(t, 50*time.Millisecond)

	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	if _, live := m.Live()["r1"]; !live {
		t.Fatal("no session after Acquire")
	}
	m.Release("r1")

	waitFor(t, "the session to be torn down", 2*time.Second, func() bool {
		_, live := m.Live()["r1"]
		return !live
	})
}

// The pool hand-off moved with the teardown, and this is why it had to: while a
// session lingers it is still in Live(), and `syncAlertPool` excludes anything
// there. Firing the hook at Release would tell the pool to reclaim a router the
// session has not let go of — and nothing would call it again once it had.
func TestOnIdleFiresAtTeardownAndNotAtRelease(t *testing.T) {
	m := graceManager(t, 60*time.Millisecond)

	fired := make(chan string, 4)
	m.SetOnIdle(func(id string) { fired <- id })

	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	m.Release("r1")

	select {
	case id := <-fired:
		t.Fatalf("onIdle fired for %q at Release, while the session was still live", id)
	case <-time.After(20 * time.Millisecond):
	}

	select {
	case id := <-fired:
		if id != "r1" {
			t.Errorf("onIdle fired for %q, want r1", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onIdle never fired — the pool would never reclaim this router")
	}
}

// A second viewer holds the session open on its own. The grace is about the
// LAST one leaving, and a Release that armed the timer while somebody was still
// watching would tear the session down under them.
func TestASecondViewerKeepsTheSessionWithoutAnyGrace(t *testing.T) {
	m := graceManager(t, 40*time.Millisecond)

	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	m.Release("r1") // one of two leaves

	time.Sleep(200 * time.Millisecond) // well past the grace
	if _, live := m.Live()["r1"]; !live {
		t.Fatal("the session went while a viewer still held it")
	}

	m.Release("r1") // and now the last one
	waitFor(t, "the session to go after the last viewer", 2*time.Second, func() bool {
		_, live := m.Live()["r1"]
		return !live
	})
}

// The default is what ships, so it is worth one assertion: a Manager nobody
// configured must not tear sessions down instantly, and must not linger for ever.
func TestTheDefaultGraceIsTwoMinutes(t *testing.T) {
	if DefaultIdleGrace != 2*time.Minute {
		t.Errorf("DefaultIdleGrace = %s, want 2m", DefaultIdleGrace)
	}
	m := &Manager{}
	if got := m.grace(); got != DefaultIdleGrace {
		t.Errorf("an unconfigured Manager graces for %s, want the default %s", got, DefaultIdleGrace)
	}
}

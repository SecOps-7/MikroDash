package session

import (
	"strings"
	"testing"
	"time"
)

// CloseNow answers a different question from Release, and the difference is the
// whole point: Release means "a viewer left" and grants the idle grace; CloseNow
// means "this router is being disabled or deleted" and takes effect at once.
//
// The grace here is an HOUR, so "it tore down" cannot be confused with "the test
// waited long enough".
func TestCloseNowTearsDownImmediately(t *testing.T) {
	m := graceManager(t, time.Hour)

	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	if _, live := m.Live()["r1"]; !live {
		t.Fatal("no session after Acquire")
	}

	m.CloseNow("r1")

	if _, live := m.Live()["r1"]; live {
		t.Error("CloseNow left the session live — a disabled router keeps being polled")
	}
}

// THE REGRESSION IT EXISTS FOR. `routers_api.go` used Release when Release WAS a
// teardown; the idle grace turned those two call sites into a two-minute delay
// against a router the operator had just switched off. Both halves are asserted,
// because the value is in the contrast.
func TestReleaseStillGracesWhileCloseNowDoesNot(t *testing.T) {
	m := graceManager(t, time.Hour)

	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	m.Release("r1")
	if _, live := m.Live()["r1"]; !live {
		t.Fatal("Release tore the session down instead of granting the grace")
	}

	m.CloseNow("r1")
	if _, live := m.Live()["r1"]; live {
		t.Error("CloseNow honoured the grace; it must not")
	}
}

// Refs are ZEROED, not decremented, and that is the safety argument: neither
// caller ever held a reference, so decrementing would be spending somebody
// else's. Disabling a router two people are watching must still stop the
// polling.
func TestCloseNowIgnoresRemainingViewers(t *testing.T) {
	m := graceManager(t, time.Hour)

	for i := 0; i < 3; i++ {
		if _, err := m.Acquire("r1"); err != nil {
			t.Fatal(err)
		}
	}
	m.CloseNow("r1")
	if _, live := m.Live()["r1"]; live {
		t.Error("three viewers kept a disabled router's session alive")
	}

	// AND NO NEGATIVE REFCOUNT IS REACHABLE, because the counter is gone rather
	// than adjusted: those viewers' own Release calls miss the map and return.
	for i := 0; i < 3; i++ {
		m.Release("r1")
	}
	if _, live := m.Live()["r1"]; live {
		t.Error("a late Release brought the session back")
	}

	// A later Acquire must build a NEW session rather than resurrect the closed
	// one, whose collectors are all stopped and whose client is closed.
	again, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	again.mu.Lock()
	closed := again.closed
	again.mu.Unlock()
	if closed {
		t.Error("Acquire handed back the closed session")
	}
}

// Both are ordinary: a delete can arrive for a router nobody ever opened, and a
// disable can be clicked twice.
func TestCloseNowOnAnUnknownOrAlreadyClosedRouter(t *testing.T) {
	m := graceManager(t, time.Hour)
	m.CloseNow("never-acquired")

	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	m.CloseNow("r1")
	m.CloseNow("r1")
}

// A pending grace must not outlive the session it was granted for: without the
// Stop in CloseNow the timer still fires an hour later against a router that was
// deleted.
func TestCloseNowCancelsAPendingLinger(t *testing.T) {
	m := graceManager(t, 40*time.Millisecond)

	if _, err := m.Acquire("r1"); err != nil {
		t.Fatal(err)
	}
	m.Release("r1") // arms the timer
	m.CloseNow("r1")

	// Past the grace, so a surviving timer would have fired by now.
	time.Sleep(200 * time.Millisecond)
	if _, live := m.Live()["r1"]; live {
		t.Error("the session came back")
	}
}

// ── THE RECONNECT LEAK, READ OUT OF THE SOURCE ─────────────────────────────
//
// The property is "connectLoop closes the client it is abandoning", and nothing
// observable distinguishes a closed client from an abandoned one: the collectors
// fail identically either way, and the evidence is a `/user/active` row on a
// router this test does not have. That is the shape release_test.go already
// uses — measure the source, and fail when the anchor moves.
//
// What it prevents: `s.client = nil` with no Close. In go-routeros v3.0.1 a
// failing asyncLoop calls closeTags and never touches the socket, and `Async`
// parks a goroutine on context.Background() that keeps the client reachable for
// ever — so the fd, the router-side session and the goroutines are not merely
// delayed, they are permanent. The two POOLS have always done this correctly;
// only internal/session did not.
func TestConnectLoopClosesTheClientItAbandons(t *testing.T) {
	body := blockBetween(t, sessionSource(t), "s.waitUntilDown(c)", "if !down {")

	if !strings.Contains(body, "c.Close()") {
		t.Fatal("connectLoop drops its client without closing it: the socket stays " +
			"established and logged in, and the router keeps the /user/active entry " +
			"for ever")
	}
	// AFTER the pointer is cleared. Closing while `s.client` still points at it
	// would hand a closed client to anything that read it in between.
	nilled := strings.Index(body, "s.client = nil")
	closed := strings.Index(body, "c.Close()")
	if nilled >= 0 && closed < nilled {
		t.Error("c.Close() runs before s.client is cleared")
	}
}

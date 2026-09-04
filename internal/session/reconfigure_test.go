package session

import (
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// endpointOf is the six fields that decide the connection, read the way the
// connect loop reads them.
func endpointOf(s *Session) routeros.Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// TestReconfigureRepointsALiveSession.
//
// ── ACQUIRE READ THE RECORD ONCE AND NOTHING RE-READ IT ────────────────────
//
// Two comments in session.go asserted that a live edit rebuilt the session,
// "which is what the live `collectionFingerprint` exists to decide". That
// mechanism was never ported — there is no fingerprint in this tree — so
// correcting a router's password did not reach the connection failing on the
// old one.
//
// That is the recovery path for issue #124, whose credential was destroyed by a
// separate bug: the operator retypes the password, and without this the file
// changed and the session went on presenting the old one every five seconds.
func TestReconfigureRepointsALiveSession(t *testing.T) {
	m := graceManager(t, time.Hour)
	s, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	if got := endpointOf(s).Password; got != "" {
		t.Fatalf("the fixture's password is %q", got)
	}

	moved := routeros.Config{
		Host: "198.51.100.88", Port: 8729,
		Username: "u2", Password: "corrected", TLS: true,
	}
	if !m.Reconfigure("r1", moved) {
		t.Fatal("Reconfigure reported no change against a different endpoint")
	}
	got := endpointOf(s)
	if !sameConnection(got, moved) {
		t.Errorf("the session still dials %s:%d as %q with password %q",
			got.Host, got.Port, got.Username, got.Password)
	}

	// THE SAME SESSION, not a replacement. Tearing it down would strand whoever
	// is watching: collectors stopped, room still joined, page frozen.
	if again, err := m.Acquire("r1"); err != nil || again != s {
		t.Errorf("the session was replaced rather than repointed (err=%v)", err)
	}
	m.Release("r1")
}

// TestOnlyThePasswordChanged is the #124 recovery path exactly, and it is a
// separate case from the one above deliberately.
//
// The test above moves host, port, username and TLS together, so a comparison
// that had DROPPED the password would still report a change and still pass. A
// mutation removing `a.Password == b.Password` survived it. The operator
// recovering from a destroyed credential changes ONE field, and it is that one.
func TestOnlyThePasswordChanged(t *testing.T) {
	m := graceManager(t, time.Hour)
	s, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	retyped := endpointOf(s)
	retyped.Password = "the-one-the-router-actually-wants"

	if !m.Reconfigure("r1", retyped) {
		t.Fatal("retyping ONLY the password was not treated as a change, so the " +
			"session keeps dialling the credential the router is rejecting")
	}
	if got := endpointOf(s).Password; got != "the-one-the-router-actually-wants" {
		t.Errorf("the session still holds %q", got)
	}
	m.Release("r1")
}

// TestReconfigureIgnoresAnUnrelatedEdit — the other direction. Every save from
// the Edit dialog calls this, and most change a label or a poll interval; a
// comparison that answered "changed" to those would drop a working connection
// on every edit in the fleet.
func TestReconfigureIgnoresAnUnrelatedEdit(t *testing.T) {
	m := graceManager(t, time.Hour)
	s, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	same := endpointOf(s)
	// `Label` and `Debug` are the session's own and must not count as a change.
	same.Label = "Renamed"
	same.Debug = !same.Debug
	if m.Reconfigure("r1", same) {
		t.Error("a rename was treated as an endpoint change and dropped the connection")
	}
	m.Release("r1")
}

// TestReconfigureOnARouterNobodyIsWatchingIsHarmless. The record is still
// written; the pool holds that router, and `Acquire` reads the file next time.
func TestReconfigureOnARouterNobodyIsWatchingIsHarmless(t *testing.T) {
	m := graceManager(t, time.Hour)
	if m.Reconfigure("r1", routeros.Config{Host: "198.51.100.99"}) {
		t.Error("reported a change against a session that does not exist")
	}
	if m.Reconfigure("nosuch", routeros.Config{Host: "198.51.100.99"}) {
		t.Error("reported a change for an unknown router")
	}
}

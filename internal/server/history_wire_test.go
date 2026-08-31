package server

import "testing"

// ── OFF RETURNS A DISABLED WIRE, NOT NIL ───────────────────────────────────
//
// The distinction is the whole point of building this before the window. A nil
// would make the cutover the first time `session.go`'s Record and Flush calls
// ever executed; a disabled wire means they run on every tick, all the way to
// the guard, and do nothing.
func TestTheHistoryWireIsBuiltButDisabledByDefault(t *testing.T) {
	s := schedServer(t, `[]`)
	w := s.buildHistoryWire(false)
	if w == nil {
		t.Fatal("a nil wire: the call sites in session.go would then be unexercised " +
			"until the cutover window, which is what building this early avoids")
	}
	if w.Enabled() {
		t.Error("the wire reports enabled with the flag off")
	}
}

func TestTheHistoryWireRecordsWhenAskedTo(t *testing.T) {
	s := schedServer(t, `[]`)
	w := s.buildHistoryWire(true)
	if w == nil || !w.Enabled() {
		t.Errorf("the flag was set and the wire is %v", w)
	}
}

// NO DATABASE IS NIL, not a wire with nowhere to write. `historywire.New` would
// take a nil Store happily and drop every row at persist time, which looks
// identical to recording correctly.
func TestTheHistoryWireNeedsADatabase(t *testing.T) {
	s := schedServer(t, `[]`)
	s.auditDB = nil
	if w := s.buildHistoryWire(true); w != nil {
		t.Errorf("a wire was built with no database: %v", w)
	}
}

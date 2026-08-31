package historywire

import "testing"

const t0 = int64(1699996800000)

func rows(s *fakeStore) []string {
	out := make([]string, 0, len(s.rows))
	for _, r := range s.rows {
		state := "down"
		if r.Connected {
			state = "up"
		}
		out = append(out, r.Table+":"+state)
	}
	return out
}

// ONLY A REAL TRANSITION WRITES.
//
// A reconnect that was never seen to drop produces no row and no status — rule
// 1. Without it every session rebuild would write an "up" row for a router that
// had never been recorded down, and the Connectivity report would show an
// outage-free router flapping.
func TestOnlyATransitionWrites(t *testing.T) {
	w, s := on(t)
	th := ThresholdMs(0, true) // zero: no debounce, so the rules are visible

	if st := w.Connected("r-1", th, t0); len(st) != 1 || !st[0] {
		t.Fatalf("first connect returned %v", st)
	}
	if got := rows(s); len(got) != 1 || got[0] != "connectivity:up" {
		t.Fatalf("first connect wrote %v", got)
	}
	// A SECOND connect with no drop between: nothing.
	before := len(s.rows)
	w.Connected("r-1", th, t0+1000)
	if len(s.rows) != before {
		t.Errorf("a repeat connect wrote %v", rows(s)[before:])
	}
}

// A THRESHOLD OF ZERO MEANS ZERO, not "unset".
//
// The operator asked for no debounce; substituting the 30s default would delay
// every outage row by half a minute for exactly the routers whose owner wanted
// the opposite.
func TestAZeroThresholdIsNotTheDefault(t *testing.T) {
	if got := ThresholdMs(0, true); got != 0 {
		t.Errorf("ThresholdMs(0, present) = %d, want 0", got)
	}
	if got := ThresholdMs(0, false); got != 30_000 {
		t.Errorf("an ABSENT threshold = %d, want the 30s default", got)
	}
	// OUT OF RANGE TAKES THE DEFAULT, not the bound: the live expression is
	// `(n >= 0 && n <= 300) ? n : 30`, so 500 becomes 30 rather than 300.
	if got := ThresholdMs(500, true); got != 30_000 {
		t.Errorf("500s = %d, want the 30s default rather than the 300s clamp", got)
	}
	if got := ThresholdMs(-1, true); got != 30_000 {
		t.Errorf("-1s = %d, want the default", got)
	}
	if got := ThresholdMs(300, true); got != 300_000 {
		t.Errorf("300s = %d, want 300s — the bound itself is in range", got)
	}
}

// THE DEBOUNCE WRITES THE OBSERVED MOMENT, NOT THE MOMENT IT FIRED.
//
// Rule 2, and the reason the live app passes the timestamp explicitly: without
// it every outage is recorded `connDownThresholdSec` late and reads as shorter
// than it was.
func TestTheDebouncedRowCarriesTheObservedMoment(t *testing.T) {
	w, s := on(t)
	th := int64(30_000)
	w.Connected("r-1", th, t0)
	s.rows = nil

	// The link drops. Nothing is written yet.
	if st := w.Disconnected("r-1", th, t0+1000); len(st) != 0 {
		t.Errorf("a drop inside the debounce reported %v", st)
	}
	if len(s.rows) != 0 {
		t.Fatalf("a drop inside the debounce wrote %v", rows(s))
	}
	// A tick before the threshold does nothing.
	w.Tick("r-1", th, t0+20_000)
	if len(s.rows) != 0 {
		t.Fatalf("an early tick wrote %v", rows(s))
	}
	// And after it, the row appears — stamped when the link WENT, not now.
	st := w.Tick("r-1", th, t0+40_000)
	if len(st) != 1 || st[0] {
		t.Fatalf("the firing tick reported %v, want one 'down'", st)
	}
	if len(s.rows) != 1 {
		t.Fatalf("wrote %v", rows(s))
	}
	if s.rows[0].TS != t0+1000 {
		t.Errorf("row stamped %d, want the observed drop at %d — an outage recorded when the "+
			"timer fired reads as 29 seconds shorter than it was", s.rows[0].TS, t0+1000)
	}
	if !s.rows[0].ExplicitTS {
		t.Error("the debounced row did not mark its timestamp explicit — rule 2")
	}
}

// A SECOND DROP DURING THE DEBOUNCE MUST NOT RE-ARM IT.
//
// A flapping link would otherwise postpone its own outage row indefinitely and
// never be recorded down at all.
func TestAFlappingLinkCannotPostponeItsOwnOutage(t *testing.T) {
	w, s := on(t)
	th := int64(30_000)
	w.Connected("r-1", th, t0)
	s.rows = nil

	w.Disconnected("r-1", th, t0+1000)
	for i := 1; i <= 5; i++ {
		w.Disconnected("r-1", th, t0+1000+int64(i)*5000)
	}
	w.Tick("r-1", th, t0+40_000)
	if len(s.rows) != 1 {
		t.Fatalf("wrote %v, want exactly one outage row", rows(s))
	}
	if s.rows[0].TS != t0+1000 {
		t.Errorf("row stamped %d — a later drop re-armed the timer and moved the "+
			"observed moment", s.rows[0].TS)
	}
}

// A CONNECT CANCELS A PENDING DEBOUNCE OUTRIGHT, so a blip shorter than the
// threshold is never recorded as an outage at all.
func TestAConnectCancelsThePendingDebounce(t *testing.T) {
	w, s := on(t)
	th := int64(30_000)
	w.Connected("r-1", th, t0)
	s.rows = nil

	w.Disconnected("r-1", th, t0+1000)
	w.Connected("r-1", th, t0+5000) // back before the threshold
	w.Tick("r-1", th, t0+60_000)
	if len(s.rows) != 0 {
		t.Errorf("a blip shorter than the debounce was recorded as %v", rows(s))
	}
}

// A FIRST SIGHTING THAT IS ALREADY DOWN writes immediately — rule 3. There is no
// previous state to debounce against.
func TestAFirstSightingDownWritesAtOnce(t *testing.T) {
	w, s := on(t)
	th := int64(30_000)
	st := w.Disconnected("r-1", th, t0)
	if len(st) != 1 || st[0] {
		t.Fatalf("reported %v", st)
	}
	if got := rows(s); len(got) != 1 || got[0] != "connectivity:down" {
		t.Errorf("wrote %v, want one down row without waiting for the debounce", got)
	}
}

// ROUTERS ARE INDEPENDENT: one router's outage must not suppress another's.
func TestConnectivityIsPerRouter(t *testing.T) {
	w, s := on(t)
	th := ThresholdMs(0, true)
	w.Connected("r-a", th, t0)
	w.Connected("r-b", th, t0)
	s.rows = nil
	w.Disconnected("r-a", th, t0+1000)
	if len(s.rows) != 1 || s.rows[0].RouterID != "r-a" {
		t.Fatalf("wrote %v", s.rows)
	}
	// r-b is still up, so its own drop still writes.
	w.Disconnected("r-b", th, t0+2000)
	if len(s.rows) != 2 || s.rows[1].RouterID != "r-b" {
		t.Errorf("r-b's drop wrote %v", s.rows)
	}
}

// FORGET IS FOR A ROUTER THAT IS GONE, and dropping state on a mere disconnect
// would make the next connect look like a first sighting.
func TestForgetResetsTheStateAndIsNotCalledOnDisconnect(t *testing.T) {
	w, s := on(t)
	th := ThresholdMs(0, true)
	w.Connected("r-1", th, t0)
	w.Disconnected("r-1", th, t0+1000)
	s.rows = nil

	// Reconnecting writes an up row, because it IS a transition.
	w.Connected("r-1", th, t0+2000)
	if len(s.rows) != 1 {
		t.Fatalf("reconnect wrote %v", rows(s))
	}
	s.rows = nil

	// After Forget the router is unknown again, so the next connect is a first
	// sighting — which writes. That is why Forget is not called on a disconnect.
	w.Forget("r-1")
	w.Connected("r-1", th, t0+3000)
	if len(s.rows) != 1 {
		t.Errorf("after Forget, a connect wrote %v — a forgotten router's first "+
			"sighting should write", rows(s))
	}
}

// THE THRESHOLD IS RE-READ ON EVERY EVENT, not captured when the router is
// first seen.
//
// An operator can change `connDownThresholdSec` while a session is live, and the
// live code reads it per event for that reason. Captured instead, the change
// would appear to save and do nothing until the process restarted — the worst
// kind of settings bug, because the field shows the new value.
func TestTheThresholdIsRereadOnEveryEvent(t *testing.T) {
	w, s := on(t)

	// Seen first with a 30s debounce.
	w.Connected("r-1", 30_000, t0)
	s.rows = nil

	// The operator sets it to zero, then the link drops. With the threshold
	// re-read, that is "declare it down immediately" and the row is written now.
	if st := w.Disconnected("r-1", 0, t0+1000); len(st) != 1 || st[0] {
		t.Fatalf("the drop reported %v, want one 'down'", st)
	}
	if len(s.rows) != 1 {
		t.Errorf("wrote %v — the new zero threshold was ignored and the drop was "+
			"debounced against the old 30s", rows(s))
	}
}

// And the other direction: raising it mid-session starts debouncing.
func TestRaisingTheThresholdStartsDebouncing(t *testing.T) {
	w, s := on(t)
	w.Connected("r-1", 0, t0)
	s.rows = nil
	if st := w.Disconnected("r-1", 30_000, t0+1000); len(st) != 0 {
		t.Errorf("the drop reported %v, want nothing — it should now debounce", st)
	}
	if len(s.rows) != 0 {
		t.Errorf("wrote %v with a 30s threshold newly in force", rows(s))
	}
}

// A DISABLED WIRE records no connectivity either, and reports no status.
func TestADisabledWireIgnoresConnectivity(t *testing.T) {
	s := &fakeStore{}
	w := New(false, s)
	th := ThresholdMs(0, true)
	if st := w.Connected("r-1", th, t0); st != nil {
		t.Errorf("a disabled wire reported %v", st)
	}
	w.Disconnected("r-1", th, t0+1000)
	w.Tick("r-1", th, t0+60_000)
	if len(s.rows) != 0 {
		t.Errorf("a disabled wire wrote %v", rows(s))
	}
}

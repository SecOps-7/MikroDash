package connstate

import (
	"testing"

	"mikrodash/internal/history"
)

// The outage debounce and the clock that makes it work.
//
// ── MOVED HERE FROM internal/historywire, WITH ITS PROPERTIES INTACT ───────
//
// These were `conn_test.go` and `debounce_test.go` in the history wire, and
// what they pin has not changed: rule 1 to rule 4, the observed moment, the
// flap that cannot postpone its own outage, and the fleet-wide tick. What HAS
// changed is who asks. The threshold is declared per router rather than passed
// on every call, and the verdict goes to a callback rather than being returned,
// because the badge and the alert consume it as well as the recorder.
//
// ── A SIX-SECOND RECONNECT WAS BEING FILED AS AN OUTAGE ────────────────────
//
// `history.Connectivity` holds no timer: the caller supplies time through
// `Tick`, which is what makes its rules testable without one. Nothing called
// it, so a non-zero threshold could never fire and the only workable setting
// was zero — its own branch, meaning "record every close immediately". The
// Reports page then showed the active router flapping, because a routine
// reconnect takes about five seconds and every one was written down.

const t0 = int64(1699996800000)
const minute = int64(60_000)

// recorder is both sinks, so every test can assert on either without a second
// harness. A tracker is built with both by default; the tests that are about a
// missing sink build their own.
type recorder struct {
	rows     []history.Row
	verdicts []string // "r-1:up" / "r-1:down", in order
}

func (r *recorder) row(rows []history.Row) { r.rows = append(r.rows, rows...) }

func (r *recorder) verdict(routerID string, up bool) {
	state := "down"
	if up {
		state = "up"
	}
	r.verdicts = append(r.verdicts, routerID+":"+state)
}

func on(t *testing.T) (*Tracker, *recorder) {
	t.Helper()
	rec := &recorder{}
	return New(rec.row, rec.verdict), rec
}

func states(r *recorder) []string {
	out := make([]string, 0, len(r.rows))
	for _, row := range r.rows {
		state := "down"
		if row.Connected {
			state = "up"
		}
		out = append(out, row.Table+":"+state)
	}
	return out
}

// ONLY A REAL TRANSITION WRITES.
//
// A reconnect that was never seen to drop produces no row — rule 1. Without it
// every session rebuild would write an "up" row for a router that had never
// been recorded down, and the Connectivity report would show an outage-free
// router flapping.
func TestOnlyATransitionWrites(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", ThresholdMs(0, true)) // zero: the rules are visible

	tr.Connected("r-1", t0)
	if got := states(rec); len(got) != 1 || got[0] != "connectivity:up" {
		t.Fatalf("first connect wrote %v", got)
	}
	// A SECOND connect with no drop between: no row. The VERDICT is still
	// reported — live's rule is that status goes out on every connect — and the
	// badge is idempotent, so that costs nothing and keeps one path for both.
	before := len(rec.rows)
	tr.Connected("r-1", t0+1000)
	if len(rec.rows) != before {
		t.Errorf("a repeat connect wrote %v", states(rec)[before:])
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

// AN UNDECLARED ROUTER GETS THE LIVE DEFAULT, not zero.
//
// The threshold is a map entry now, and a map's zero value is 0 — which is a
// REAL setting meaning "record every close at once". A router whose threshold
// had not been declared yet would silently have had the operator's debounce
// turned off, and the only symptom is a Reports page full of six-second
// outages.
func TestAnUndeclaredRouterDebouncesByDefault(t *testing.T) {
	tr, rec := on(t)
	tr.Connected("r-1", t0)
	rec.rows = nil

	tr.Disconnected("r-1", t0+1000)
	if len(rec.rows) != 0 {
		t.Fatalf("a drop on an undeclared router wrote %v at once — its threshold "+
			"defaulted to zero rather than to the live 30s", states(rec))
	}
	tr.TickAll(t0 + 40_000)
	if len(rec.rows) != 1 {
		t.Errorf("wrote %v after the default threshold elapsed, want one outage", states(rec))
	}
}

// THE DEBOUNCE WRITES THE OBSERVED MOMENT, NOT THE MOMENT IT FIRED.
//
// Rule 2, and the reason the live app passes the timestamp explicitly: without
// it every outage is recorded `connDownThresholdSec` late and reads as shorter
// than it was.
func TestTheDebouncedRowCarriesTheObservedMoment(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.Connected("r-1", t0)
	rec.rows, rec.verdicts = nil, nil

	// The link drops. Nothing is written yet, and nothing is DECLARED yet: the
	// badge must not move for a blip.
	tr.Disconnected("r-1", t0+1000)
	if len(rec.rows) != 0 || len(rec.verdicts) != 0 {
		t.Fatalf("a drop inside the debounce wrote %v and declared %v", states(rec), rec.verdicts)
	}
	// A tick before the threshold does nothing.
	tr.TickAll(t0 + 20_000)
	if len(rec.rows) != 0 {
		t.Fatalf("an early tick wrote %v", states(rec))
	}
	// And after it, the row appears — stamped when the link WENT, not now.
	tr.TickAll(t0 + 40_000)
	if len(rec.rows) != 1 {
		t.Fatalf("wrote %v", states(rec))
	}
	if len(rec.verdicts) != 1 || rec.verdicts[0] != "r-1:down" {
		t.Errorf("the expiring debounce declared %v, want one r-1:down", rec.verdicts)
	}
	if rec.rows[0].TS != t0+1000 {
		t.Errorf("row stamped %d, want the observed drop at %d — an outage recorded when the "+
			"timer fired reads as 29 seconds shorter than it was", rec.rows[0].TS, t0+1000)
	}
	if !rec.rows[0].ExplicitTS {
		t.Error("the debounced row did not mark its timestamp explicit — rule 2")
	}
}

// A SECOND DROP DURING THE DEBOUNCE MUST NOT RE-ARM IT.
//
// A flapping link would otherwise postpone its own outage row indefinitely and
// never be recorded down at all.
func TestAFlappingLinkCannotPostponeItsOwnOutage(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.Connected("r-1", t0)
	rec.rows = nil

	tr.Disconnected("r-1", t0+1000)
	for i := 1; i <= 5; i++ {
		tr.Disconnected("r-1", t0+1000+int64(i)*5000)
	}
	tr.TickAll(t0 + 40_000)
	if len(rec.rows) != 1 {
		t.Fatalf("wrote %v, want exactly one outage row", states(rec))
	}
	if rec.rows[0].TS != t0+1000 {
		t.Errorf("row stamped %d — a later drop re-armed the timer and moved the "+
			"observed moment", rec.rows[0].TS)
	}
}

// A CONNECT CANCELS A PENDING DEBOUNCE OUTRIGHT, so a blip shorter than the
// threshold is never recorded as an outage and never reaches the badge.
//
// This is the six-second reconnect the operator was seeing filed as an outage.
func TestABlipShorterThanTheThresholdIsNeitherRecordedNorDeclared(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.Connected("r-1", t0)
	rec.rows, rec.verdicts = nil, nil

	tr.Disconnected("r-1", t0+1000)
	tr.TickAll(t0 + 3000)
	tr.Connected("r-1", t0+7000) // back before the threshold
	tr.TickAll(t0 + 60_000)      // long past it, but it was cancelled

	if len(rec.rows) != 0 {
		t.Errorf("a six-second reconnect was recorded as %v", states(rec))
	}
	for _, v := range rec.verdicts {
		if v == "r-1:down" {
			t.Errorf("a six-second reconnect declared the router offline: %v — the "+
				"Devices page goes red for a blip", rec.verdicts)
		}
	}
}

// AN OUTAGE LONGER THAN THE THRESHOLD IS RECORDED AND DECLARED. The other
// direction of the test above, or a tracker that never fires would pass it.
func TestAnOutageLongerThanTheThresholdIsRecorded(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.Connected("r-1", t0)
	rec.rows, rec.verdicts = nil, nil

	tr.Disconnected("r-1", t0+1000)
	tr.TickAll(t0 + 20_000)
	if len(rec.rows) != 0 {
		t.Fatalf("wrote before the threshold elapsed")
	}
	tr.TickAll(t0 + 40_000)
	if len(rec.rows) == 0 {
		t.Fatal("a real outage was never recorded — the debounce swallowed it")
	}
	if len(rec.verdicts) != 1 || rec.verdicts[0] != "r-1:down" {
		t.Errorf("declared %v, want one r-1:down", rec.verdicts)
	}
}

// TICKALL IS WHAT FIRES IT — without a caller the debounce never expires, which
// is why zero was the only usable threshold before it existed.
func TestTickAllIsWhatFiresIt(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.Connected("r-1", t0)
	before := len(rec.rows)
	tr.Disconnected("r-1", t0+1000)

	if len(rec.rows) != before {
		t.Fatal("the disconnect wrote immediately at a non-zero threshold")
	}
	tr.TickAll(t0 + minute)
	if len(rec.rows) == before {
		t.Error("TickAll did not fire the expired debounce; a non-zero threshold " +
			"can then never record an outage at all")
	}
}

// A ZERO THRESHOLD STILL RECORDS AT ONCE — rule 4. Zero is a deliberate
// setting with its own branch, not an absence.
func TestAZeroThresholdStillRecordsAtOnce(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 0)
	tr.Connected("r-1", t0)
	before := len(rec.rows)
	tr.Disconnected("r-1", t0+1000)
	if len(rec.rows) == before {
		t.Error("a zero threshold did not record the close immediately")
	}
}

// A FIRST SIGHTING THAT IS ALREADY DOWN writes immediately — rule 3. There is no
// previous state to debounce against.
func TestAFirstSightingDownWritesAtOnce(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.Disconnected("r-1", t0)
	if got := states(rec); len(got) != 1 || got[0] != "connectivity:down" {
		t.Errorf("wrote %v, want one down row without waiting for the debounce", got)
	}
}

// ROUTERS ARE INDEPENDENT: one router's outage must not suppress another's, and
// a fleet-wide sweep is where that is easy to get wrong.
func TestConnectivityIsPerRouter(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-a", 0)
	tr.SetThreshold("r-b", 0)
	tr.Connected("r-a", t0)
	tr.Connected("r-b", t0)
	rec.rows = nil

	tr.Disconnected("r-a", t0+1000)
	if len(rec.rows) != 1 || rec.rows[0].RouterID != "r-a" {
		t.Fatalf("wrote %v", rec.rows)
	}
	tr.Disconnected("r-b", t0+2000)
	if len(rec.rows) != 2 || rec.rows[1].RouterID != "r-b" {
		t.Errorf("r-b's drop wrote %v", rec.rows)
	}
}

// AND SO IS THE FLEET-WIDE TICK.
func TestTickAllIsPerRouter(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.SetThreshold("r-2", 30_000)
	tr.Connected("r-1", t0)
	tr.Connected("r-2", t0)
	before := len(rec.rows)

	tr.Disconnected("r-1", t0+1000)
	tr.TickAll(t0 + 40_000)

	got := rec.rows[before:]
	if len(got) != 1 {
		t.Fatalf("one router's outage wrote %d rows", len(got))
	}
	if got[0].RouterID != "r-1" {
		t.Errorf("the outage was filed against %s", got[0].RouterID)
	}
}

// FORGET IS FOR A ROUTER THAT IS GONE, and dropping state on a mere disconnect
// would make the next connect look like a first sighting.
func TestForgetResetsTheStateAndIsNotCalledOnDisconnect(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 0)
	tr.Connected("r-1", t0)
	tr.Disconnected("r-1", t0+1000)
	rec.rows = nil

	// Reconnecting writes an up row, because it IS a transition.
	tr.Connected("r-1", t0+2000)
	if len(rec.rows) != 1 {
		t.Fatalf("reconnect wrote %v", states(rec))
	}
	rec.rows = nil

	// After Forget the router is unknown again, so the next connect is a first
	// sighting — which writes. That is why Forget is not called on a disconnect.
	tr.Forget("r-1")
	tr.Connected("r-1", t0+3000)
	if len(rec.rows) != 1 {
		t.Errorf("after Forget, a connect wrote %v — a forgotten router's first "+
			"sighting should write", states(rec))
	}
}

// ── THE THRESHOLD APPLIES ON SAVE, WHICH IS WHY IT IS DECLARED HERE ────────
//
// The old shape passed `threshMs` on every call and the state machine re-read
// it, which a test in `historywire` pinned. The staleness was one level up: the
// session resolved the value when it was BUILT and passed that frozen copy for
// the router's whole life, and a router held for alerting or recording is never
// rebuilt. An operator changed the Offline threshold, saw it save, and got the
// old behaviour until the process restarted.
//
// The argument is gone. What replaces that test is this: a declaration on a
// router the tracker is ALREADY watching takes effect on its next event.
func TestANewThresholdAppliesToARouterAlreadyTracked(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 30_000)
	tr.Connected("r-1", t0)
	rec.rows = nil

	// The operator sets it to zero: "declare it down immediately".
	tr.SetThreshold("r-1", 0)
	tr.Disconnected("r-1", t0+1000)
	if len(rec.rows) != 1 {
		t.Errorf("wrote %v — the saved zero threshold was ignored and the drop was "+
			"debounced against the old 30s, which is the restart-required bug", states(rec))
	}
}

// And the other direction, or the test above passes against a tracker that
// records everything at once.
func TestRaisingTheThresholdStartsDebouncing(t *testing.T) {
	tr, rec := on(t)
	tr.SetThreshold("r-1", 0)
	tr.Connected("r-1", t0)
	rec.rows = nil

	tr.SetThreshold("r-1", 30_000)
	tr.Disconnected("r-1", t0+1000)
	if len(rec.rows) != 0 {
		t.Errorf("wrote %v with a 30s threshold newly in force", states(rec))
	}
}

// ── THE VERDICT DOES NOT DEPEND ON THE RECORDER ────────────────────────────
//
// This is why the debounce left `internal/historywire`. `-history` defaults to
// FALSE and every entry point there returned early on a disabled wire, so the
// state machine never ran on a default install. A badge and an alert hung off
// that would have been dead on every install that had not opted into recording.
//
// A tracker with NO row sink at all is that install, and it must still decide.
func TestTheVerdictArrivesWithNothingRecording(t *testing.T) {
	rec := &recorder{}
	tr := New(nil, rec.verdict)
	tr.SetThreshold("r-1", 30_000)

	tr.Connected("r-1", t0)
	tr.Disconnected("r-1", t0+1000)
	tr.TickAll(t0 + 40_000)
	tr.Connected("r-1", t0+50_000)

	want := []string{"r-1:up", "r-1:down", "r-1:up"}
	if len(rec.verdicts) != len(want) {
		t.Fatalf("declared %v, want %v — with no recorder the debounce decided nothing",
			rec.verdicts, want)
	}
	for i := range want {
		if rec.verdicts[i] != want[i] {
			t.Fatalf("declared %v, want %v", rec.verdicts, want)
		}
	}
}

// ── "NEVER SEEN" IS NOT "DOWN" ─────────────────────────────────────────────
//
// `Online` answers the badge, and conflating an unobserved router with an
// offline one is what painted every card red for the seconds before the first
// dial returned. Reported twice.
func TestOnlineKeepsUnobservedApartFromDown(t *testing.T) {
	tr, _ := on(t)
	if up, known := tr.Online("r-1"); up || known {
		t.Errorf("an untracked router reports (%v, %v), want (false, false)", up, known)
	}

	tr.SetThreshold("r-1", 30_000)
	if _, known := tr.Online("r-1"); known {
		t.Error("declaring a threshold counted as an observation; the router has " +
			"not been reached, and a badge reading this would say Offline")
	}

	tr.Connected("r-1", t0)
	if up, known := tr.Online("r-1"); !up || !known {
		t.Errorf("after a connect: (%v, %v), want (true, true)", up, known)
	}

	// DROPPED BUT NOT YET DECLARED: still online, which is the entire point of
	// the threshold.
	tr.Disconnected("r-1", t0+1000)
	if up, known := tr.Online("r-1"); !up || !known {
		t.Errorf("inside the debounce: (%v, %v), want (true, true) — the badge "+
			"moved before the threshold elapsed", up, known)
	}
	tr.TickAll(t0 + 40_000)
	if up, known := tr.Online("r-1"); up || !known {
		t.Errorf("after the debounce expired: (%v, %v), want (false, true)", up, known)
	}
}

// A NIL TRACKER IS INERT, which is what a test or a build without one holds.
func TestANilTrackerDoesNothing(t *testing.T) {
	var tr *Tracker
	tr.SetThreshold("r-1", 0)
	tr.Connected("r-1", t0)
	tr.Disconnected("r-1", t0+1000)
	tr.TickAll(t0 + minute)
	tr.Forget("r-1")
	if up, known := tr.Online("r-1"); up || known {
		t.Errorf("a nil tracker answered (%v, %v)", up, known)
	}
}

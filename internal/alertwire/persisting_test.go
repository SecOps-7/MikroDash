package alertwire

import (
	"strings"
	"testing"

	"mikrodash/internal/alert"
	"mikrodash/internal/collect"
)

// A router whose alerts notify but are never written down.
//
// ── SUPPRESSING THE WRITES ALONE WOULD BREAK ALERTING ──────────────────────
//
// The store IS the de-duplication memory. `emit` asks `HasOpen` to decide
// whether a persisting condition should ring again, and `Resolve` to decide
// whether a recovery fires at all. Simply not writing would give repeat
// down-notifications for ever and no recovery notifications — worse than either
// extreme. So a reporting-off router gets the same memory in RAM instead.

// cpuHigh and cpuNormal are one rule's two edges, which is all these need.
func cpuHigh() *collect.SystemPayload   { return &collect.SystemPayload{CPULoad: 99} }
func cpuNormal() *collect.SystemPayload { return &collect.SystemPayload{CPULoad: 5} }

func fired(t *testing.T, got []alert.Fired, why string) {
	t.Helper()
	if len(got) == 0 {
		t.Fatalf("%s: nothing fired", why)
	}
}

// TestAPersistingConditionNotifiesOnceNotEveryTick is the risk this design
// carries: get `HasOpen` wrong and every poll re-notifies for ever.
func TestAPersistingConditionNotifiesOnceNotEveryTick(t *testing.T) {
	w, h := wireOn(t)
	w.SetPersisting("r-1", false)

	fired(t, w.Evaluate(router, "system:update", cpuHigh()), "the first high CPU")

	// The condition persists. Ten more polls must ring nothing.
	for i := 0; i < 10; i++ {
		if got := w.Evaluate(router, "system:update", cpuHigh()); len(got) != 0 {
			t.Fatalf("poll %d re-notified: %v — a persisting condition would ring "+
				"on every tick, for ever", i+2, got)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("the database was touched for a reporting-off router: %v", h.calls)
	}
}

// TestRecoveryStillNotifies — the other half. Without a memory, `Resolve`
// returns nothing and the "up" alert never fires at all, so an operator is told
// about every outage and never told it ended.
func TestRecoveryStillNotifies(t *testing.T) {
	w, h := wireOn(t)
	w.SetPersisting("r-1", false)

	fired(t, w.Evaluate(router, "system:update", cpuHigh()), "high CPU")
	fired(t, w.Evaluate(router, "system:update", cpuNormal()), "the recovery")

	if len(h.calls) != 0 {
		t.Errorf("the database was touched: %v", h.calls)
	}
}

// TestNoRecoveryWithoutAnOutage — `Resolve` returning an id for something that
// was never open would invent a recovery for an outage that never happened.
func TestNoRecoveryWithoutAnOutage(t *testing.T) {
	w, _ := wireOn(t)
	w.SetPersisting("r-1", false)

	if got := w.Evaluate(router, "system:update", cpuNormal()); len(got) != 0 {
		t.Errorf("a recovery fired with no outage open: %v", got)
	}
}

// TestPersistingRoutersStillWriteRows — the other direction, or every test
// above passes against a wire that evaluates nothing at all.
func TestPersistingRoutersStillWriteRows(t *testing.T) {
	w, h := wireOn(t)
	w.SetPersisting("r-1", true)

	fired(t, w.Evaluate(router, "system:update", cpuHigh()), "high CPU")
	var inserted bool
	for _, c := range h.calls {
		if strings.HasPrefix(c, "insert ") {
			inserted = true
		}
	}
	if !inserted {
		t.Errorf("a persisting router wrote no row: %v", h.calls)
	}
}

// TestAnUndeclaredRouterPersists — the safe default. This is set from the fleet
// syncs, so a router seen before the first sync keeps writing rather than
// silently throwing its alert history away.
func TestAnUndeclaredRouterPersists(t *testing.T) {
	w, h := wireOn(t)
	fired(t, w.Evaluate(router, "system:update", cpuHigh()), "high CPU")
	if len(h.calls) == 0 {
		t.Error("an undeclared router wrote nothing; silence must not mean discard")
	}
}

// TestTogglingRebuildsTheEvaluator — the store IS the memory, so swapping
// stores must swap memories. An evaluator kept across the change would consult
// a store that has never seen what it opened.
func TestTogglingRebuildsTheEvaluator(t *testing.T) {
	w, _ := wireOn(t)
	w.SetPersisting("r-1", false)
	fired(t, w.Evaluate(router, "system:update", cpuHigh()), "high CPU")
	if w.Routers() != 1 {
		t.Fatalf("evaluators = %d", w.Routers())
	}

	w.SetPersisting("r-1", true)
	if w.Routers() != 0 {
		t.Error("the evaluator survived a change of store, so it is now consulting " +
			"a memory that has never seen the conditions it opened")
	}
}

// TestSettingTheSameValueIsANoOp — the fleet syncs call this on every pass, and
// resetting every router's edge state twice a second would make a persisting
// condition ring for ever by a different route.
func TestSettingTheSameValueIsANoOp(t *testing.T) {
	w, _ := wireOn(t)
	w.SetPersisting("r-1", false)
	fired(t, w.Evaluate(router, "system:update", cpuHigh()), "high CPU")

	for i := 0; i < 5; i++ {
		w.SetPersisting("r-1", false)
	}
	if w.Routers() != 1 {
		t.Fatal("an unchanged setting dropped the evaluator")
	}
	if got := w.Evaluate(router, "system:update", cpuHigh()); len(got) != 0 {
		t.Errorf("re-declaring the same setting re-notified: %v", got)
	}
}

// TestMemoryIsPerRouter — one router's open condition must not silence
// another's alert.
func TestMemoryIsPerRouter(t *testing.T) {
	w, _ := wireOn(t)
	w.SetPersisting("r-1", false)
	w.SetPersisting("r-2", false)

	fired(t, w.Evaluate(router, "system:update", cpuHigh()), "r-1 high CPU")
	other := alert.Router{ID: "r-2", AlertsEnabled: true}
	fired(t, w.Evaluate(other, "system:update", cpuHigh()), "r-2 high CPU")
}

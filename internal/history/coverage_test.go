package history

import (
	"reflect"
	"testing"
)

func set(ids ...string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// TestCoverageOpensAndClosesToMatchWhatIsObserved — a router newly observed
// opens a run, one no longer observed closes its run, one observed throughout
// is left alone. Sorted, so the writer's statements do not depend on map order.
func TestCoverageOpensAndClosesToMatchWhatIsObserved(t *testing.T) {
	ops := PlanCoverage(set("keep", "gone-b", "gone-a"), set("keep", "new-b", "new-a"), 1_000, 1_000)
	if !reflect.DeepEqual(ops.Open, []string{"new-a", "new-b"}) {
		t.Errorf("Open = %v, want the two newly observed routers, sorted", ops.Open)
	}
	if !reflect.DeepEqual(ops.Close, []string{"gone-a", "gone-b"}) {
		t.Errorf("Close = %v, want the two no longer observed, sorted", ops.Close)
	}
}

// TestTouchIsOncePerMinuteNotPerTick — the heartbeat is the whole cost of
// coverage, and the ticker runs every second. Touching per tick is sixty
// writes a minute where one will do, and it is the mutation this pins.
func TestTouchIsOncePerMinuteNotPerTick(t *testing.T) {
	open, covered := set("r"), set("r")
	if PlanCoverage(open, covered, 59_999, 0).Touch {
		t.Error("touched before a minute had passed; at one tick a second that is " +
			"sixty writes a minute instead of one")
	}
	if !PlanCoverage(open, covered, 60_000, 0).Touch {
		t.Error("no touch after a full minute: a crash would lose everything since the run opened")
	}
}

// TestNoTouchWithNothingStayingOpen — a closing run is touched by its close and
// an opening one starts current, so with nothing staying open the heartbeat has
// nothing to do, however long it has been.
func TestNoTouchWithNothingStayingOpen(t *testing.T) {
	if ops := PlanCoverage(set("closing"), set("opening"), 10*CoverageTouchMs, 0); ops.Touch {
		t.Errorf("touched with no run staying open: %+v", ops)
	}
}

// TestNoOpsForAnUnchangedFleetWithinTheMinute — the common tick: every router
// observed and already open, heartbeat not due. It must decide nothing, or the
// one-second ticker becomes a write a second.
func TestNoOpsForAnUnchangedFleetWithinTheMinute(t *testing.T) {
	ops := PlanCoverage(set("a", "b"), set("a", "b"), 30_000, 0)
	if len(ops.Open) != 0 || len(ops.Close) != 0 || ops.Touch {
		t.Errorf("an unchanged fleet inside the minute produced %+v", ops)
	}
}

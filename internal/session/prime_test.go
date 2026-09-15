package session

import (
	"strings"
	"testing"
)

// TestEveryTargetCanBeRefreshed is the gate that makes phase 5.2 real.
//
// ── THE DEFECT IT REPLACES ──────────────────────────────────────────────────
//
// `probe` asked `if r, ok := any(t).(refresher); ok { r.RefreshNow() }`, where
// `t` is a `collectorTarget` — a struct with no methods. The assertion was
// ALWAYS false, so no dormancy probe has ever refreshed anything: the collector
// was resumed and then waited a full cadence for its answer, which on `wifi`'s
// 300-second subscription is five minutes of a page saying nothing.
//
// Nothing failed, because a resumed collector does eventually report. That is
// the shape this repository keeps losing coverage to, so the fix comes with a
// check — and the check reads the SOURCE rather than a live Session, because
// building one needs a router.
//
// It fails in both directions: a target with no refresh fails, and a refresh
// wired to a method that does not exist fails to compile.
func TestEveryTargetCanBeRefreshed(t *testing.T) {
	src := readSource(t, "dormancy_targets.go")

	// One `add(` per target, and each must carry a fourth closure.
	got := strings.Count(src, "add(\"")
	if got != len(targetKeys) {
		t.Fatalf("%d add() calls for %d target keys — targets() and targetKeys have "+
			"drifted, and this check is reading the wrong thing", got, len(targetKeys))
	}
	// ── A NIL REFRESH IS ALLOWED, AND ONLY WHERE `noPrimePath` SAYS WHY ─────
	//
	// This required all 24, which was true while `targetKeys` and the prime list
	// were the same thing. They separated in 3.4: `logs` and `ping` are set B
	// acquisitions with no "take one reading" to ask for, and until then they
	// were kept OUT of the table entirely to avoid this rule — which also meant
	// `applyDemand` could not gate them, so `logs` held a channel per router for
	// a page nobody had open.
	//
	// The property is unchanged and the exceptions are now named rather than
	// avoided. `TestEveryPrimeTargetCanActuallyRefresh` fails a nil that is NOT
	// recorded, and a recording for a key the table does not hold.
	// A table collector's refresh is a closure, `func() { s.x.RefreshNow() }`: its
	// method is promoted from the embedded core, and a promoted method value
	// dereferences a collector the session did not build (dormancy_targets.go).
	refreshers := strings.Count(src, ".RefreshNow)") + strings.Count(src, ".Tick)") +
		strings.Count(src, ".RefreshNow() })")
	if want := len(targetKeys) - len(noPrimePath); refreshers != want {
		t.Errorf("%d of %d targets have a refresh closure, expected %d (%d recorded in "+
			"noPrimePath). A target without one is skipped by primeAll and by the "+
			"dormancy probe, so its page waits a full cadence on first landing — "+
			"silently, because a resumed collector does eventually report.",
			refreshers, len(targetKeys), want, len(noPrimePath))
	}
}

// TestProbeNoLongerUsesADeadTypeAssertion. The bug was invisible precisely
// because the code LOOKED right, so the shape it must not return to is named.
func TestProbeNoLongerUsesADeadTypeAssertion(t *testing.T) {
	src := readSource(t, "dormancy_run.go")
	if strings.Contains(src, "any(t).(refresher)") {
		t.Error("probe is asserting `refresher` on collectorTarget again. That struct " +
			"has no methods, so the assertion is always false and the refresh half of " +
			"every probe silently does nothing. Use collectorTarget.refresh.")
	}
	if !strings.Contains(src, "if t.refresh != nil") {
		t.Error("probe no longer calls t.refresh, so a dormant collector is resumed and " +
			"then waits a full cadence for its answer")
	}
}

// TestPrimeSkipsWhatAlreadyReported is the property that makes primeAll safe to
// call from anywhere: it is not a poll, it is a floor.
func TestPrimeSkipsWhatAlreadyReported(t *testing.T) {
	src := readSource(t, "dormancy_run.go")
	for _, want := range []string{"if t.last() != nil", "if !s.CollectorEnabled(key)"} {
		if !strings.Contains(src, want) {
			t.Errorf("primeAll no longer guards on %q. Without it the pass re-reads "+
				"collectors that already have data, and reads ones the operator turned "+
				"off for this router.", want)
		}
	}
}

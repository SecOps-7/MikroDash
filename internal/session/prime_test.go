package session

import (
	"strings"
	"testing"
	"time"

	"mikrodash/internal/collect"
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
	// `.Prime() })` is the areas collector's: its refresh takes no key and reads
	// every area that has no payload yet, which is what "take one reading" means
	// for one collector serving many pages. Counted here rather than given a
	// RefreshNow() alias, because an alias would be a second name for one thing.
	refreshers := strings.Count(src, ".RefreshNow)") + strings.Count(src, ".Tick)") +
		strings.Count(src, ".RefreshNow() })") + strings.Count(src, ".Prime() })")
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

// TestAReconnectInvalidatesThePrimedReading.
//
// `primedSys` is the one `/system/resource` reading a warm session takes, and
// `PrimeUnread` skips every session that already holds one. It was never
// cleared, so a router that rebooted and reconnected kept its PRE-REBOOT uptime
// on the Devices page for as long as the session lived, and its pre-upgrade
// RouterOS version with it - the prime is also what reports a warm session's
// identity.
//
// Driven through `adoptConnection` rather than a dial: it is the ONLY place in
// this package a session becomes connected (grep `connected = true`), so a test
// of it covers every connect, first or re-.
func TestAReconnectInvalidatesThePrimedReading(t *testing.T) {
	m := graceManager(t, time.Hour)
	s, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	stale := &collect.SystemPayload{UptimeRaw: "3w2d"}
	s.mu.Lock()
	s.primedSys = stale
	s.mu.Unlock()

	if !s.adoptConnection(nil) {
		t.Fatal("a live session refused a new connection")
	}
	if got := s.primedSystem(); got != nil {
		t.Errorf("the primed reading survived a reconnect (uptime %q): a rebooted "+
			"router keeps its old uptime and version on the Devices page", got.UptimeRaw)
	}
	if !s.Connected() {
		t.Error("adoptConnection did not mark the session connected")
	}

	// THE CONTROL. A session released while its dial was in flight must refuse
	// the client and change NOTHING - otherwise the assertion above would pass
	// for a method that clears the field on every call whatever it decides.
	s.mu.Lock()
	s.closed = true
	s.connected = false
	s.primedSys = stale
	s.mu.Unlock()
	if s.adoptConnection(nil) {
		t.Error("a closed session adopted a connection")
	}
	if s.primedSystem() != stale || s.Connected() {
		t.Error("a refused connection still changed the session's state")
	}
}

// TestSystemOrPrimedFallsBackToThePrimedReading — the one thing the deleted
// `Manager.Snapshots` did that a plain read does not: hand out the primed
// reading when the session's own system collector has none. A warm session runs
// no collector until the Devices page's hold starts one, and this is what fills
// the card in between.
func TestSystemOrPrimedFallsBackToThePrimedReading(t *testing.T) {
	m := graceManager(t, time.Hour)
	s, err := m.Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	// THE CONTROL FIRST: nothing primed and nothing collected is nil, not a
	// zeroed payload that would draw 0% gauges and an empty uptime as if read.
	if got := s.SystemOrPrimed(); got != nil {
		t.Fatalf("a session with no reading at all returned %+v", got)
	}

	primed := &collect.SystemPayload{CPULoad: 42}
	s.mu.Lock()
	s.primedSys = primed
	s.mu.Unlock()
	if got := s.SystemOrPrimed(); got != primed {
		t.Errorf("SystemOrPrimed = %+v, want the primed reading; a warm router's card "+
			"is a green badge over blank gauges", got)
	}
}

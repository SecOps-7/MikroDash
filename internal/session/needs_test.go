package session

import (
	"strings"
	"testing"
	"time"
)

// The decision that keeps 4.3 from being a regression.
//
// Replacing the background pools with sessions is a LOSS unless a session held
// for alerting runs only what alerting needs: the pool costs 119-120 commands a
// minute for one unwatched router, and a session's connect block starts fifteen
// collectors against the pool's seven.
func TestNeedsGivesAnAlertingRouterOnlyTheAlertFeed(t *testing.T) {
	why := Reasons{Alerts: true}

	for _, k := range AlertFeeds {
		if !Needs(k, why) {
			t.Errorf("%s feeds an alert rule and would not run on a router held for "+
				"alerting — the rule then never fires, silently", k)
		}
	}
	// The nine that make the naive version a regression.
	for _, k := range []string{"bridges", "dhcpLeases", "dhcpNetworks", "dns",
		"firewall", "logs", "talkers", "vlans", "wan"} {
		if Needs(k, why) {
			t.Errorf("%s would run on a router held only for alerting. Nobody is "+
				"watching it and no rule reads it; `wan` alone polls every two seconds.", k)
		}
	}
}

// TestAViewerWantsEverything, because any page can be navigated to and the page
// gates decide the rest. This is existing behaviour and the merge must not
// narrow it.
func TestAViewerWantsEverything(t *testing.T) {
	why := Reasons{Viewer: true}
	for _, k := range []string{"bridges", "wan", "vlans", "queues", "capsman", "logs"} {
		if !Needs(k, why) {
			t.Errorf("a viewer would not get %s, so navigating to its page shows nothing", k)
		}
	}
}

// TestReasonsCombine: a router can be watched AND alerting AND recorded, and the
// answer is the union. A miss here starves whichever consumer was not counted.
func TestReasonsCombine(t *testing.T) {
	if !Needs("traffic", Reasons{History: true}) {
		t.Error("history does not get traffic, so traffic_samples stops being written")
	}
	if !Needs("ping", Reasons{History: true}) {
		t.Error("history does not get ping, so ping_samples stops being written")
	}
	if !Needs("vpn", Reasons{Alerts: true, History: true}) {
		t.Error("the union of two reasons lost a collector one of them needs")
	}
	if Needs("wan", Reasons{Alerts: true, History: true, Devices: true}) {
		t.Error("wan runs for a router with no viewer; none of those three reads it")
	}
}

// TestNothingRunsForNoReason. Without this the check above passes against a
// Needs that simply returns true.
func TestNothingRunsForNoReason(t *testing.T) {
	for _, k := range []string{"system", "ping", "traffic", "vpn", "wan"} {
		if Needs(k, Reasons{}) {
			t.Errorf("%s runs for a session nobody holds and nobody views", k)
		}
	}
}

// TestApplyReasonsLeavesAViewerAlone.
//
// `Needs` says a viewer wants everything, which is about what is ALLOWED to run,
// not what should be running now. Page gating decides that, and it is the reason
// an idle browser does not poll twenty-two collectors. Resuming everything
// because a viewer exists would undo all of it — so the prescriptive case is the
// viewerless one, and this pins that.
func TestApplyReasonsLeavesAViewerAlone(t *testing.T) {
	src := readSource(t, "needs.go")
	if !contains(src, "if why.Viewer { return }") {
		t.Error("applyDemand no longer returns early for a viewer, so it would resume " +
			"every collector for any browser and undo page gating entirely")
	}
	if !contains(src, "if s == nil || !s.Connected() { return }") {
		t.Error("applyDemand no longer guards on Connected. A hold taken while the " +
			"session is dialling then reaches collectors that do not exist yet.")
	}
	if !contains(src, "s.ResumeCollector(key)") {
		t.Error("applyDemand resumes a collector without the funnel, so a collector the " +
			"operator disabled for this router comes back because alerting wants it")
	}
}

// TestEveryTransitionConverges: the order a hold and a connect arrive in is
// racy, so every one of them must re-apply. A missing call leaves a session
// running the wrong set until something else happens to touch it.
func TestEveryTransitionConverges(t *testing.T) {
	src := readSource(t, "session.go")
	for _, where := range []string{
		// The Retain path. NOT "hold then applyDemand" -- that adjacency is what
		// the first version of this test asserted, and it was pinning the BUG:
		// applyDemand no-ops while a viewer is present, and Acquire's reference
		// is still held at that point. The correct shape is release, THEN apply,
		// and TestRetainPrunesAfterGivingBackItsViewerReference checks the order
		// directly. Here it is enough that the Retain path applies at all.
		"m.Release(routerID)",
		// The Drop path. Checked as the whole sequence, because `delete(s.holds,
		// reason)` alone is trivially present and a mutation removing the
		// applyDemand after it SURVIVED the first version of this test.
		"delete(s.holds, reason) empty := len(s.holds) == 0 && s.refs <= 0 s.mu.Unlock() s.applyDemand()",
		// The connect path. NOT deferred -- see TestTheConnectPruneIsNotDeferred.
		"s.applyDemand() first = false",
	} {
		if !contains(src, where) {
			t.Errorf("a holder transition no longer calls applyDemand (%q). The session "+
				"then runs whatever the last transition left, which for an alerting "+
				"router is fifteen collectors instead of six.", where)
		}
	}
}

// TestRetainPrunesAfterGivingBackItsViewerReference.
//
// ── THE BUG THIS PINS, WHICH EVERY TEST MISSED ──────────────────────────────
//
// `Retain` acquires (taking a VIEWER reference), records the hold, and releases.
// `applyDemand` does nothing while a viewer is present — so calling it between
// the hold and the release saw refs == 1, concluded a viewer wanted everything,
// and returned. The session then ran all fifteen collectors for a router nobody
// was watching.
//
// Every test was green. The tests ask what the code DECIDES; none of them could
// see how much the router was being asked. It was found by measuring: 119-120
// commands a minute became 263-311.
func TestRetainPrunesAfterGivingBackItsViewerReference(t *testing.T) {
	src := readSource(t, "session.go")
	rel := indexOf(src, "m.Release(routerID) ")
	app := indexOf(src, "s.applyDemand() return s, nil")
	if rel < 0 || app < 0 {
		t.Fatal("Retain no longer releases then applies; this check is reading the wrong " +
			"shape and would pass against anything")
	}
	if app < rel {
		t.Error("Retain calls applyDemand BEFORE giving back its viewer reference. " +
			"applyDemand no-ops while a viewer is present, so the prune never happens " +
			"and a router held for alerting runs every collector.")
	}
}

// TestTheIdleGatePrunesAtTheEndOfTheGrace, not the moment a viewer leaves.
//
// ── WHY THE TIMING IS THE WHOLE POINT ───────────────────────────────────────
//
// The first version pruned in `Release`. That is wrong, and the operator said so
// on 2026-09-09: a viewer leaving starts the GRACE, it does not mean nobody is
// coming back. A page refresh, a router switch and a closed tab are
// indistinguishable from there, and the first two return within seconds.
//
// Pruning immediately suspended every collector the moment a browser blinked,
// and the viewer came back to a page waiting for data — the exact churn the
// two-minute grace exists to prevent, moved from the connection to the
// collectors.
//
// So the prune waits out the grace alongside the teardown: two minutes with
// nobody watching, and then a held session drops to what alerting and history
// need while keeping its connection, because alerts cannot be evaluated without
// one.
func TestTheIdleGatePrunesAtTheEndOfTheGrace(t *testing.T) {
	src := readSource(t, "session.go")

	// Release must NOT prune: it only arms the timer.
	rel := src[indexOf(src, "func (m *Manager) Release(routerID string) {"):]
	rel = rel[:min(len(rel), 900)]
	if contains(rel, "s.applyDemand()") {
		t.Error("Release prunes when the last viewer leaves. A refresh or a router " +
			"switch then suspends every collector for the two minutes the grace was " +
			"meant to cover, and the viewer returns to a page waiting for data.")
	}
	// idleOut must, and only when no viewer came back.
	if !contains(src, "viewer := s.refs > 0") || !contains(src, "if !viewer { s.applyDemand() }") {
		t.Error("idleOut no longer prunes a held session at the end of the grace, so a " +
			"router held for alerting keeps the full viewer collector set indefinitely")
	}
	// And the grace is the operator's two minutes.
	if DefaultIdleGrace != 2*time.Minute {
		t.Errorf("the idle grace is %v; the operator asked for two minutes with nobody "+
			"watching before the collectors go quiet", DefaultIdleGrace)
	}
}

// TestTheConnectPruneIsNotDeferred.
//
// It was written as `defer s.applyDemand()`. A defer runs when the FUNCTION
// returns, and the function is `connectLoop` — a loop that runs for the life of
// the session and never returns. So the prune never happened, and a router held
// only for alerting kept all fifteen collectors.
//
// Every test stayed green, because they assert the call EXISTS and it did. It
// took a command-rate measurement to find: 264-287 a minute against a 119-120
// baseline. This is the cheap version of that measurement.
func TestTheConnectPruneIsNotDeferred(t *testing.T) {
	src := readSource(t, "session.go")
	if contains(src, "defer s.applyDemand()") {
		t.Error("the connect path defers applyDemand. connectLoop never returns, so a " +
			"deferred call never runs and a held session keeps every collector.")
	}
	if !contains(src, "s.replayResumes() // ── PHASE 4.3c") && !contains(src, "s.applyDemand() first = false") {
		t.Error("the connect path no longer prunes at the end of the start block, so what " +
			"a held session runs depends on whatever touched it last")
	}
}

// TestResumeCollectorRefusesWhatTheSessionHasNoReasonToRun.
//
// The connect-time prune is not enough on its own. The dormancy probe calls
// `ResumeCollector` for any collector due for a probe, and it has no way to know
// why the session exists — so after the prune it talked `queues` back into
// running on a router nobody was watching.
//
// Found by measurement, not by reading: the rate settled at 147-167 a minute
// against a 119-120 baseline, and the busiest-menu list named `/queue/simple`
// and `/queue/tree`. The funnel is where the veto belongs, beside the enabled
// check, because every resume in the app goes through it.
func TestResumeCollectorRefusesWhatTheSessionHasNoReasonToRun(t *testing.T) {
	src := readSource(t, "dormancy_targets.go")
	// RE-AIMED 2026-09-12: the rule moved into `mayRun`, which the dormancy probe
	// now asks too, so it is stated once rather than copied beside the probe.
	if !contains(src, "if !s.mayRun(key) { return }") ||
		!contains(src, "why := s.reasonsLocked()") || !contains(src, "return Needs(key, why)") ||
		!contains(src, "if !s.CollectorEnabled(key) { return false }") {
		t.Error("ResumeCollector no longer refuses a collector the session has no reason " +
			"to run (via mayRun), so a resume talks a held session back into running what " +
			"the prune stopped")
	}
	// Beside the enabled check, not after the work: a refusal that happens later
	// has already started something.
	if indexOf(src, "if !s.mayRun(key) { return }") > indexOf(src, "if !s.Connected() {") {
		t.Error("the Needs veto is after the not-connected latch, so a refused resume is " +
			"still remembered and replayed when the link comes up")
	}
}

// TestBothConnectPathsPrune.
//
// ── THE BUG, AND WHY IT SURVIVED EVERY OTHER CHECK ──────────────────────────
//
// The connect loop has two branches: `if first` builds and starts the
// collectors, and the `else` restarts them after a reconnect. The 4.3c prune was
// added to the first and not the second, so a session held only for alerting
// came back from any blip running all fifteen collectors.
//
// Nothing caught it. The unit tests assert the prune exists and it did; the live
// measurement was taken at 129 commands a minute and the reconnect had not
// happened yet. The router dropped for five seconds forty minutes later and the
// rate went to ~300 and stayed there — found only because the loop kept
// watching after the work looked finished.
//
// This is the same shape as `TestBothTeardownPathsStopEveryCollector`, which
// exists because the mirror-image mistake was made on the way down.
func TestBothConnectPathsPrune(t *testing.T) {
	src := readSource(t, "session.go")
	if n := strings.Count(src, "s.applyDemand()"); n < 4 {
		t.Errorf("session.go calls applyDemand %d times; expected at least four — the "+
			"first connect, the reconnect, Retain and Drop. A branch without it leaves a "+
			"held session running the viewer's collector set until something else "+
			"happens to touch it.", n)
	}
	// The reconnect branch specifically: it restarts every collector, so it is
	// the one where a missing prune is invisible AND expensive.
	recon := src[indexOf(src, "if s.eff.Enabled[\"conns\"] { s.conns.Reconnected() }"):]
	if !contains(recon[:min(len(recon), 200)], "s.applyDemand()") {
		t.Error("the reconnect branch no longer prunes. A held session comes back from " +
			"any blip running every collector, which is what took this install from 129 " +
			"commands a minute to 300.")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// indexOf is strings.Index over the flattened, comment-stripped source that
// `readSource` returns.
func indexOf(src, want string) int {
	return strings.Index(src, strings.Join(strings.Fields(want), " "))
}

// TestAWokenCollectorIsAlsoResumed.
//
// `ResumeCollector` used to return straight after `WakeForFocus`, relying on the
// dormancy probe's second pass through the funnel to do the resume. The probe no
// longer resumes — it reads — so an early return here would wake a collector a
// page has just asked for and leave it stopped.
func TestAWokenCollectorIsAlsoResumed(t *testing.T) {
	src := readSource(t, "dormancy_targets.go")
	if !contains(src, "s.WakeForFocus(key)") {
		t.Fatal("ResumeCollector no longer wakes a dormant collector at all — this check reads nothing")
	}
	if contains(src, "s.WakeForFocus(key) return") {
		t.Error("ResumeCollector returns after waking a dormant collector, so a page focus " +
			"wakes it and never resumes it: the probe no longer does that half")
	}
}

// TestWantsAsksTheHoldsWhenNobodyIsWatching.
//
// ── A SURVIVING MUTATION IS WHY THIS EXISTS ────────────────────────────────
//
// Phase 6.3 moved the demand rule here, and the room half was driven by
// `TestWantsCollectorReadsTheRooms` next door. The HOLD half was not: deleting
// the `Needs(key, why)` branch entirely left both packages green, because the
// only thing asserting it was a source check looking for the words.
//
// That is the branch that keeps an unwatched router evaluating alerts. Without
// it a session held for alerting runs nothing at all, and the rules go quiet with
// nothing anywhere saying so — which is the exact failure `AlertFeeds` exists to
// prevent, one layer down.
func TestWantsAsksTheHoldsWhenNobodyIsWatching(t *testing.T) {
	s := NewForTest(nil, "r1") // no hub: no room can be occupied
	for _, key := range AlertFeeds {
		if s.Wants(key) {
			t.Fatalf("%s is wanted by a session with no viewer and no holds", key)
		}
	}

	s.mu.Lock()
	s.holds = map[string]bool{"alerts": true}
	s.mu.Unlock()

	for _, key := range AlertFeeds {
		if !s.Wants(key) {
			t.Errorf("%s is an alert feed and a session held for alerting does not want "+
				"it. The rules go quiet on every unwatched router, and nothing says so.",
				key)
		}
	}
	// And a hold must not want everything: that would put the whole viewer set
	// back on a router nobody is looking at, which is what 4.3 removed.
	if s.Wants("queues") {
		t.Error("a session held for alerting wants `queues`, which no alert rule reads")
	}
}

// TestWantsConsultsTheHoldsEvenWithAViewer.
//
// ── THE REGRESSION THIS PINS SHIPPED FOR ONE COMMIT ────────────────────────
//
// `Needs` returns true for EVERYTHING while a viewer is present, because it
// answers "what is this session allowed to run". Phase 6.3's first version
// handled that with `Needs(key, why) && !why.Viewer`, which does not ask the
// holds question without the viewer term — it SKIPS the question entirely
// whenever a viewer exists.
//
// So on the router you are looking at, the alert and history feeds were gated
// purely on rooms. Opening any page that does not declare `ifStatus` suspended
// it, and with it four of the six alert rules, on the one router most likely to
// be watched. Found by a Devices-page bug: no WAN RX/TX.
func TestWantsConsultsTheHoldsEvenWithAViewer(t *testing.T) {
	s := NewForTest(nil, "r1") // no hub: no room can be occupied
	s.mu.Lock()
	s.holds = map[string]bool{"alerts": true}
	s.refs = 1 // a browser has this router selected
	s.mu.Unlock()

	for _, key := range AlertFeeds {
		if !s.Wants(key) {
			t.Errorf("%s is an alert feed and the session does not want it while a "+
				"viewer is present. The rules go quiet on the router somebody is "+
				"actually looking at, which is the last place anyone would look for it.",
				key)
		}
	}
	// AND A VIEWER STILL DOES NOT WANT EVERYTHING, or page gating is undone:
	// `queues` is fed by no hold and no occupied room.
	if s.Wants("queues") {
		t.Error("a viewer makes `queues` wanted with nobody on its page; `Needs` " +
			"returning true for everything under a viewer has leaked into the rule")
	}
}

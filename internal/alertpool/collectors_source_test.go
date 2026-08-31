package alertpool

import (
	"mikrodash/internal/collection"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Everything built must be stopped.
//
// ── WHY THIS IS A SOURCE PIN AND NOT A BEHAVIOURAL ONE ────────────────────
//
// A behavioural test cannot see it. `reader.Connected()` gates every poll, so a
// collector left running after a drop emits NOTHING — the events look identical
// either way, and a mutant deleting two Stop calls survived a test that watched
// for events.
//
// What is actually lost is timers. `collect.pollLoop` is a self-rescheduling
// `time.Timer` that arms the next tick from inside the current one and never
// consults the session, so a collector that is never stopped keeps waking for
// the life of the process. That is exactly the defect found in
// `session.Manager.Release` on 2026-08-29 — five of fourteen stopped, nine
// timers alive per released session — and it was invisible for the same reason:
// the reads fail quietly and nobody is listening.
//
// So the invariant is checked the same way it is there: count both lists out of
// the source and require they agree.
func TestEveryCollectorBuiltIsAlsoStopped(t *testing.T) {
	b, err := os.ReadFile("collectors.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)

	built := fieldsIn(t, src, "func buildCollectors(", `s\.(\w+) = collect\.New`)
	stopped := fieldsIn(t, src, "func (s *poolSession) stopCollectors(", `s\.(\w+)\.Stop\(\)`)

	if len(built) == 0 || len(stopped) == 0 {
		t.Fatalf("measured nothing: %d built, %d stopped — the anchors have moved",
			len(built), len(stopped))
	}

	have := map[string]bool{}
	for _, f := range stopped {
		have[f] = true
	}
	var leaked []string
	for _, f := range built {
		if !have[f] {
			leaked = append(leaked, f)
		}
	}
	if len(leaked) > 0 {
		t.Errorf("%d collector(s) are built and never stopped: %v\n"+
			"Each keeps a self-rescheduling timer alive for the life of the process once its "+
			"session goes. No event is emitted — `reader.Connected()` gates the poll — which is "+
			"precisely why this cannot be caught by watching events.\nbuilt=%v stopped=%v",
			len(leaked), leaked, built, stopped)
	}
}

// AND THE ALERT SET IS THE LIVE POOL'S SIX. A seventh would be load per router
// for a rule that does not read it; a sixth missing means an alert family that
// fires for the watched router and silently never for any other — which is the
// defect this whole package exists to close.
//
// ── `traffic` IS THE ONE EXCEPTION, AND IT IS NOT AN ALERT COLLECTOR ──────
//
// Added 2026-08-30 for continuous history. It is built ONLY when this session is
// the one recording — `hist`, which is true for at most one router in the fleet
// — and no alert rule reads it. Counting it among the six would say this pool
// runs seven collectors per router, which is exactly the load claim the test
// exists to protect.
//
// So it is excluded by name and its per-router-ness is asserted separately, in
// `TestTrafficIsBuiltOnlyForTheHistoryRouter`. If a future change starts
// building it unconditionally, that test fails rather than this one passing
// quietly with a widened list.
func TestThePoolRunsTheLivePoolsSixCollectors(t *testing.T) {
	b, err := os.ReadFile("collectors.go")
	if err != nil {
		t.Fatal(err)
	}
	all := fieldsIn(t, string(b), "func buildCollectors(", `s\.(\w+) = collect\.New`)
	got := make([]string, 0, len(all))
	for _, f := range all {
		if f == "traffic" {
			continue
		}
		got = append(got, f)
	}
	want := []string{"ifStatus", "netwatch", "ping", "routing", "system", "vpn"}
	if len(got) != len(want) {
		t.Fatalf("the pool builds %v; the live pool runs %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("the pool builds %v; the live pool runs %v", got, want)
			return
		}
	}
}

// ── AND ROUTING IS ASKED FOR IN ITS NARROW MODE ───────────────────────────
//
// `collect.Routing` has the mode and `internal/collect` tests it; nothing tested
// that THIS pool asks for it. A mutant deleting `.BGPOnly()` here passed
// everything — the same "test the call site, not the callee" shape that has now
// come up four times in this port.
//
// The cost of losing it is quiet: two extra reads (`/ip/route/print`,
// `/ipv6/route/print`) per routing tick per alert-enabled router, for a payload
// no page renders, on hardware whose documented limit is concurrent API
// channels. Nothing breaks; the fleet just gets slower.
func TestThePoolAsksRoutingForBGPOnly(t *testing.T) {
	b, err := os.ReadFile("collectors.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := indexOf(src, "func buildCollectors(")
	if i < 0 {
		t.Fatal("buildCollectors is gone — this test is measuring nothing")
	}
	body := src[i:]
	if j := indexOf(body, "\n}"); j >= 0 {
		body = body[:j]
	}
	if !regexp.MustCompile(`collect\.NewRouting\([^)]*\)\.BGPOnly\(\)`).MatchString(body) {
		t.Error("the pool builds Routing without .BGPOnly(): every alert tick then also reads " +
			"/ip/route/print and /ipv6/route/print, per alert-enabled router, for a payload no " +
			"page renders")
	}
}

func fieldsIn(t *testing.T, src, anchor, pat string) []string {
	t.Helper()
	i := indexOf(src, anchor)
	if i < 0 {
		t.Fatalf("anchor %q not found — this test is measuring nothing", anchor)
	}
	rest := src[i:]
	if j := indexOf(rest, "\n}"); j >= 0 {
		rest = rest[:j]
	}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(pat).FindAllStringSubmatch(rest, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TRAFFIC IS BUILT FOR ONE ROUTER, NOT THE FLEET.
//
// Continuous history costs one command channel on one router. The failure this
// guards is the easy one: dropping the `hist` guard so every pooled router opens
// a traffic stream, which multiplies the cost by the size of the fleet and
// breaks CLAUDE.md's rule that efficiency means FEWER router channels.
//
// Read out of the SOURCE because the guard is a build-time decision — by the
// time a test can observe collectors, the choice has already been made.
func TestTrafficIsBuiltOnlyForTheHistoryRouter(t *testing.T) {
	b, err := os.ReadFile("collectors.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "s.traffic = collect.NewTraffic")
	if i < 0 {
		t.Fatal("the traffic collector is gone; continuous history has no source")
	}
	// The nearest enclosing `if` before it must be the history guard.
	before := src[:i]
	j := strings.LastIndex(before, "\n\tif ")
	if j < 0 {
		t.Fatal("the traffic collector is not inside a guard at function level")
	}
	guard := strings.SplitN(before[j+1:], "{", 2)[0]
	if !strings.Contains(guard, "hist") {
		t.Errorf("the traffic collector's guard is %q, which does not test `hist`. "+
			"Every pooled router would then open a traffic stream.", strings.TrimSpace(guard))
	}
}

// THE RECORDED ROUTER FOLLOWS AN ACTIVATION, and both ends are rebuilt.
//
// The pair is built inside `buildCollectors`, which runs once per session, so a
// session built history-off has no traffic collector to switch on later.
// `SetHistoryRouter` therefore reports that it changed and marks BOTH the old
// and the new router for rebuild; `Sync` does the work through the single
// construction path.
//
// Marking only the new one would leave the previous session recording a router
// the operator has stopped looking at — two histories at once, which reads as
// duplicate rows rather than as an error.
func TestSwitchingTheHistoryRouterRebuildsBothEnds(t *testing.T) {
	p := &Pool{}
	if p.SetHistoryRouter("a") != true {
		t.Fatal("the first call reported no change")
	}
	if got := p.HistoryRouter(); got != "a" {
		t.Fatalf("HistoryRouter() = %q, want a", got)
	}
	// Only the new one on the first call — there is no old one.
	if len(p.pendingRebuild) != 1 || !p.pendingRebuild["a"] {
		t.Fatalf("pendingRebuild = %v, want just a", p.pendingRebuild)
	}
	p.pendingRebuild = map[string]bool{}

	if p.SetHistoryRouter("b") != true {
		t.Fatal("switching reported no change")
	}
	if !p.pendingRebuild["a"] {
		t.Error("the OLD router was not marked for rebuild; it would keep recording")
	}
	if !p.pendingRebuild["b"] {
		t.Error("the NEW router was not marked for rebuild; it would never start")
	}

	// IDEMPOTENT. Re-naming the same router must not churn sessions: `Sync` runs
	// on every routers change, and a rebuild drops and re-dials a connection.
	p.pendingRebuild = map[string]bool{}
	if p.SetHistoryRouter("b") != false {
		t.Error("re-naming the same router reported a change")
	}
	if len(p.pendingRebuild) != 0 {
		t.Errorf("re-naming the same router marked %v for rebuild", p.pendingRebuild)
	}
}

// A HISTORY-ONLY SESSION IS BUILT FOR A ROUTER WITH ALERTS OFF.
//
// `buildCollectors` returned early on `!AlertsEnabled`, which is right for
// alerting and wrong for history: the ACTIVE router is whichever one the
// operator selected and nothing says it has alerts on. Two of this fleet's three
// have alerts off today; that the active one has them on is luck.
//
// The session must then carry ping and traffic and NOT the four alert-only
// collectors, and must not feed the evaluator.
func TestAHistoryOnlySessionCarriesThePairAndNoAlertCollectors(t *testing.T) {
	s := &poolSession{r: Router{ID: "a", PingTarget: "198.51.100.254", DefaultIf: "ether1"}}
	fired := 0
	buildCollectors(s, collection.Resolved{Poll: map[string]int{}, Enabled: map[string]bool{}},
		func(Router, string, any) { fired++ }, func(string, string, any) {}, true)

	if s.traffic == nil || s.ping == nil {
		t.Fatal("a history-only session is missing the pair")
	}
	if s.system != nil || s.vpn != nil || s.netwatch != nil || s.ifStatus != nil {
		t.Error("a router with alerts OFF was given alert collectors")
	}
	if fired != 0 {
		t.Errorf("a history-only session fed the alert evaluator %d time(s)", fired)
	}
}

// WHERE A SESSION'S PAYLOADS GO — the four combinations, stated.
//
// The one with teeth is alerts-OFF + history-ON: that session exists only
// because the router is the one being recorded, and if its payloads reached the
// evaluator, enabling continuous history would silently turn alerting back on
// for a router the operator switched it off for. A notification nobody asked for
// is worse than a missing one.
func TestEmitTargets(t *testing.T) {
	for _, c := range []struct {
		alerts, hist   bool
		wantEv, wantHi bool
	}{
		{true, false, true, false},   // ordinary alert session
		{true, true, true, true},     // the active router, alerting too
		{false, true, false, true},   // history-only: MUST NOT alert
		{false, false, false, false}, // status-only; not built at all
	} {
		ev, hi := emitTargets(c.alerts, c.hist)
		if ev != c.wantEv || hi != c.wantHi {
			t.Errorf("emitTargets(alerts=%v, hist=%v) = (%v, %v), want (%v, %v)",
				c.alerts, c.hist, ev, hi, c.wantEv, c.wantHi)
		}
	}
}

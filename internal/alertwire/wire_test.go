package alertwire

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/alert"
	"mikrodash/internal/collect"
)

// A recording History. Every call is kept in order, so the tests assert what
// WOULD have been written rather than reading it back out of a database.
type fakeHist struct {
	open  map[string]bool
	calls []string
	nowOf []int64
	next  int64
}

func newHist() *fakeHist { return &fakeHist{open: map[string]bool{}} }

func key(r, t, s string) string { return r + "|" + t + "|" + s }

// stored turns a DISPLAY alert type into the one a row actually carries.
//
// `alert.Fired.AlertType` is the display form and `storedType` derives what goes
// in the column. The display forms are NOT the short names — the CPU rule fires
// `"High CPU"`, which stores as `high_cpu`, and `StoredType("CPU")` is `"cpu"`,
// a different row entirely.
//
// Two rounds of this test asserted the wrong key and failed with "no row was
// filed" while the alert had fired correctly — which reads as a broken wire and
// was a broken assertion, twice. Passing the DISPLAY string the rule actually
// emits, through the same `StoredType` the writer uses, is what makes it
// self-consistent instead of a second copy of the mapping.
func storedKey(r, display, s string) string {
	return key(r, alert.StoredType(display), s)
}

func (f *fakeHist) HasOpenAlert(r, t, s string) bool {
	f.calls = append(f.calls, "has "+key(r, t, s))
	return f.open[key(r, t, s)]
}

func (f *fakeHist) InsertAlertEvent(r, t, s, detail string, now int64) int64 {
	f.calls = append(f.calls, fmt.Sprintf("insert %s detail=%q", key(r, t, s), detail))
	f.nowOf = append(f.nowOf, now)
	f.open[key(r, t, s)] = true
	f.next++
	return f.next
}

func (f *fakeHist) ResolveAlertEvent(r, t, s string, now int64) []int64 {
	f.calls = append(f.calls, "resolve "+key(r, t, s))
	f.nowOf = append(f.nowOf, now)
	if !f.open[key(r, t, s)] {
		return []int64{}
	}
	delete(f.open, key(r, t, s))
	return []int64{1}
}

func onSettings() alert.Settings {
	return alert.Settings{
		CPUThreshold: 80, NotifCPU: true, NotifRouterUpdate: true,
		PingLoss: 20, NotifPing: true, NotifNetwatch: true,
		NotifIfaceUpDown: true, NotifVPN: true, NotifBGP: true,
		IfaceTypeFilters: map[string]bool{
			"notifIfaceEther": true, "notifIfaceWlan": true, "notifIfaceBridge": true,
			"notifIfaceVlan": true, "notifIfaceOther": true,
		},
	}
}

func wireOn(t *testing.T) (*Wire, *fakeHist) {
	t.Helper()
	h := newHist()
	w := New(h, onSettings())
	// A FIXED CLOCK, so "one instant per event" is assertable.
	var tick int64 = 1699996400000
	w.now = func() int64 { tick += 1000; return tick }
	return w, h
}

var router = alert.Router{ID: "r-1", AlertsEnabled: true}

// ── the routing question ────────────────────────────────────────────────────

// An event with no rule must do NOTHING — not even ask the database. This sits
// in the emit path of every collector, so a stray HasOpen per payload would be
// a query per router per poll for a dozen collectors that have no alerts at all.
func TestAnEventWithNoRuleTouchesNothing(t *testing.T) {
	w, h := wireOn(t)
	for _, ev := range []string{"dns:update", "bridges:update", "packages:update"} {
		if got := w.Evaluate(router, ev, &collect.SystemPayload{CPULoad: 99}); got != nil {
			t.Errorf("%s fired %v", ev, got)
		}
	}
	if len(h.calls) != 0 {
		t.Errorf("the database was asked %v for events with no rule", h.calls)
	}
	if w.Routers() != 0 {
		t.Error("an evaluator was built for an event with no rule")
	}
}

// THE TYPE IS THE GUARD, not the name. A payload of the wrong type under a known
// event name evaluates nothing rather than being coerced.
func TestAMismatchedPayloadEvaluatesNothing(t *testing.T) {
	w, h := wireOn(t)
	if got := w.Evaluate(router, "system:update", &collect.PingPayload{}); got != nil {
		t.Errorf("a ping payload under system:update fired %v", got)
	}
	if got := w.Evaluate(router, "system:update", "not a payload at all"); got != nil {
		t.Errorf("a string fired %v", got)
	}
	if len(h.calls) != 0 {
		t.Errorf("the database was asked %v", h.calls)
	}
}

// A ROUTER WITH NO ID evaluates nothing. `alert.Router`'s own comment records
// that the live `fire` guards on it, and that a fixture without one made every
// case read as "no alert fired".
func TestARouterWithNoIDIsIgnored(t *testing.T) {
	w, h := wireOn(t)
	got := w.Evaluate(alert.Router{AlertsEnabled: true}, "system:update",
		&collect.SystemPayload{CPULoad: 99})
	if got != nil {
		t.Errorf("fired %v for a router with no id", got)
	}
	if len(h.calls) != 0 {
		t.Errorf("wrote %v", h.calls)
	}
	// AND NO EVALUATOR IS BUILT. The rules guard on the id too, so a wire that
	// let it through would still fire nothing — but it would key an evaluator on
	// the empty string and grow one entry per id-less event forever.
	if w.Routers() != 0 {
		t.Errorf("%d evaluator(s) built for a router with no id", w.Routers())
	}
}

// ── the rules, through the wire ─────────────────────────────────────────────

func TestCPUCrossingTheThresholdFilesAndResolves(t *testing.T) {
	w, h := wireOn(t)

	if got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 1 {
		t.Fatalf("a first reading over the threshold fired %v", got)
	}
	if !h.open[storedKey("r-1", "High CPU", "")] {
		t.Error("no cpu row was filed")
	}

	// STILL HIGH: no second row. The dedup is the database's answer, and this is
	// what would break if `subject IS ?` were `= ?` — see internal/db.
	before := len(h.calls)
	if got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 95}); len(got) != 0 {
		t.Errorf("a second high reading fired %v", got)
	}
	for _, c := range h.calls[before:] {
		if len(c) > 6 && c[:6] == "insert" {
			t.Errorf("a second row was filed while the first was open: %s", c)
		}
	}

	// BACK DOWN: resolved.
	got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 10})
	if len(got) != 1 || !got[0].Up {
		t.Errorf("coming back under the threshold produced %v", got)
	}
	if h.open[storedKey("r-1", "High CPU", "")] {
		t.Error("the cpu row is still open after recovery")
	}
}

// ONE INSTANT PER EVENT. Every row a single payload files carries the same
// timestamp — see the `store` comment for why this differs from the live app.
func TestOneEventStampsOneInstant(t *testing.T) {
	w, h := wireOn(t)
	// Two interfaces going down together: two rows, one event.
	got := w.Evaluate(router, "ifstatus:update", &collect.IfStatusPayload{
		Interfaces: []collect.Interface{
			{Name: "ether1", Type: "ether", Running: true},
			{Name: "ether2", Type: "ether", Running: true},
		}})
	_ = got
	h.nowOf = nil
	got = w.Evaluate(router, "ifstatus:update", &collect.IfStatusPayload{
		Interfaces: []collect.Interface{
			{Name: "ether1", Type: "ether", Running: false},
			{Name: "ether2", Type: "ether", Running: false},
		}})
	if len(got) != 2 {
		t.Fatalf("two interfaces going down fired %d alert(s): %v", len(got), got)
	}
	if len(h.nowOf) < 2 {
		t.Fatalf("only %d write(s) recorded a timestamp", len(h.nowOf))
	}
	// EQUAL TO THE WIRE'S OWN CLOCK, not merely equal to each other.
	//
	// Self-consistency is not enough: a mutant reading `time.Now()` inside
	// `Record` produces two writes in the same millisecond and passes. The
	// injected clock returns a value no real clock will, so this can only pass
	// if the instant came from `w.now()` — which is the property.
	want := h.nowOf[0]
	if want < 1699996400000 || want > 1699996500000 {
		t.Errorf("the stamp is %d, which is not from the injected clock — it was read "+
			"from the real one, per write", want)
	}
	for _, ts := range h.nowOf {
		if ts != want {
			t.Errorf("one event stamped %v — the instant is read per write, not per event",
				h.nowOf)
			break
		}
	}
}

// A VPN tunnel's STATE STRING reaches the rule unchanged. The adapter must not
// collapse it to a boolean: "stale" and "never" are both disconnected, and a
// port comparing `!= "stale"` misses a peer going to "never".
func TestTheVPNStateStringSurvivesTheAdapter(t *testing.T) {
	w, _ := wireOn(t)
	w.Evaluate(router, "vpn:update", &collect.VPNPayload{
		Tunnels: []collect.Tunnel{{Name: "wg0", State: "active"}}})
	for _, state := range []string{"stale", "never"} {
		w2, h2 := wireOn(t)
		w2.Evaluate(router, "vpn:update", &collect.VPNPayload{
			Tunnels: []collect.Tunnel{{Name: "wg0", State: "active"}}})
		got := w2.Evaluate(router, "vpn:update", &collect.VPNPayload{
			Tunnels: []collect.Tunnel{{Name: "wg0", State: state}}})
		if len(got) != 1 {
			t.Errorf("active -> %q fired %v, want one alert", state, got)
		}
		if !h2.open[storedKey("r-1", "VPN Disconnected", "wg0")] {
			t.Errorf("active -> %q filed no row", state)
		}
	}
}

// A NIL PING LOSS IS NOT ZERO.
//
// The rule treats a missing reading as "no answer yet"; zero would RESOLVE an
// outstanding loss alert that is still true. A payload with no `loss` arrives
// whenever the ping collector could not run — which is exactly when the alert
// most needs to stay open.
func TestANilPingLossDoesNotResolve(t *testing.T) {
	w, h := wireOn(t)
	lossy, clean := 60, 0

	if got := w.Evaluate(router, "ping:update", &collect.PingPayload{
		Target: "8.8.8.8", Loss: &lossy}); len(got) != 1 {
		t.Fatalf("60%% loss fired %v", got)
	}
	openKey := storedKey("r-1", "Ping Loss", "8.8.8.8")
	if !h.open[openKey] {
		t.Fatalf("no ping row was filed; calls were %v", h.calls)
	}

	// NO READING AT ALL. The alert must stay open.
	if got := w.Evaluate(router, "ping:update", &collect.PingPayload{
		Target: "8.8.8.8", Loss: nil}); len(got) != 0 {
		t.Errorf("a payload with no loss reading fired %v", got)
	}
	if !h.open[openKey] {
		t.Error("a missing loss reading RESOLVED the alert — nil was read as zero")
	}

	// A real zero does resolve it.
	if got := w.Evaluate(router, "ping:update", &collect.PingPayload{
		Target: "8.8.8.8", Loss: &clean}); len(got) != 1 || !got[0].Up {
		t.Errorf("0%% loss produced %v, want a resolution", got)
	}
}

// AN INTERFACE THE ADMIN DISABLED is not an interface that went down.
//
// `alert.Interface` carries `Disabled` for that reason, and dropping it in the
// adapter would file a link-down alert every time somebody disabled a port on
// purpose.
func TestADisabledInterfaceIsNotAnOutage(t *testing.T) {
	w, h := wireOn(t)
	w.Evaluate(router, "ifstatus:update", &collect.IfStatusPayload{
		Interfaces: []collect.Interface{{Name: "ether1", Type: "ether", Running: true}}})

	got := w.Evaluate(router, "ifstatus:update", &collect.IfStatusPayload{
		Interfaces: []collect.Interface{
			{Name: "ether1", Type: "ether", Running: false, Disabled: true},
		}})
	if len(got) != 0 {
		t.Errorf("disabling an interface fired %v", got)
	}
	if h.open[storedKey("r-1", "Interface Down", "ether1")] {
		t.Error("a deliberately disabled interface filed a link-down alert")
	}
}

// THE UPDATE RULE COMPARES LATEST AGAINST RUNNING, and the adapter must not pass
// the same string for both — that reads as "already up to date" on every router,
// so the RouterOS-update alert could never fire.
func TestTheUpdateRuleGetsBothVersions(t *testing.T) {
	w, h := wireOn(t)
	got := w.Evaluate(router, "system:update", &collect.SystemPayload{
		CPULoad: 5, Version: "7.23", LatestVersion: "7.24", UpdateAvailable: true})
	if len(got) != 1 {
		t.Fatalf("an available update fired %v; calls were %v", got, h.calls)
	}
	// THE DETAIL IS WHERE THE TWO VERSIONS SHOW UP, and it is the only place.
	//
	// The rule fires on `available && latest != ""` — it does NOT compare the two
	// to decide — so passing the running version for both still produces exactly
	// one alert and a count check passes against it. What changes is the row an
	// operator reads: "RouterOS 7.23 is available (running 7.23)".
	//
	// A mutant doing that survived until this assertion existed.
	var detail string
	for _, c := range h.calls {
		if len(c) > 6 && c[:6] == "insert" {
			detail = c
		}
	}
	if detail == "" {
		t.Fatalf("no row filed for an available update; calls were %v", h.calls)
	}
	if !strings.Contains(detail, "7.24") {
		t.Errorf("the row is %s — it does not name the LATEST version, so the adapter "+
			"passed the running one for both", detail)
	}
	if !strings.Contains(detail, "7.23") {
		t.Errorf("the row is %s — it does not name the RUNNING version", detail)
	}
}

// ── the per-router state ────────────────────────────────────────────────────

// TWO ROUTERS ARE TWO EVALUATORS. A shared one would let one router's reading
// satisfy another's edge check.
func TestEachRouterHasItsOwnEdgeState(t *testing.T) {
	w, h := wireOn(t)
	a := alert.Router{ID: "r-a", AlertsEnabled: true}
	b := alert.Router{ID: "r-b", AlertsEnabled: true}

	if got := w.Evaluate(a, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 1 {
		t.Fatalf("router a fired %v", got)
	}
	// Router b's FIRST reading is also over the threshold and must alert on its
	// own account.
	if got := w.Evaluate(b, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 1 {
		t.Errorf("router b fired %v — its edge state was satisfied by router a", got)
	}
	if !h.open[storedKey("r-a", "High CPU", "")] || !h.open[storedKey("r-b", "High CPU", "")] {
		t.Error("both routers should have an open cpu row")
	}
	if w.Routers() != 2 {
		t.Errorf("%d evaluator(s) for two routers", w.Routers())
	}
}

// DROPPING forgets the edge state, and the live comment says what that costs: a
// rebuilt evaluator reports a persisting condition again. The DATABASE is what
// stops that becoming a duplicate row — which is the whole reason HasOpen asks
// it rather than the evaluator.
func TestADroppedEvaluatorRebuildsButDoesNotDuplicate(t *testing.T) {
	w, h := wireOn(t)
	if got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 1 {
		t.Fatalf("first firing produced %v", got)
	}
	w.Drop("r-1")
	if w.Routers() != 0 {
		t.Fatal("the evaluator was not dropped")
	}

	// The condition is STILL TRUE and the rebuilt evaluator has no memory of it.
	got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 94})
	if len(got) != 0 {
		t.Errorf("a rebuilt evaluator fired %v for an already-open alert — the database "+
			"dedup did not hold", got)
	}
	inserts := 0
	for _, c := range h.calls {
		if len(c) > 6 && c[:6] == "insert" {
			inserts++
		}
	}
	if inserts != 1 {
		t.Errorf("%d rows filed for one condition across an evaluator drop", inserts)
	}
}

// SETTINGS ARE REPLACED IN PLACE. Turning a toggle off must not clear the edge
// state, or every currently-true condition re-fires on the next save.
func TestSettingsChangeKeepsTheEdgeState(t *testing.T) {
	w, _ := wireOn(t)
	if got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 1 {
		t.Fatalf("first firing produced %v", got)
	}
	before := w.Routers()
	off := onSettings()
	off.NotifCPU = false
	w.SetSettings(off)
	if w.Routers() != before {
		t.Errorf("%d evaluator(s) after a settings change, was %d — they were rebuilt",
			w.Routers(), before)
	}
	// With the toggle off the rule is silent, which is the point of the toggle.
	if got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 10}); len(got) != 0 {
		t.Errorf("the cpu rule fired %v with its toggle off", got)
	}

	// AND THE EDGE STATE SURVIVED. Turn the toggle back on and send a reading
	// that is STILL high: the evaluator remembers it was already alerting, so
	// nothing new fires. A rebuild would have cleared that memory and re-fired,
	// which is the burst this exists to prevent — and the row-count check above
	// cannot see it, because a rebuild keeps the count identical.
	w2, h2 := wireOn(t)
	if got := w2.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 1 {
		t.Fatalf("setup: first firing produced %v", got)
	}
	h2.open = map[string]bool{} // forget the row, so ONLY the edge state can suppress
	on := onSettings()
	on.PingLoss = 33 // an unrelated change, so the cpu toggle itself is untouched
	w2.SetSettings(on)
	if got := w2.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 95}); len(got) != 0 {
		t.Errorf("a settings change made a still-true condition re-fire (%v) — the "+
			"evaluators were rebuilt and lost their edge state", got)
	}
}

// A ROUTER WITH ALERTS DISABLED evaluates nothing, and the flag is re-read on
// every event rather than captured — `alert.Evaluator`'s own comment says the
// live code does that "in case it was toggled after session creation".
func TestAlertsDisabledIsReReadEachEvent(t *testing.T) {
	w, h := wireOn(t)
	off := alert.Router{ID: "r-1", AlertsEnabled: false}
	if got := w.Evaluate(off, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 0 {
		t.Errorf("fired %v with alerts disabled", got)
	}
	// Now ON, same wire, same evaluator.
	if got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 94}); len(got) != 1 {
		t.Errorf("fired %v after alerts were re-enabled", got)
	}
	if !h.open[storedKey("r-1", "High CPU", "")] {
		t.Error("no row filed after re-enabling")
	}
}

// A nil Wire is inert rather than a panic: the session builds one only when
// history is configured.
func TestANilWireIsInert(t *testing.T) {
	var w *Wire
	if got := w.Evaluate(router, "system:update", &collect.SystemPayload{CPULoad: 99}); got != nil {
		t.Errorf("a nil wire fired %v", got)
	}
}

// ── THE CRASH THIS LOCK EXISTS FOR ────────────────────────────────────────
//
// `alert.Evaluator` keeps its edge state in plain maps with no lock, because the
// thing it was ported from cannot race — JavaScript is single-threaded, so the
// live evaluator is reached from one event loop.
//
// This port reaches it from many goroutines: every collector has its own poll
// timer, and `internal/alertpool` multiplied that by the fleet. On 2026-08-29
// the server died mid page-sweep with
//
//	fatal error: concurrent map writes
//	  alert.(*Evaluator).IfstatusUpdate  eval.go:428
//
// A fatal error is not a failed request — it takes the process, so every router
// loses monitoring until something restarts it.
//
// This drives ONE router from many goroutines at once. Without the per-router
// lock it fails as a hard runtime fatal (which no `recover` can catch), so the
// signal is the test binary dying rather than a neat assertion — which is
// exactly what happened in production.
func TestOneRoutersRulesAreNotEvaluatedConcurrently(t *testing.T) {
	w := New(newHist(), alert.Settings{})
	r := alert.Router{ID: "r-1", AlertsEnabled: true}

	// Payloads for four different rule families, so the goroutines write
	// DIFFERENT maps on the same evaluator — which is the shape that crashed.
	cpu := 10
	loss := 0
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 60; j++ {
				switch (n + j) % 4 {
				case 0:
					w.Evaluate(r, "system:update", &collect.SystemPayload{CPULoad: cpu})
				case 1:
					w.Evaluate(r, "ifstatus:update", &collect.IfStatusPayload{
						Interfaces: []collect.Interface{
							{Name: "ether1", Type: "ether", Running: j%2 == 0},
							{Name: "ether2", Type: "ether", Running: j%3 == 0},
						}})
				case 2:
					w.Evaluate(r, "vpn:update", &collect.VPNPayload{
						Tunnels: []collect.Tunnel{{Name: "wg0", State: "active"}}})
				default:
					w.Evaluate(r, "ping:update", &collect.PingPayload{
						Target: "1.1.1.1", Loss: &loss})
				}
			}
		}(i)
	}
	wg.Wait()

	// Reaching here at all is the assertion. One evaluator, and it survived.
	if got := w.Routers(); got != 1 {
		t.Errorf("Routers() = %d, want 1 — the concurrent callers built more than one "+
			"evaluator for the same router, so each holds a fraction of the edge state", got)
	}
}

// A COLLECTOR THAT HAS NEVER CHECKED FOR UPDATES MUST NOT RESOLVE AN OPEN ALERT.
//
// ── THE MEASURED DEFECT ───────────────────────────────────────────────────
//
// 50 `routeros_update` fire/resolve pairs in 24 hours on the active router,
// against ZERO in the live app over the same period. `updateVerdict` returns
// false when there is no `latest-version` and no `status`, and `updateRule`
// reads false as "the router reached the version" and closes the alert.
//
// It happens because this port runs TWO System collectors per router — the
// session's and the alertpool's — with private update state, which
// `collect/system.go` warned about in advance: "A second session type would need
// the shared map back." The session's has run the check; the pool's has not.
//
// The sequence below is the real one: a browser opens (fire), the browser closes
// and the pool takes over with an unchecked collector (previously: resolve).
func TestAnUncheckedSystemPayloadDoesNotResolveAnUpdateAlert(t *testing.T) {
	w, _ := wireOn(t)

	// 1. The session's collector: the check has run, an update is available.
	fired := w.Evaluate(router, "system:update", &collect.SystemPayload{
		Version: "7.24", LatestVersion: "7.24.1", UpdateStatus: "New version is available",
		UpdateAvailable: true,
	})
	if len(fired) != 1 || fired[0].Up {
		t.Fatalf("the first payload produced %+v, want one alert firing", fired)
	}

	// 2. The pool's collector: never checked, so NO version and NO status.
	got := w.Evaluate(router, "system:update", &collect.SystemPayload{
		Version: "7.24", LatestVersion: "", UpdateStatus: "", UpdateAvailable: false,
	})
	for _, f := range got {
		if f.Up {
			t.Errorf("an unchecked payload RESOLVED an alert (%q). It carries no update "+
				"information at all — that is not the same as the router being up to "+
				"date, and treating it as such closes an alert another collector opened.",
				f.AlertType)
		}
	}

	// 3. AND A REAL "up to date" STILL RESOLVES. The fix must not make the
	//    resolution unreachable — an operator who upgrades has to see it close.
	got = w.Evaluate(router, "system:update", &collect.SystemPayload{
		Version: "7.24.1", LatestVersion: "7.24.1", UpdateStatus: "System is already up to date",
		UpdateAvailable: false,
	})
	ups := 0
	for _, f := range got {
		if f.Up {
			ups++
		}
	}
	if ups != 1 {
		t.Errorf("a genuine up-to-date payload produced %d resolution(s), want 1: %+v",
			ups, got)
	}

	// 4. AND A STATUS-ONLY PAYLOAD IS STILL A REAL READING. The guard tests BOTH
	//    fields for a reason: with no `latest-version` the verdict falls back to
	//    `strings.Contains(status, "new version")`, so a router that reports its
	//    state in words alone is checked, not unknown. A guard that looked only
	//    at `latest-version` would take the CPU-only path here and lose the
	//    resolution entirely — a mutation doing exactly that survived until this
	//    case existed.
	w2, _ := wireOn(t)
	if f := w2.Evaluate(router, "system:update", &collect.SystemPayload{
		Version: "7.24", LatestVersion: "7.24.1", UpdateStatus: "New version is available",
		UpdateAvailable: true,
	}); len(f) != 1 {
		t.Fatalf("setup: the update alert did not open (%+v)", f)
	}
	got = w2.Evaluate(router, "system:update", &collect.SystemPayload{
		Version: "7.24.1", LatestVersion: "", UpdateStatus: "System is already up to date",
		UpdateAvailable: false,
	})
	ups = 0
	for _, f := range got {
		if f.Up {
			ups++
		}
	}
	if ups != 1 {
		t.Errorf("a status-only up-to-date payload produced %d resolution(s), want 1. "+
			"It has no latest-version but it HAS been checked; treating it as unknown "+
			"leaves the alert open for ever.", ups)
	}

	// 5. A TRANSIENT STATUS IS STILL UNKNOWN, and this is the case the first fix
	//    missed. "finding out latest version..." has a status and no version, so
	//    a guard of `latest == "" && status == ""` let it through and
	//    `updateVerdict` read it as "up to date". FOUR ROWS appeared after that
	//    fix shipped, which is how the subset was found.
	for _, transient := range []string{
		"finding out latest version...", "checking for updates", "Update in progress",
	} {
		w3, _ := wireOn(t)
		if f := w3.Evaluate(router, "system:update", &collect.SystemPayload{
			Version: "7.24", LatestVersion: "7.24.1", UpdateStatus: "New version is available",
			UpdateAvailable: true,
		}); len(f) != 1 {
			t.Fatalf("setup: the update alert did not open (%+v)", f)
		}
		for _, f := range w3.Evaluate(router, "system:update", &collect.SystemPayload{
			Version: "7.24", LatestVersion: "", UpdateStatus: transient, UpdateAvailable: false,
		}) {
			if f.Up {
				t.Errorf("a payload whose status is %q RESOLVED the alert. The router is "+
					"still working out the answer; that is not a verdict.", transient)
			}
		}
	}
}

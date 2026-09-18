package collect

import (
	"errors"
	"mikrodash/internal/hub"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/roscache"

	"mikrodash/internal/routeros"
)

// ── THE SPLIT IS NOT COVERED BY THE GOLDEN, SO IT IS COVERED HERE ────────────
//
// `fixture_test.go` builds this collector at a 30s poll, which is at or above
// `ifStatusMetaTarget`, so `metaTicks` returns 1 and the replay reads all four
// menus on every tick exactly as it did before the split. That is why the golden
// stayed green through this change — a fact worth stating rather than enjoying.
//
// These tests drive the branch the golden cannot reach: a fast poll, where the
// three metadata menus must fall behind the rates read and the payload must stay
// complete anyway.

type splitReader struct {
	byMenu map[string]int
}

func (r *splitReader) Connected() bool { return true }

func (r *splitReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.byMenu[cmd.Path]++
	switch cmd.Path {
	case "/interface/print":
		return []routeros.Reply{{
			"name": "ether1", "type": "ether", "running": "true", "disabled": "false",
			"mac-address": "02:00:00:00:00:01", "rx-byte": "100", "tx-byte": "200",
			"rx-error": "1", "tx-error": "0", "rx-drop": "0", "tx-drop": "0",
			"tx-queue-drop": "0", "link-downs": "0",
		}}, nil
	case "/ip/address/print":
		return []routeros.Reply{{"interface": "ether1", "address": "198.51.100.1/24"}}, nil
	case "/interface/ethernet/print":
		return []routeros.Reply{{"name": "ether1", "rx-fcs-error": "0"}}, nil
	case "/interface/monitor-traffic":
		// A different value every call, so a suppressed emit is distinguishable
		// from a stale one.
		n := r.byMenu[cmd.Path]
		return []routeros.Reply{{
			"name":               "ether1",
			"rx-bits-per-second": strings.Repeat("1", n) + "000000",
			"tx-bits-per-second": "2000000",
		}}, nil
	}
	return nil, nil
}

// TestIfStatusSplitsTheMetadataReads is the whole point of the split: at a fast
// poll the rates read runs on every tick and the other three do not.
func TestIfStatusSplitsTheMetadataReads(t *testing.T) {
	r := &splitReader{byMenu: map[string]int{}}
	c := NewIfStatus(r, hub.Relay{}, "r1", 1000)

	want := c.metaTicks()
	if want < 2 {
		t.Fatalf("a 1s poll must split; metaTicks = %d", want)
	}

	// Enough ticks to cross the boundary twice, whatever the target is set to.
	ticks := 2*want + 1
	for i := 0; i < ticks; i++ {
		c.Tick()
	}

	if got := r.byMenu["/interface/monitor-traffic"]; got != ticks {
		t.Errorf("rates read %d times, want one per tick (%d)", got, ticks)
	}
	wantMeta := 1 + (ticks-1)/want // the first tick, then one every `want`
	for _, menu := range []string{"/interface/print", "/ip/address/print", "/interface/ethernet/print"} {
		if got := r.byMenu[menu]; got != wantMeta {
			t.Errorf("%s read %d times over %d ticks, want %d", menu, got, ticks, wantMeta)
		}
	}
}

// TestIfStatusPublishesFullRowsBetweenMetadataReads pins the property that makes
// the split safe: a tick that fetched nothing but rates still emits an interface
// carrying its addresses, MAC and counters, and carries the NEW rate.
func TestIfStatusPublishesFullRowsBetweenMetadataReads(t *testing.T) {
	r := &splitReader{byMenu: map[string]int{}}
	c := NewIfStatus(r, hub.Relay{}, "r1", 1000)

	c.Tick() // reads everything
	first := c.Last()
	c.Tick() // rates only
	second := c.Last()

	if len(second.Interfaces) != 1 {
		t.Fatalf("second tick published %d interfaces, want 1", len(second.Interfaces))
	}
	got := second.Interfaces[0]
	if len(got.IPs) != 1 || got.IPs[0] != "198.51.100.1/24" {
		t.Errorf("addresses lost between metadata reads: %v", got.IPs)
	}
	if got.MacAddr == "" || got.RxBytes == nil {
		t.Errorf("metadata lost between metadata reads: %+v", got)
	}
	if got.RxMbps == first.Interfaces[0].RxMbps {
		t.Errorf("rates did not move on a rates-only tick: %v", got.RxMbps)
	}
}

// TestIfStatusRatesNeverWriteBackIntoTheHeldMetadata is the copy in Tick. Held
// rows are reused every tick, so a rate written into them would outlive the
// reading it came from — an interface would show its last known speed for ever.
func TestIfStatusRatesNeverWriteBackIntoTheHeldMetadata(t *testing.T) {
	r := &splitReader{byMenu: map[string]int{}}
	c := NewIfStatus(r, hub.Relay{}, "r1", 1000)
	c.Tick()

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.base) != 1 {
		t.Fatalf("held %d rows, want 1", len(c.base))
	}
	if c.base[0].RxMbps != 0 || c.base[0].TxMbps != 0 {
		t.Errorf("held metadata carries a rate: rx=%v tx=%v", c.base[0].RxMbps, c.base[0].TxMbps)
	}
}

// TestIfStatusSlowPollReadsEveryMenuEveryTick records the collapse case, and it
// is the branch the frozen golden takes. If this ever fails, the golden replay
// is no longer exercising the code it was recorded against.
func TestIfStatusSlowPollReadsEveryMenuEveryTick(t *testing.T) {
	r := &splitReader{byMenu: map[string]int{}}
	c := NewIfStatus(r, hub.Relay{}, "r1", 30000)
	if n := c.metaTicks(); n != 1 {
		t.Fatalf("a 30s poll must not split; metaTicks = %d", n)
	}
	for i := 0; i < 3; i++ {
		c.Tick()
	}
	for _, menu := range []string{
		"/interface/print", "/ip/address/print",
		"/interface/ethernet/print", "/interface/monitor-traffic",
	} {
		if got := r.byMenu[menu]; got != 3 {
			t.Errorf("%s read %d times over 3 ticks, want 3", menu, got)
		}
	}
}

// TestIfStatusSuspendArmsTheNextMetadataRead: a blurred page can sit for an hour,
// and resuming must not publish an hour-old interface list beside a live rate.
func TestIfStatusSuspendArmsTheNextMetadataRead(t *testing.T) {
	r := &splitReader{byMenu: map[string]int{}}
	c := NewIfStatus(r, hub.Relay{}, "r1", 1000)
	c.Tick()
	before := r.byMenu["/interface/print"]

	c.Suspend()
	c.Tick()

	if got := r.byMenu["/interface/print"]; got != before+1 {
		t.Errorf("first tick after Suspend read metadata %d times, want %d", got-before, 1)
	}
}

// ── THE FAST/SLOW RULE ACROSS COLLECTORS ────────────────────────────────────

type routeSplitReader struct{ byMenu map[string]int }

func (r *routeSplitReader) Connected() bool { return true }
func (r *routeSplitReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.byMenu[cmd.Path]++
	switch cmd.Path {
	case "/ip/route/print", "/ipv6/route/print":
		return []routeros.Reply{{".id": "*1", "dst-address": "0.0.0.0/0", "gateway": "198.51.100.1", "active": "true"}}, nil
	case "/routing/bgp/session/print":
		return []routeros.Reply{{"name": "peer1", "established": "true"}}, nil
	}
	return nil, nil
}

// TestRoutingSplitsTheRouteTables: BGP session state is what flaps and stays on
// the poll; the route tables are large, rarely move, and go to the slow lane.
func TestRoutingSplitsTheRouteTables(t *testing.T) {
	r := &routeSplitReader{byMenu: map[string]int{}}
	c := NewRouting(r, hub.Relay{}, 10000)

	const ticks = 9
	for i := 0; i < ticks; i++ {
		c.Tick()
	}

	if got := r.byMenu["/routing/bgp/session/print"]; got != ticks {
		t.Errorf("BGP read %d times, want one per tick (%d)", got, ticks)
	}
	want := ticks / routeConfigEvery
	for _, menu := range []string{"/ip/route/print", "/ipv6/route/print"} {
		if got := r.byMenu[menu]; got != want {
			t.Errorf("%s read %d times over %d ticks, want %d", menu, got, ticks, want)
		}
	}
}

// TestRoutingFirstTickReadsTheRoutes pins tick 0 taking the slow lane too: a
// payload whose route list is empty until the third poll is a blank page, not a
// stale one.
func TestRoutingFirstTickReadsTheRoutes(t *testing.T) {
	r := &routeSplitReader{byMenu: map[string]int{}}
	c := NewRouting(r, hub.Relay{}, 10000)
	c.Tick()
	if got := r.byMenu["/ip/route/print"]; got != 1 {
		t.Errorf("first tick read the route table %d times, want 1", got)
	}
	if p := c.Last(); p == nil || len(p.Routes) == 0 {
		t.Errorf("first payload carries no routes: %+v", p)
	}
}

// TestRoutingWriteDoesNotWaitForTheSlowLane: RefreshNow is what the route write
// path calls, and it must re-read immediately rather than let an operator watch
// their own edit take half a minute to appear.
func TestRoutingWriteDoesNotWaitForTheSlowLane(t *testing.T) {
	r := &routeSplitReader{byMenu: map[string]int{}}
	c := NewRouting(r, hub.Relay{}, 10000)
	c.Tick()
	before := r.byMenu["/ip/route/print"]
	c.RefreshNow()
	if got := r.byMenu["/ip/route/print"]; got != before+1 {
		t.Errorf("RefreshNow read the route table %d extra times, want 1", got-before)
	}
}

// ── PHASE 4.1: THE DERIVATION, TESTED WITHOUT A COLLECTOR ───────────────────
//
// `BuildIfStatus` is the worked example for extracting a derivation as a pure
// function. The point of the extraction is exactly this: the error and drop
// figures the page shows are DELTAS, and until now the only way to test one was
// to drive a whole collector through two ticks against a fake router. Prior state
// as a PARAMETER makes it two calls and no I/O at all.

func ifRow(name string, rxErr, rxDrop string) routeros.Reply {
	return routeros.Reply{
		"name": name, "type": "ether", "running": "true", "disabled": "false",
		"rx-error": rxErr, "tx-error": "0",
		"rx-drop": rxDrop, "tx-drop": "0", "tx-queue-drop": "0",
	}
}

func TestBuildIfStatusDifferencesTwoReadings(t *testing.T) {
	t0 := time.Now()

	base, snap, delta := BuildIfStatus(nil, IfStatusInput{
		Ifaces: []routeros.Reply{ifRow("ether1", "5", "2")},
		Addrs:  []routeros.Reply{{"interface": "ether1", "address": "198.51.100.1/24"}},
		Now:    t0,
	})
	if len(base) != 1 {
		t.Fatalf("first reading built %d rows, want 1", len(base))
	}
	// THE FIRST READING HAS NO BASELINE, and must not invent one. A zero delta
	// here would read as "no errors in this window" on a link that has five.
	if len(delta) != 0 {
		t.Errorf("first reading produced a delta with nothing to subtract from: %v", delta)
	}
	if base[0].ErrorsDelta != nil {
		t.Errorf("first reading carries an error delta: %v", *base[0].ErrorsDelta)
	}

	base2, _, delta2 := BuildIfStatus(snap, IfStatusInput{
		Ifaces: []routeros.Reply{ifRow("ether1", "9", "2")},
		Addrs:  []routeros.Reply{{"interface": "ether1", "address": "198.51.100.1/24"}},
		Now:    t0.Add(30 * time.Second),
	})
	d, ok := delta2["ether1"]
	if !ok || d.errors == nil {
		t.Fatalf("second reading produced no error delta: %+v", delta2)
	}
	if *d.errors != 4 {
		t.Errorf("error delta = %v, want 4 (9 minus 5)", *d.errors)
	}
	if d.windowMs != 30000 {
		t.Errorf("delta window = %vms, want 30000 — the window is the gap between "+
			"the two readings, not the poll", d.windowMs)
	}
	if base2[0].DeltaWindowMs == nil || *base2[0].DeltaWindowMs != 30000 {
		t.Errorf("the window did not reach the payload: %+v", base2[0].DeltaWindowMs)
	}
}

// TestBuildIfStatusIsPure pins the property the extraction exists for: same
// inputs, same outputs, and the prior state handed in is not written through.
func TestBuildIfStatusIsPure(t *testing.T) {
	in := IfStatusInput{Ifaces: []routeros.Reply{ifRow("ether1", "5", "2")}, Now: time.Now()}
	prior := map[string]counterSnap{}

	a, snapA, _ := BuildIfStatus(prior, in)
	b, snapB, _ := BuildIfStatus(prior, in)

	if len(prior) != 0 {
		t.Errorf("the prior state passed in was mutated: %v", prior)
	}
	if len(a) != len(b) || a[0].Name != b[0].Name || len(snapA) != len(snapB) {
		t.Errorf("two identical calls disagreed: %+v vs %+v", a, b)
	}
}

// TestBuildIfStatusRefusesToBuildFromNothing: nil is "keep what you have", and
// the caller depends on it. Returning an empty slice instead would blank the
// Interfaces page on one failed read.
func TestBuildIfStatusRefusesToBuildFromNothing(t *testing.T) {
	base, snap, delta := BuildIfStatus(nil, IfStatusInput{Now: time.Now()})
	if base != nil || snap != nil || delta != nil {
		t.Errorf("no rows produced a payload: %v %v %v", base, snap, delta)
	}
}

// TestBuildFirewallRuleReadsPriorAndReturnsNext pins the discipline that makes
// the stateful derivations safe to extract: the function READS the prior
// counters and never writes them, returning the new baseline for the caller to
// store. A derivation that advanced the baseline itself would move it even when
// the caller discarded the result.
func TestBuildFirewallRuleReadsPriorAndReturnsNext(t *testing.T) {
	prev := map[string]fwCount{}
	row := routeros.Reply{".id": "*1", "chain": "input", "action": "accept", "packets": "100", "bytes": "9000"}

	first, count := BuildFirewallRule(prev, "filter", row)
	if first.DeltaPackets != 0 {
		t.Errorf("first reading invented a delta: %d", first.DeltaPackets)
	}
	if len(prev) != 0 {
		t.Errorf("the derivation wrote to the prior state it was handed: %v", prev)
	}
	if count.packets != 100 {
		t.Errorf("returned baseline = %d packets, want 100", count.packets)
	}

	prev[countKey("filter", "*1")] = count
	row["packets"] = "140"
	second, _ := BuildFirewallRule(prev, "filter", row)
	if second.DeltaPackets != 40 {
		t.Errorf("delta = %d, want 40 (140 minus 100)", second.DeltaPackets)
	}

	// A COUNTER THAT WENT BACKWARDS is a rule recreated on the same id, not
	// forty negative packets. The clamp is what stops the page showing one.
	row["packets"] = "5"
	third, _ := BuildFirewallRule(prev, "filter", row)
	if third.DeltaPackets != 0 {
		t.Errorf("a reset counter produced delta %d, want 0", third.DeltaPackets)
	}
}

// TestBuildLanOverviewIsAFunctionOfItsInputs: the largest derivation extracted,
// and it carries no state at all. Two identical calls must agree, and the lease
// list it is handed must not be modified.
func TestBuildLanOverviewIsAFunctionOfItsInputs(t *testing.T) {
	in := LanInput{
		Nets:     []routeros.Reply{{"address": "192.168.88.0/24", "gateway": "192.168.88.1"}},
		Addrs:    []routeros.Reply{{"interface": "ether1", "address": "198.51.100.2/24"}},
		LeaseIPs: []string{"192.168.88.10", "192.168.88.11"},
		WanIface: "ether1",
		Now:      time.Now(),
	}
	a := BuildLanOverview(in)
	b := BuildLanOverview(in)

	if len(in.LeaseIPs) != 2 {
		t.Errorf("the lease list handed in was modified: %v", in.LeaseIPs)
	}
	if a.WanIP != "198.51.100.2/24" {
		t.Errorf("WAN address = %q, want the address on the named interface", a.WanIP)
	}
	if len(a.Networks) != 1 || a.Networks[0].LeaseCount != 2 {
		t.Errorf("lease count did not land on the subnet: %+v", a.Networks)
	}
	if len(a.Networks) != len(b.Networks) || a.WanIP != b.WanIP {
		t.Errorf("two identical calls disagreed")
	}
}

// ── PHASE 3.2: THE SHARED SUBSCRIPTION HELPER ───────────────────────────────

type schedReader struct{ n int32 }

func (s *schedReader) Connected() bool { return true }
func (s *schedReader) Do(routeros.Cmd) ([]routeros.Reply, error) {
	atomic.AddInt32(&s.n, 1)
	return []routeros.Reply{{"a": "b"}}, nil
}

// TestScheduledFallsBackToTheLoopWithNoCache is the property that keeps the two
// background pools working: they build every collector without a cache, and a
// router nobody is watching still needs its alerts.
func TestScheduledFallsBackToTheLoopWithNoCache(t *testing.T) {
	var ticks int32
	loop := newPollLoop(func() { atomic.AddInt32(&ticks, 1) },
		func() time.Duration { return 5 * time.Millisecond })
	s := scheduled{loop: loop}

	if s.scheduling() {
		t.Fatal("a collector with no cache reported itself as scheduled")
	}
	s.begin()
	time.Sleep(40 * time.Millisecond)
	s.end()
	if atomic.LoadInt32(&ticks) == 0 {
		t.Error("begin did not start the fallback loop")
	}

	after := atomic.LoadInt32(&ticks)
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&ticks) != after {
		t.Error("end did not stop the fallback loop")
	}
}

// TestScheduledBeginIsIdempotent. Resume on a running collector must not add a
// second subscription: the demand set counts subscribers, so a duplicate would
// keep the menu alive after the real one released it — the collector would go on
// reading a router nobody is watching.
func TestScheduledBeginIsIdempotent(t *testing.T) {
	c := roscache.New(&schedReader{})
	s := scheduled{cache: c, menu: "/ip/dns/print",
		cadence: func() time.Duration { return time.Second }}

	s.begin()
	s.begin()
	s.begin()
	if got := len(c.Demand()); got != 1 {
		t.Fatalf("three begins produced %d demands, want 1", got)
	}
	s.end()
	if got := len(c.Demand()); got != 0 {
		t.Errorf("one end left %d demands; the extra begins leaked a subscription", got)
	}
}

// TestScheduledEndIsIdempotent: Stop after Suspend is an ordinary sequence in
// this app, and a second release must not disturb anything.
func TestScheduledEndIsIdempotent(t *testing.T) {
	c := roscache.New(&schedReader{})
	other := c.Subscribe("/ip/dns/print", nil, roscache.Every(time.Second), nil)
	defer other()

	s := scheduled{cache: c, menu: "/ip/dns/print",
		cadence: func() time.Duration { return time.Second }}
	s.begin()
	s.end()
	s.end()
	s.end()

	if got := len(c.Demand()); got != 1 {
		t.Errorf("repeated end removed another subscriber's demand: %d left, want 1", got)
	}
}

// TestScheduledWithNeitherCacheNorLoopIsInert. A misconfigured collector should
// read nothing, not panic on a nil dereference inside a timer goroutine where
// the stack says nothing useful.
func TestScheduledWithNeitherCacheNorLoopIsInert(t *testing.T) {
	s := scheduled{}
	s.begin()
	s.end()
}

// ── MECHANISM B: A MENU CHOSEN AT RUNTIME ───────────────────────────────────

// TestResubscribeMovesTheDemand. The old menu must actually go quiet, not just
// stop being consulted: the demand set is what decides whether the scheduler
// reads a menu at all, so a subscription left behind means the router keeps
// being asked for a table nobody is looking at.
func TestResubscribeMovesTheDemand(t *testing.T) {
	c := roscache.New(&schedReader{})
	s := scheduled{cache: c, menu: "/ip/firewall/filter/print",
		fields:  []string{".id", "packets"},
		cadence: func() time.Duration { return time.Second }}
	s.begin()

	s.resubscribe("/ip/firewall/nat/print", nil)
	d := c.Demand()
	if len(d) != 1 || d[0].Menu != "/ip/firewall/nat/print" {
		t.Fatalf("after resubscribe the demand is %+v; want exactly the nat menu", d)
	}
	if len(d[0].Fields) != 2 {
		t.Errorf("the field list was lost in the move: %v — an empty one means EVERY "+
			"field, and all-fields sticks for the session", d[0].Fields)
	}

	s.end()
	if got := len(c.Demand()); got != 0 {
		t.Errorf("end after a resubscribe left %d demands; the move leaked the old one", got)
	}
}

// TestResubscribeWhileSuspendedRecordsTheChoice. The firewall tab can be
// switched while the page is blurred, and `wifi` latches its stack from a read
// that Start performs before Resume. Both land here, and both must be picked up
// by the next begin rather than silently subscribing to the old menu.
func TestResubscribeWhileSuspendedRecordsTheChoice(t *testing.T) {
	c := roscache.New(&schedReader{})
	s := scheduled{cache: c, menu: "/interface/wifi/print",
		cadence: func() time.Duration { return time.Second }}

	s.resubscribe("/interface/wireless/print", nil)
	if got := len(c.Demand()); got != 0 {
		t.Fatalf("resubscribe while suspended declared %d demands; it must only record", got)
	}
	s.begin()
	d := c.Demand()
	if len(d) != 1 || d[0].Menu != "/interface/wireless/print" {
		t.Fatalf("begin subscribed to %+v; it ignored the recorded menu", d)
	}
	s.end()
}

// TestResubscribeToTheSameMenuIsANoOp, so a caller may pass its current
// selection unconditionally — which the firewall does, since `SetActiveTable`
// is reached from a frame the browser sends on every page focus.
func TestResubscribeToTheSameMenuIsANoOp(t *testing.T) {
	c := roscache.New(&schedReader{})
	fired := 0
	apply := func([]routeros.Reply, error) { fired++ }
	s := scheduled{cache: c, menu: "/ip/firewall/filter/print", apply: apply,
		cadence: func() time.Duration { return time.Second }}
	s.begin()

	s.resubscribe("/ip/firewall/filter/print", nil)
	if s.apply == nil {
		t.Error("a same-menu resubscribe replaced the callback; it must change nothing")
	}
	if got := len(c.Demand()); got != 1 {
		t.Errorf("a same-menu resubscribe left %d demands, want 1", got)
	}
	s.end()
}

// TestResubscribeBindsTheCallbackToTheNewMenu is the reason `resubscribe` takes
// a callback at all. Rows carry no menu, so a callback that resolves the menu
// itself can be handed the old table's rows after the field has moved — and
// since RouterOS `.id` values repeat across menus, that merge SUCCEEDS and
// reports the wrong numbers.
func TestResubscribeBindsTheCallbackToTheNewMenu(t *testing.T) {
	c := roscache.New(&schedReader{})
	got := make(chan string, 4)
	s := scheduled{cache: c, menu: "/a/print",
		apply:   func([]routeros.Reply, error) { got <- "a" },
		cadence: func() time.Duration { return time.Second }}
	s.begin()
	s.resubscribe("/b/print", func([]routeros.Reply, error) { got <- "b" })
	defer s.end()

	sc := roscache.NewScheduler(c, 5*time.Millisecond)
	sc.Start()
	defer sc.Stop()

	select {
	case which := <-got:
		if which != "b" {
			t.Fatalf("the scheduler delivered to the %q callback after a move to /b/print", which)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery after resubscribe")
	}
}

// TestVpnSlowMenusAreOnTheSlowLane.
//
// ── THE REGRESSION THIS PINS ────────────────────────────────────────────────
//
// `vpn` reads four menus and only ONE is live: WireGuard handshake ages move
// every few seconds, while PPP sessions and IPsec security associations change
// when a tunnel comes up or goes down.
//
// Mechanism A subscribed it to `/ppp/active` at `pollMs` — five seconds on the
// operator's install — which put all three slow menus on the fast lane. The
// POLLED path never did: its loop body is `RefreshNow`, WireGuard alone, and
// `loadOther` ran about once a minute.
//
// So 3.2a tripled this collector's cost, and nothing caught it: `vpn` only ran
// for a router somebody was watching, where the extra reads sat inside a much
// larger total. It surfaced when phase 4.3 began running it for UNWATCHED
// routers and the per-minute figures could be compared with the pool's.
func TestVpnSlowMenusAreOnTheSlowLane(t *testing.T) {
	// A fast poll must not drag the slow menus onto the fast lane.
	fast := NewVPN(nil, hub.Relay{}, 5000)
	if got := fast.sched.cadence(); got < vpnSlowTarget {
		t.Errorf("with a 5s poll the subscription asks for %v; the three non-WireGuard "+
			"menus belong on the %v lane. This is the shape that tripled vpn's cost in "+
			"3.2a.", got, vpnSlowTarget)
	}
	// A SLOWER POLL CANNOT ASK FOR MORE THAN THE TARGET, because this collector
	// clamps its own poll to thirty seconds -- the same value. A floor was
	// written here first to honour "the operator wants less", and the branch was
	// unreachable; the test says so rather than pretending otherwise.
	slow := NewVPN(nil, hub.Relay{}, 60000)
	if got := slow.sched.cadence(); got != vpnSlowTarget {
		t.Errorf("a 60s poll produced %v, want %v: clampPoll caps this collector at the "+
			"slow target, so there is no slower case to honour", got, vpnSlowTarget)
	}
	// And the residual loop stays FAST, because WireGuard is the live half.
	if got := fast.poll.bounded(); got > vpnSlowTarget {
		t.Errorf("the residual loop runs at %v; WireGuard handshake ages are the one "+
			"thing here that moves, and slowing them is the opposite of the fix", got)
	}
}

// ── B.2: THE DELIVERY BRANCH ────────────────────────────────────────────────
//
// `scheduled` gained one job: ask the cache to keep its menu current from an
// open channel instead of a read, when `eff.Stream` says so. Everything else is
// unchanged, which is what these pin — the subscription, the cadence and the
// `apply` callback must be identical either way, because the operator's rule is
// that mode switches DELIVERY ONLY and never touches intervals.

// streamReader is a schedReader that can also stream.
type streamReader struct {
	schedReader
	mu     sync.Mutex
	opens  int
	args   []string // each opened channel's arguments, in order
	refuse bool
}

func (r *streamReader) Stream(cmd routeros.Cmd, _ func(routeros.Reply)) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.refuse {
		return nil, errors.New("this router will not stream that")
	}
	r.opens++
	r.args = append(r.args, strings.Join(cmd.Args, " "))
	return func() {}, nil
}

func (r *streamReader) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opens
}

func streamed(t *testing.T, c *roscache.Cache) []string {
	t.Helper()
	return c.StreamedMenus()
}

// TestPollModeOpensNoChannel. The default, and the state every collector is in
// today: no decision installed on the cache means poll, and nothing must open.
//
// B.3 MOVED WHERE "POLL" IS SPELLED. It used to be a nil `stream` field on the
// collector; it is now the absence of a `StreamWhen` on the cache, or a decision
// that says no. One place instead of twenty-two.
func TestPollModeOpensNoChannel(t *testing.T) {
	r := &streamReader{}
	c := roscache.New(r) // no StreamWhen: nothing may stream
	s := scheduled{cache: c, menu: "/ip/dns/print",
		cadence: func() time.Duration { return time.Second }}
	s.begin()
	defer s.end()

	if n := r.count(); n != 0 {
		t.Errorf("%d channel(s) opened on a cache with no delivery decision", n)
	}
	if got := streamed(t, c); len(got) != 0 {
		t.Errorf("streamed menus = %v, want none", got)
	}
	if len(c.Demand()) != 1 {
		t.Error("poll mode did not subscribe; delivery is not the only thing that changed")
	}
}

// TestStreamModeOpensOneChannelAndKeepsTheSubscription is the property the whole
// design rests on: the menu is still in the demand set, still at its declared
// cadence, and only the FILLER differs.
func TestStreamModeOpensOneChannelAndKeepsTheSubscription(t *testing.T) {
	r := &streamReader{}
	c := roscache.New(r)
	c.StreamWhen(func(string) bool { return true })
	s := scheduled{cache: c, menu: "/ip/dns/print",
		cadence:    func() time.Duration { return time.Second },
		streamKey:  func(row routeros.Reply) string { return row[".id"] },
		streamArgs: func() []string { return []string{"=interval=1"} },
	}
	s.begin()

	if n := r.count(); n != 1 {
		t.Fatalf("%d channel(s) opened, want 1", n)
	}
	d := c.Demand()
	if len(d) != 1 || d[0].Menu != "/ip/dns/print" || d[0].Cadence != time.Second {
		t.Errorf("demand = %v; streaming must not disturb the subscription or its cadence", d)
	}

	// AND THE CHANNEL GOES WITH THE SUBSCRIPTION. A fill outliving its collector
	// is an open channel on a router nobody is reading.
	s.end()
	if got := streamed(t, c); len(got) != 0 {
		t.Errorf("still streaming %v after end()", got)
	}
}

// TestARetuneReopensTheStreamAtTheNewInterval. A streamed menu carries its
// interval in the command (`=interval=`), so a cadence the scheduler asks
// live is not enough: the channel must be closed and reopened. It was left
// open at the old interval until the collector was suspended (review loop,
// item 9).
func TestARetuneReopensTheStreamAtTheNewInterval(t *testing.T) {
	r := &streamReader{}
	c := roscache.New(r)
	c.StreamWhen(func(string) bool { return true })
	every := time.Second
	s := scheduled{cache: c, menu: "/ip/dns/print",
		cadence:   func() time.Duration { return every },
		fields:    []string{".id", "name"},
		streamKey: func(row routeros.Reply) string { return row[".id"] },
	}
	s.begin()
	defer s.end()

	every = 7 * time.Second
	s.retune()

	if n := r.count(); n != 2 {
		t.Fatalf("%d channel(s) opened across a retune, want 2 (the old one closed, a new one opened)", n)
	}
	r.mu.Lock()
	last := r.args[len(r.args)-1]
	r.mu.Unlock()
	if !strings.Contains(last, "=interval=7") {
		t.Errorf("the reopened stream's arguments are %q; it is still at the old interval", last)
	}
	if got := streamed(t, c); len(got) != 1 {
		t.Errorf("streamed menus after a retune = %v, want exactly the one", got)
	}

	// A retune of a suspended collector opens nothing.
	s.end()
	s.retune()
	if n := r.count(); n != 2 {
		t.Errorf("a retune after end() opened a channel (%d opens)", n)
	}
}

// TestACollectorWithNoKeyTakesTheDefault.
//
// ── THIS TEST USED TO ASSERT THE OPPOSITE, AND B.3 IS WHY ──────────────────
//
// It was `TestAKeylessCollectorWillNotStream`: a collector that had not said how
// its rows are identified stayed polled, because a rolling map with no key holds
// one row.
//
// B.3 gave `scheduled` a DEFAULT key — RouterOS `.id`, which every `/print` row
// carries and which is stable across re-prints. That is what makes B.4 one line
// per collector instead of an edit to each, so the default is the feature rather
// than a convenience.
//
// The old assertion is not merely retired: `roscache` still refuses a nil key,
// and `FillFromStream` still counts rows it cannot name, so a menu whose rows
// have no `.id` is discoverable rather than silent. What changed is that nobody
// reaches that path by simply not having said anything.
func TestACollectorWithNoKeyTakesTheDefault(t *testing.T) {
	r := &streamReader{}
	c := roscache.New(r)
	c.StreamWhen(func(string) bool { return true })
	s := scheduled{cache: c, menu: "/ip/dns/print",
		cadence: func() time.Duration { return time.Second },
	}
	s.begin()
	defer s.end()

	if n := r.count(); n != 1 {
		t.Errorf("%d channel(s) opened; a collector with no key of its own should take "+
			"the `.id` default, which is what makes B.4 one line per collector", n)
	}
	if len(c.Demand()) != 1 {
		t.Error("the collector lost its subscription; delivery is the only thing that changes")
	}
}

// TestTheDefaultStreamArgsMatchThePolledRead.
//
// The operator's rule is that mode switches DELIVERY ONLY and never touches
// intervals. A stream opened without arguments would push at whatever rate the
// router chose, which is a silent change of the setting — so the interval is the
// subscription's own cadence and the proplist is the subscription's own fields.
func TestTheDefaultStreamArgsMatchThePolledRead(t *testing.T) {
	args := defaultStreamArgs(func() time.Duration { return 5 * time.Second },
		[]string{"name", "running"})
	got := strings.Join(args, " ")
	if !strings.Contains(got, "=interval=5") {
		t.Errorf("args = %q; the interval must be the collector's own cadence, or "+
			"choosing Stream silently changes the poll setting", got)
	}
	if !strings.Contains(got, "=.proplist=name,running") {
		t.Errorf("args = %q; a streamed menu must ask for exactly what the polled "+
			"read asked for", got)
	}

	// SUB-SECOND FLOORS TO ONE. RouterOS takes seconds, and rounding to zero
	// would ask for a stream with no interval at all.
	if got := strings.Join(defaultStreamArgs(
		func() time.Duration { return 200 * time.Millisecond }, nil), " "); got != "=interval=1" {
		t.Errorf("a sub-second cadence produced %q, want =interval=1", got)
	}
}

// TestARefusedStreamFallsBackToPolling. A router that will not stream a menu
// must leave a working polled collector, not an error on a page that is
// rendering correctly.
func TestARefusedStreamFallsBackToPolling(t *testing.T) {
	r := &streamReader{refuse: true}
	c := roscache.New(r)
	c.StreamWhen(func(string) bool { return true })
	s := scheduled{cache: c, menu: "/ip/dns/print",
		cadence:   func() time.Duration { return time.Second },
		streamKey: func(row routeros.Reply) string { return row[".id"] },
	}
	s.begin()
	defer s.end()

	if got := streamed(t, c); len(got) != 0 {
		t.Errorf("a refused stream left %v registered", got)
	}
	if len(c.Demand()) != 1 {
		t.Fatal("a refused stream cost the collector its subscription")
	}
}

// TestResubscribeMovesTheChannelWithTheMenu. Mechanism B moves menus routinely —
// `firewall` follows the table on screen, `wifi`/`wireless` follow whichever
// stack the router has. A fill is per-menu, so a move that left the old channel
// open would leak it AND, because a menu cannot be filled twice, would stop the
// collector ever streaming that menu again if it moved back.
func TestResubscribeMovesTheChannelWithTheMenu(t *testing.T) {
	r := &streamReader{}
	c := roscache.New(r)
	c.StreamWhen(func(string) bool { return true })
	s := scheduled{cache: c, menu: "/ip/firewall/filter/print",
		cadence:   func() time.Duration { return time.Second },
		apply:     func([]routeros.Reply, error) {},
		streamKey: func(row routeros.Reply) string { return row[".id"] },
	}
	s.begin()
	defer s.end()

	if got := streamed(t, c); len(got) != 1 || got[0] != "/ip/firewall/filter/print" {
		t.Fatalf("streamed = %v after begin", got)
	}

	s.resubscribe("/ip/firewall/nat/print", func([]routeros.Reply, error) {})

	got := streamed(t, c)
	if len(got) != 1 || got[0] != "/ip/firewall/nat/print" {
		t.Errorf("streamed = %v after resubscribe, want only the new menu — the old "+
			"channel leaked, and the old menu can now never be filled again", got)
	}
	// AND BACK, which is the case the leak would have broken permanently.
	s.resubscribe("/ip/firewall/filter/print", func([]routeros.Reply, error) {})
	if got := streamed(t, c); len(got) != 1 || got[0] != "/ip/firewall/filter/print" {
		t.Errorf("streamed = %v after moving back; the first menu could not be "+
			"re-filled, which is what a leaked fill does", got)
	}
}

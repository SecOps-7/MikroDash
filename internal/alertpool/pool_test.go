package alertpool

import (
	"errors"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// A dialer under the test's control. Everything the connect loop does is
// reachable through it, which is the only way this half gets covered without a
// router.
type fakeDial struct {
	mu       sync.Mutex
	dials    int
	fail     bool
	conns    []*fakeConn
	dialedAs []routeros.Config
}

func (f *fakeDial) dial(cfg routeros.Config) (Conn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dials++
	f.dialedAs = append(f.dialedAs, cfg)
	if f.fail {
		return nil, errors.New("refused")
	}
	c := &fakeConn{up: true}
	f.conns = append(f.conns, c)
	return c, nil
}

func (f *fakeDial) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.dials }

type fakeConn struct {
	mu     sync.Mutex
	up     bool
	closed bool
}

func (c *fakeConn) Do(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }
func (c *fakeConn) Stream(routeros.Cmd, func(routeros.Reply)) (func(), error) {
	return func() {}, nil
}
func (c *fakeConn) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.up
}
func (c *fakeConn) Close() error { c.mu.Lock(); c.closed = true; c.mu.Unlock(); return nil }
func (c *fakeConn) drop()        { c.mu.Lock(); c.up = false; c.mu.Unlock() }

type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) hook(id string, up bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, id+"="+map[bool]string{true: "up", false: "down"}[up])
}
func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string{}, r.seen...)
}

func waitFor(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", why)
}

// ── THE POINT OF THE WHOLE PACKAGE ────────────────────────────────────────
//
// A router nobody is watching gets a connection, and its status is reported.
// Before this package existed, cAP AX and hAP AC2 read Offline until somebody
// opened the Devices page — which is what the operator reported.
func TestARouterNobodyIsWatchingGetsAConnection(t *testing.T) {
	d := &fakeDial{}
	rec := &recorder{}
	p := New(d.dial, 10*time.Millisecond, rec.hook, nil, nil)
	defer p.Close()

	p.Sync([]Router{{ID: "a", Label: "Alpha", Host: "198.51.100.1"}}, "", nil)

	waitFor(t, "the dial", func() bool { return d.count() >= 1 })
	waitFor(t, "the status hook", func() bool { return len(rec.all()) >= 1 })

	if got := rec.all()[0]; got != "a=up" {
		t.Errorf("first status %q, want a=up", got)
	}
	if st := p.Status(); !st["a"] {
		t.Errorf("Status() = %v, want a online", st)
	}
}

// A DROP IS NOTICED AND RE-DIALLED. Without this the pool reports a dead router
// as Online for ever, which is worse than reporting it offline.
func TestADropIsNoticedAndRedialled(t *testing.T) {
	d := &fakeDial{}
	rec := &recorder{}
	p := New(d.dial, 10*time.Millisecond, rec.hook, nil, nil)
	defer p.Close()

	p.Sync([]Router{{ID: "a", Host: "198.51.100.1"}}, "", nil)
	waitFor(t, "the first connect", func() bool { return len(rec.all()) >= 1 })

	d.mu.Lock()
	first := d.conns[0]
	d.mu.Unlock()
	first.drop()

	waitFor(t, "the down report", func() bool {
		for _, s := range rec.all() {
			if s == "a=down" {
				return true
			}
		}
		return false
	})
	waitFor(t, "the re-dial", func() bool { return d.count() >= 2 })
}

// ── TRANSITION-ONLY, AND THE RETRY IS WHY IT MATTERS ──────────────────────
//
// An unreachable router re-dials every retry interval. A hook that fired each
// time would push a router:status frame to every browser on every attempt.
func TestAnUnreachableRouterReportsDownOnce(t *testing.T) {
	d := &fakeDial{fail: true}
	rec := &recorder{}
	p := New(d.dial, 5*time.Millisecond, rec.hook, nil, nil)
	defer p.Close()

	p.Sync([]Router{{ID: "a", Host: "198.51.100.1"}}, "", nil)
	waitFor(t, "several dial attempts", func() bool { return d.count() >= 4 })

	n := 0
	for _, s := range rec.all() {
		if s == "a=down" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("reported down %d times across %d dial attempts, want 1 — a status "+
			"frame per retry reaches every browser", n, d.count())
	}
}

// A router that is down when the pool starts must still be REPORTED down: false
// is Go's zero value, so "no entry" and "known offline" must be distinguishable.
func TestAnInitiallyDownRouterIsReported(t *testing.T) {
	d := &fakeDial{fail: true}
	rec := &recorder{}
	p := New(d.dial, 5*time.Millisecond, rec.hook, nil, nil)
	defer p.Close()

	p.Sync([]Router{{ID: "a", Host: "198.51.100.1"}}, "", nil)
	waitFor(t, "the down report", func() bool { return len(rec.all()) >= 1 })
	if got := rec.all()[0]; got != "a=down" {
		t.Errorf("first status %q, want a=down", got)
	}
}

// The active router is not connected to. Its interactive session already holds
// it, and a second connection is the cost this pool exists to avoid.
func TestTheActiveRouterIsNotDialled(t *testing.T) {
	d := &fakeDial{}
	p := New(d.dial, 10*time.Millisecond, nil, nil, nil)
	defer p.Close()

	p.Sync([]Router{{ID: "a", Host: "198.51.100.1"}}, "a", nil)
	time.Sleep(60 * time.Millisecond)
	if d.count() != 0 {
		t.Errorf("dialled %d time(s) for the active router", d.count())
	}
}

// Sync is what tears a session down, and the socket must actually close.
func TestRemovingARouterClosesItsConnection(t *testing.T) {
	d := &fakeDial{}
	p := New(d.dial, 10*time.Millisecond, nil, nil, nil)
	defer p.Close()

	p.Sync([]Router{{ID: "a", Host: "198.51.100.1"}}, "", nil)
	waitFor(t, "the connect", func() bool { return d.count() >= 1 })

	d.mu.Lock()
	c := d.conns[0]
	d.mu.Unlock()

	p.Sync(nil, "", nil)

	// ── CLOSED BY THE TIME Sync RETURNS, NOT EVENTUALLY ───────────────────
	//
	// `teardown` is synchronous, so there is nothing to wait for. Waiting here
	// let a mutant survive that deleted teardown's `Close` entirely: the connect
	// loop ALSO closes the socket when it notices `stop`, so the connection did
	// eventually shut — up to a second later, at the next `Connected` poll, with
	// Sync long since returned. On a settings change that re-syncs the fleet
	// that is a second of doubled connections per router.
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if !closed {
		t.Error("Sync returned with the socket still open — teardown did not close it, " +
			"and the connect loop's own close is up to a Connected poll away")
	}

	// ...and it forgets the status, so a re-add cannot inherit a stale Online.
	if _, still := p.Status()["a"]; still {
		t.Error("a dropped router kept its status; a later re-add would start from it")
	}
}

// THE DIALLED CONFIG IS THE ROUTER'S. A pool that dialled the right host with
// the wrong TLS setting would connect to nothing and report every router down.
func TestTheDialUsesTheRoutersOwnSettings(t *testing.T) {
	d := &fakeDial{}
	p := New(d.dial, 10*time.Millisecond, nil, nil, nil)
	defer p.Close()

	p.Sync([]Router{{
		ID: "a", Host: "198.51.100.9", Port: 8729, TLS: true, InsecureTLS: true,
		Username: "svc", Password: "s3cret",
	}}, "", nil)
	waitFor(t, "the dial", func() bool { return d.count() >= 1 })

	d.mu.Lock()
	got := d.dialedAs[0]
	d.mu.Unlock()
	if got.Host != "198.51.100.9" || got.Port != 8729 || !got.TLS || !got.InsecureTLS ||
		got.Username != "svc" || got.Password != "s3cret" {
		t.Errorf("dialled %+v — not the router's own settings", got)
	}
}

// ── THE GAP THIS PACKAGE CLOSES, ASSERTED DIRECTLY ────────────────────────
//
// Before it, `alertwire.Evaluate` was reached from ONE place — the emit closure
// in `session.go` — so with `-alert-dispatch` on, alerts fired only for the
// router on screen. An operator would believe the fleet was covered.
func TestAnAlertsEnabledRouterFeedsTheEvaluator(t *testing.T) {
	d := &fakeDial{}
	var mu sync.Mutex
	seen := map[string]int{}
	on := func(r Router, event string, _ any) {
		mu.Lock()
		seen[r.ID+":"+event]++
		mu.Unlock()
	}
	p := New(d.dial, 10*time.Millisecond, nil, on, nil)
	defer p.Close()

	p.Sync([]Router{{ID: "a", Label: "Alpha", Host: "198.51.100.1", AlertsEnabled: true}}, "", nil)

	waitFor(t, "an evaluator event", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) > 0
	})
}

// ── AND A STATUS-ONLY ROUTER RUNS NOTHING ─────────────────────────────────
//
// `alertsEnabled` decides whether a session has COLLECTORS, not whether it
// exists: "a status-only session needs no collectors since the ROS connection
// events alone provide Online/Offline state". A pool that ran six collectors per
// router regardless would put the whole fleet's polling on hardware whose
// documented limit is concurrent API channels — the cost this split exists to
// avoid.
func TestAStatusOnlyRouterRunsNoCollectors(t *testing.T) {
	d := &fakeDial{}
	var mu sync.Mutex
	events := 0
	on := func(Router, string, any) { mu.Lock(); events++; mu.Unlock() }
	rec := &recorder{}
	p := New(d.dial, 10*time.Millisecond, rec.hook, on, nil)
	defer p.Close()

	// alertsEnabled is false — the default.
	p.Sync([]Router{{ID: "a", Host: "198.51.100.1"}}, "", nil)

	// It still connects and still reports status: that is the whole point.
	waitFor(t, "the status report", func() bool { return len(rec.all()) >= 1 })
	time.Sleep(120 * time.Millisecond)

	mu.Lock()
	n := events
	mu.Unlock()
	if n != 0 {
		t.Errorf("a status-only session emitted %d collector event(s) — it should run none", n)
	}
}

// The collectors stop when the socket drops, so a poll does not run against a
// dead connection and log an error for something that is not a fault.
func TestCollectorsStopWhenTheConnectionDrops(t *testing.T) {
	d := &fakeDial{}
	var mu sync.Mutex
	after := 0
	dropped := false
	on := func(Router, string, any) {
		mu.Lock()
		if dropped {
			after++
		}
		mu.Unlock()
	}
	p := New(d.dial, time.Hour, nil, on, nil) // no re-dial during the test
	defer p.Close()

	p.Sync([]Router{{ID: "a", Host: "198.51.100.1", AlertsEnabled: true}}, "", nil)
	waitFor(t, "the connect", func() bool { return d.count() >= 1 })

	d.mu.Lock()
	c := d.conns[0]
	d.mu.Unlock()

	mu.Lock()
	dropped = true
	mu.Unlock()
	c.drop()

	// Well past the 1s watch tick, so the loop has certainly noticed.
	time.Sleep(1300 * time.Millisecond)
	mu.Lock()
	n := after
	mu.Unlock()
	if n > 2 {
		t.Errorf("%d collector events after the drop — the collectors kept polling a dead "+
			"connection", n)
	}
}

// A ROUTER IS DIALLED ONCE PER SYNC, NOT TWICE.
//
// ── THE LEAK THIS CLOSES ──────────────────────────────────────────────────
//
// `Sync` constructs one session per entry in `Build` then `Rebuild` and stores
// each under `p.sessions[id]`. A router in BOTH lists gets two sessions: the
// second overwrites the first in the map and the first is LEAKED — still
// dialled, still collecting, tracked by nothing that can stop it.
//
// It happened on every start. The first `SetHistoryRouter` always moves the
// history target from "" to the active router and marks it for rebuild, and the
// first `Sync` is also the one that BUILDS it. Measured on the running process:
// two established sockets to the active router, one each to the other two.
//
// FOUND BY A LOG LINE — "Mikrotik hAP AX3 connected" twice in the same second —
// and NOT by the first test written for it, which re-implemented the merge
// inline instead of calling `Sync`. That test passed with the bug restored. This
// one drives the real pool and counts dials, which is the only thing the defect
// could not fake.
func TestTheHistoryRouterIsDialledOnlyOnce(t *testing.T) {
	d := &fakeDial{}
	p := New(d.dial, time.Hour, nil, nil, nil) // no retry inside the test window
	defer p.Close()
	p.WithHistory(func(string, string, any) {})

	// Exactly the order `syncAlertPool` uses on a cold start.
	p.SetHistoryRouter("active")
	p.Sync([]Router{
		{ID: "active", Label: "Active", Host: "198.51.100.1", AlertsEnabled: true},
		{ID: "other", Label: "Other", Host: "198.51.100.2", AlertsEnabled: true},
	}, "", nil)

	waitFor(t, "both routers dialled", func() bool { return d.count() >= 2 })
	// Give a duplicate session time to dial, so this fails on the bug rather
	// than racing past it.
	time.Sleep(150 * time.Millisecond)

	byHost := map[string]int{}
	d.mu.Lock()
	for _, c := range d.dialedAs {
		byHost[c.Host]++
	}
	d.mu.Unlock()

	if n := byHost["198.51.100.1"]; n != 1 {
		t.Errorf("the history router was dialled %d times, want 1 — a router in both "+
			"Build and Rebuild gets two sessions and the first is leaked, holding a "+
			"second connection open for the life of the process", n)
	}
	if n := byHost["198.51.100.2"]; n != 1 {
		t.Errorf("the other router was dialled %d times, want 1", n)
	}
}

// ── THE DEVICES PAGE'S FIRST PAINT DEPENDS ON THIS ────────────────────────
//
// `Snapshots` is what lets the Devices page show real state the moment it opens,
// instead of a fleet of red "Offline" cards waiting on the overview pool to
// dial. Two properties, and the second is the one that is easy to lose:
//
//  1. A router this pool has an opinion about is reported, with that opinion.
//  2. A router it does NOT yet have an opinion about is OMITTED, not reported
//     as down. Emitting `Connected: false` for it would push the same
//     zero-value-as-observation bug down one layer.
func TestSnapshotsReportOnlyWhatThePoolHasObserved(t *testing.T) {
	d := &fakeDial{}
	rec := &recorder{}
	p := New(d.dial, 10*time.Millisecond, rec.hook, nil, nil)
	defer p.Close()

	// No sessions at all: nothing to report, and no panic on the empty maps.
	if got := p.Snapshots(); len(got) != 0 {
		t.Fatalf("Snapshots() = %v on an unsynced pool, want none", got)
	}

	p.Sync([]Router{{ID: "a", Label: "Alpha", Host: "198.51.100.1"}}, "", nil)
	waitFor(t, "the first observation", func() bool { return len(p.Snapshots()) == 1 })

	got := p.Snapshots()
	if got[0].RouterID != "a" || !got[0].Connected {
		t.Errorf("Snapshots() = %+v, want a connected", got[0])
	}

	// A SESSION WITH NO OBSERVATION IS NOT A SNAPSHOT. Built directly rather
	// than raced into existence, because the window between "session exists" and
	// "first dial returned" is exactly what this guards and is too short to hit
	// reliably from the outside.
	// `stop` is made because `teardown` closes it; the deferred `Close` reaches
	// every session in the map, this one included.
	p.mu.Lock()
	p.sessions["pending"] = &poolSession{
		r: Router{ID: "pending"}, stop: make(chan struct{}),
	}
	p.mu.Unlock()

	for _, s := range p.Snapshots() {
		if s.RouterID == "pending" {
			t.Error("a session whose first dial has not returned was reported; " +
				"the caller cannot tell that from a router that is genuinely down")
		}
	}
}

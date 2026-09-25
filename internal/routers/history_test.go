package routers

import (
	"sync"
	"testing"
	"time"
)

// hcfg is `cfg` with a DISTINCT host, so `dialLog.conn` can tell the routers
// apart — the shared helper gives every router the same one, which is fine for
// tests that only count sessions and useless for one that asks which router
// opened a stream.
func hcfg(id string) RouterConfig {
	c := cfg(id)
	c.Host = "198.51.100." + map[string]string{"a": "1", "b": "2"}[id]
	c.PingTarget = "198.51.100.254"
	c.DefaultIf = "ether1"
	return c
}

// rec is hcfg with reporting on.
//
// These tests used to call `p.SetHistoryRouter(id)` — one router recorded, and
// it was whichever one was active. Recording is each router's own setting now,
// so the choice travels in the config the pool is synced with. Every property
// below is unchanged; only how the choice arrives is.
func rec(id string) RouterConfig {
	c := hcfg(id)
	c.ReportingEnabled = true
	return c
}

// recorder captures what the pool sent to the history wire.
type recorder struct {
	mu   sync.Mutex
	seen map[string]map[string]int // routerID -> event -> count
}

func newRecorder() *recorder { return &recorder{seen: map[string]map[string]int{}} }

func (r *recorder) rec(routerID, event string, _ any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen[routerID] == nil {
		r.seen[routerID] = map[string]int{}
	}
	r.seen[routerID][event]++
}

func (r *recorder) routers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seen))
	for id := range r.seen {
		out = append(out, id)
	}
	return out
}

// ONLY THE HISTORY ROUTER OPENS THE HISTORY STREAMS.
//
// ── THE PROPERTY THIS EXISTS FOR ──────────────────────────────────────────
//
// Continuous history was added because the port recorded only while a browser
// was open — measured at 5-44 traffic rows an hour against live's steady 60. The
// fix must not become "every pooled router now streams traffic and ping", which
// would multiply the cost by the size of the fleet and break CLAUDE.md's rule
// that efficiency means FEWER router channels.
//
// So the assertion is about the OTHER routers as much as the chosen one: `b`
// must open no stream at all.
func TestOnlyTheHistoryRouterRunsTheHistoryCollectors(t *testing.T) {
	d := &dialLog{}
	r := newRecorder()
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil).WithHistory(r.rec)
	defer p.Close()

	p.Sync([]RouterConfig{rec("a"), hcfg("b")}, nil)
	waitFor(t, "both connected", func() bool { return len(p.Summaries()) == 2 })

	waitFor(t, "the chosen router opened a ping stream", func() bool {
		return d.conn("198.51.100.1") != nil && d.conn("198.51.100.1").sawStream("/tool/ping")
	})
	if c := d.conn("198.51.100.2"); c != nil && c.sawStream("/tool/ping") {
		t.Error("a router that is NOT the history router opened a ping stream. " +
			"Every pooled router streaming is the cost this design exists to avoid.")
	}
}

// A POOL WITH NO RECORDER STREAMS NOTHING — the default, and every caller that
// existed before continuous history.
func TestAPoolWithoutARecorderRunsNoHistoryCollectors(t *testing.T) {
	d := &dialLog{}
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil) // no WithHistory
	defer p.Close()

	p.Sync([]RouterConfig{rec("a")}, nil)
	waitFor(t, "connected", func() bool { return len(p.Summaries()) == 1 })
	time.Sleep(80 * time.Millisecond)

	if c := d.conn("198.51.100.1"); c != nil && c.sawStream("/tool/ping") {
		t.Error("a pool with no history recorder opened a ping stream")
	}
}

// A TOGGLED FLAG TAKES EFFECT ON SESSIONS THAT ALREADY EXIST.
//
// `Sync` does not rebuild a session it already has, so a pool built while `a`
// was recording would go on recording `a` for ever — the toggle would appear to
// save and do nothing until something unrelated rebuilt the session. This is the
// case that separates "read the flag once" from "react to it changing", and it
// is why `applyReporting` runs on every Sync.
func TestAToggledReportingFlagTakesEffect(t *testing.T) {
	d := &dialLog{}
	r := newRecorder()
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil).WithHistory(r.rec)
	defer p.Close()

	p.Sync([]RouterConfig{rec("a"), hcfg("b")}, nil)
	waitFor(t, "both connected", func() bool { return len(p.Summaries()) == 2 })
	waitFor(t, "a is streaming", func() bool {
		return d.conn("198.51.100.1") != nil && d.conn("198.51.100.1").sawStream("/tool/ping")
	})

	// The operator moves reporting from a to b. A re-Sync with the new flags is
	// exactly what `routerUpdate` produces.
	p.Sync([]RouterConfig{hcfg("a"), rec("b")}, nil)
	waitFor(t, "b took over", func() bool {
		return d.conn("198.51.100.2") != nil && d.conn("198.51.100.2").sawStream("/tool/ping")
	})

	// AND `a` STOPPED. This is the half that matters: without it "b records
	// too" passes, and the fleet ends up streaming from every router that was
	// ever active. A mutation setting `historyOn = true` for every session
	// survived every other assertion in this file.
	waitFor(t, "a stopped recording", func() bool {
		return d.conn("198.51.100.1").streamStopped("/tool/ping")
	})
}

// AND IT IS IDEMPOTENT: re-syncing an unchanged fleet restarts nothing. `Sync`
// runs on every routers change, so a stream restarted each time would be a
// stream permanently restarting.
func TestReSyncingAnUnchangedFleetIsANoOp(t *testing.T) {
	d := &dialLog{}
	r := newRecorder()
	p := NewPool(d.dial, 10*time.Millisecond, nil, nil).WithHistory(r.rec)
	defer p.Close()

	p.Sync([]RouterConfig{rec("a")}, nil)
	waitFor(t, "connected", func() bool { return len(p.Summaries()) == 1 })
	waitFor(t, "streaming", func() bool {
		return d.conn("198.51.100.1") != nil && d.conn("198.51.100.1").sawStream("/tool/ping")
	})
	before := len(d.conn("198.51.100.1").streams)
	p.Sync([]RouterConfig{rec("a")}, nil)
	time.Sleep(50 * time.Millisecond)
	if after := len(d.conn("198.51.100.1").streams); after > before+1 {
		t.Errorf("re-syncing an unchanged fleet opened %d more streams; it must be a no-op",
			after-before)
	}
}

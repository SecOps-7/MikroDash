package collect

import (
	"sync"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// The traffic stream's silent-death watchdog.
//
// ── THE FAILURE IT EXISTS FOR ───────────────────────────────────────────────
//
// `/interface/monitor-traffic` pushes a row a second and nothing acknowledges
// it. A router that stops sending leaves an open connection, a client reporting
// Connected, and a chart that has simply stopped. Nothing errors, so nothing
// retries. Reported on issue #126 as a router that "disconnects" and returns
// only when the device is deleted and re-added.
//
// These tests drive the tick directly rather than waiting on the real 5s timer:
// the point is the DECISION, and a test that slept would be slow and flaky
// without proving anything more.

// wdReader is a connected router whose stream can be counted and silenced.
type wdReader struct {
	mu      sync.Mutex
	opens   int
	stops   int
	handler func(routeros.Reply)
	conn    bool
}

func (r *wdReader) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

func (r *wdReader) Do(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }

func (r *wdReader) Stream(_ routeros.Cmd, fn func(routeros.Reply)) (func(), error) {
	r.mu.Lock()
	r.opens++
	r.handler = fn
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.stops++
		r.mu.Unlock()
	}, nil
}

func (r *wdReader) openCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opens
}

// deliver pushes one row through whatever handler the current stream installed.
func (r *wdReader) deliver() {
	r.mu.Lock()
	fn := r.handler
	r.mu.Unlock()
	if fn != nil {
		fn(routeros.Reply{"name": "ether1", "rx-bits-per-second": "1000",
			"tx-bits-per-second": "1000", "running": "true"})
	}
}

func wdTraffic(t *testing.T) (*wdReader, *Traffic, *[]map[string]any) {
	t.Helper()
	r := &wdReader{conn: true}
	var mu sync.Mutex
	health := []map[string]any{}
	emit := func(_, event string, payload any) {
		if event != "stream:health" {
			return
		}
		mu.Lock()
		health = append(health, payload.(map[string]any))
		mu.Unlock()
	}
	tr := NewTraffic(r, emit, "ether1", 1)
	// A tick this test drives by hand; the timer must not also fire.
	tr.wdEvery = time.Hour
	tr.wdStaleMs = 10_000
	t.Cleanup(tr.Stop)
	return r, tr, &health
}

// TestAStalledStreamIsRestarted is the bug.
func TestAStalledStreamIsRestarted(t *testing.T) {
	r, tr, _ := wdTraffic(t)
	tr.Start()
	if r.openCount() != 1 {
		t.Fatalf("Start opened %d streams", r.openCount())
	}
	r.deliver()

	// A healthy tick changes nothing.
	tr.watchdogTick()
	if r.openCount() != 1 {
		t.Fatalf("a healthy stream was restarted (%d opens)", r.openCount())
	}

	// Now the router goes quiet: age the last reading past the threshold.
	tr.mu.Lock()
	tr.lastData = time.Now().UnixMilli() - 30_000
	tr.streamStart = tr.lastData
	tr.mu.Unlock()

	tr.watchdogTick()
	if r.openCount() != 2 {
		t.Errorf("a stream silent for 30s was not restarted (%d opens) — the chart "+
			"stops and nothing ever retries", r.openCount())
	}
}

// TestAStreamThatFailedToOpenIsRetried. `syncStream` gives up silently when
// Stream returns an error, so without the watchdog a single failed open left the
// collector with no stream and nothing to start one.
func TestAStreamThatFailedToOpenIsRetried(t *testing.T) {
	r, tr, _ := wdTraffic(t)
	tr.Start()
	tr.stopStream() // as a failed open leaves it: no stream, watchdog still on
	if r.openCount() != 1 {
		t.Fatalf("%d opens before the retry", r.openCount())
	}
	tr.watchdogTick()
	if r.openCount() != 2 {
		t.Errorf("the watchdog did not reopen a missing stream (%d opens)", r.openCount())
	}
}

// TestADisconnectedRouterIsLeftAlone — `connectLoop` owns reconnection, and
// restarting a stream on a dead client would fail on every tick for ever.
func TestADisconnectedRouterIsLeftAlone(t *testing.T) {
	r, tr, _ := wdTraffic(t)
	tr.Start()
	// BOTH clocks aged, or the tick is not looking at a stale stream at all:
	// the watchdog compares against whichever of the two is LATER, so a fresh
	// `streamStart` alone keeps it healthy. Ageing only `lastData` made this
	// case pass against a watchdog with its connected guard removed.
	tr.mu.Lock()
	tr.lastData = time.Now().UnixMilli() - 30_000
	tr.streamStart = tr.lastData
	tr.mu.Unlock()

	r.mu.Lock()
	r.conn = false
	r.mu.Unlock()

	tr.watchdogTick()
	if r.openCount() != 1 {
		t.Errorf("the watchdog acted on a disconnected router (%d opens)", r.openCount())
	}
	// ── AND THE STREAM WAS NOT TORN DOWN EITHER ─────────────────────────────
	//
	// Counting opens alone is VACUOUS here: `syncStream` has its own connected
	// check, so a watchdog that skipped the guard would still fail to reopen and
	// the open count would look identical. What it WOULD do is close the stream
	// first, leaving the collector with nothing — so the stop count is the
	// assertion that separates the two. A mutation removing the guard survived
	// until this existed.
	r.mu.Lock()
	stops := r.stops
	r.mu.Unlock()
	if stops != 0 {
		t.Errorf("the stream was closed on a disconnected router (%d stops); the "+
			"watchdog tore it down and could not reopen it", stops)
	}
	tr.mu.Lock()
	running := tr.stop != nil
	tr.mu.Unlock()
	if !running {
		t.Error("the collector was left with no stream after a tick on a " +
			"disconnected router")
	}
}

// TestSuspendStopsTheWatchdog. `Suspend` is `Stop`, so a watchdog left running
// would find no stream and open one every five seconds — a suspended collector
// resurrecting its own stream for ever.
func TestSuspendStopsTheWatchdog(t *testing.T) {
	r, tr, _ := wdTraffic(t)
	tr.Start()
	tr.Suspend()
	before := r.openCount()
	tr.watchdogTick() // the timer is stopped, but prove the decision too
	if r.openCount() != before+1 {
		t.Logf("note: a bare tick after Suspend reopened nothing")
	}
	// The real assertion: the loop itself is stopped, so nothing will call it.
	tr.wd.mu.Lock()
	stopped := tr.wd.stopped
	tr.wd.mu.Unlock()
	if !stopped {
		t.Error("Suspend left the watchdog timer running, so the collector will " +
			"reopen its own stream every tick while suspended")
	}
}

// TestThreeRestartsReportDegraded — the end-to-end path to the warning the
// Dashboard has always been able to render and never received.
func TestThreeRestartsReportDegraded(t *testing.T) {
	r, tr, health := wdTraffic(t)
	tr.Start()

	for i := 0; i < 3; i++ {
		tr.mu.Lock()
		tr.lastData = time.Now().UnixMilli() - 30_000
		tr.streamStart = tr.lastData
		tr.mu.Unlock()
		tr.watchdogTick()
	}
	if r.openCount() != 4 {
		t.Fatalf("%d opens after three stalls", r.openCount())
	}
	if len(*health) != 1 {
		t.Fatalf("stream:health sent %d times, want exactly one transition: %v",
			len(*health), *health)
	}
	h := (*health)[0]
	if h["collector"] != "traffic" || h["degraded"] != true || h["restarts"] != 3 {
		t.Errorf("stream:health payload = %v", h)
	}
}

// TestReconnectResetsTheCount. Three restarts spread over three separate
// outages must not add up to a degraded stream that is working.
func TestReconnectResetsTheCount(t *testing.T) {
	_, tr, _ := wdTraffic(t)
	tr.Start()
	tr.mu.Lock()
	tr.health.RecordRestart(1)
	tr.health.RecordRestart(2)
	tr.mu.Unlock()

	tr.Reconnected()

	tr.mu.Lock()
	n := tr.health.Restarts()
	tr.mu.Unlock()
	if n != 0 {
		t.Errorf("Restarts = %d after a reconnect", n)
	}
}

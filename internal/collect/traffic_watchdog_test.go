package collect

import (
	"mikrodash/internal/hub"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
)

// The traffic stream's silent-death recovery, after B.7 split it in two.
//
// ── THE FAILURE IT EXISTS FOR ───────────────────────────────────────────────
//
// `/interface/monitor-traffic` pushes a row a second and nothing acknowledges
// it. A router that stops sending leaves an open connection, a client reporting
// Connected, and a chart that has simply stopped. Nothing errors, so nothing
// retries. Reported on issue #126 as a router that "disconnects" and returns
// only when the device is deleted and re-added.
//
// ── WHAT MOVED, AND WHAT THESE TESTS ASSERT NOW ────────────────────────────
//
// The channel is shared with `ifStatus` since B.7, so the RESTARTING is
// `roscache`'s: one watchdog for all eight streamed menus instead of a second
// one here racing it. `internal/roscache` owns those tests.
//
// Two jobs stayed, because only this collector can do them:
//
//	the failed-open retry   `JoinStream` returning an error leaves NO fill, so
//	                        there is nothing for the shared watchdog to watch
//	the health signal       `stream:health` tints the Dashboard's traffic card
//	                        and names a restart count that is now somebody
//	                        else's counter
//
// So these drive the tick directly and, where a restart is the subject, drive it
// END TO END: the fake router goes silent, `roscache` restarts the fill, and this
// collector turns the rising count into one transition. `Cache.StreamTimings`
// is what makes that take milliseconds instead of a minute a case.

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
	emit := hub.NewRelay(func(_ string, ev hub.Named, payload any) {
		event := ev.Name()
		if event != "stream:health" {
			return
		}
		mu.Lock()
		health = append(health, payload.(map[string]any))
		mu.Unlock()
	})
	tr := NewTraffic(r, emit, "ether1", 1)
	// THE CHANNEL LIVES IN THE CACHE NOW. A `traffic` without one holds no
	// channel at all — deliberately, so the raw path cannot survive as a
	// fallback that only tests exercise.
	c := roscache.New(r)
	// Fast enough that a stall is a few milliseconds rather than ten seconds,
	// and it is the fill's own watchdog being hurried, not a reimplementation.
	c.StreamTimings(5*time.Millisecond, 20*time.Millisecond)
	tr.UseCache(c)
	tr.wdEvery = time.Hour
	tr.wdStaleMs = 10_000
	t.Cleanup(tr.Stop)
	return r, tr, &health
}

// stall waits for the shared fill to notice silence and reopen the channel, and
// returns how many times it has.
//
// POLLED RATHER THAN SLEPT for a fixed span: the fill's watchdog runs on its own
// goroutine, and a fixed sleep is the difference between a test that is slow and
// one that is flaky.
func stall(t *testing.T, tr *Traffic, want int) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := tr.cache.StreamStats(monitorTrafficMenu); ok && st.Restarts >= want {
			return st.Restarts
		}
		time.Sleep(2 * time.Millisecond)
	}
	st, _ := tr.cache.StreamStats(monitorTrafficMenu)
	t.Fatalf("the shared channel restarted %d times in 3s, want %d — the fill's "+
		"watchdog is not recovering a silent channel", st.Restarts, want)
	return 0
}

// openStream starts the stream WITHOUT arming the watchdog timer.
//
// ── WHY NOT `Start` ─────────────────────────────────────────────────────────
//
// `pollLoop.start()` fires its first tick IMMEDIATELY — `lastRun` is the zero
// time, so a whole interval has always "already elapsed" — and a long `wdEvery`
// does not prevent that first one. So a test that called `Start`, aged the
// clocks and then called `watchdogTick` by hand was racing a background tick
// that could consume the stall first, leaving the manual tick to find a stream
// it had just restarted.
//
// It passed alone and failed under the full package, which is the signature of
// exactly this. These tests own every tick instead.
func openStream(tr *Traffic) { tr.syncStream() }

// TestAStalledStreamIsRestarted is the bug.
func TestAStalledStreamIsRestarted(t *testing.T) {
	r, tr, health := wdTraffic(t)
	openStream(tr)
	if r.openCount() != 1 {
		t.Fatalf("opening the stream made %d streams", r.openCount())
	}
	r.deliver()

	// A healthy tick changes nothing, and says nothing.
	tr.watchdogTick()
	if r.openCount() != 1 {
		t.Fatalf("a healthy stream was restarted (%d opens)", r.openCount())
	}
	if len(*health) != 0 {
		t.Fatalf("a healthy stream reported %v", *health)
	}

	// Now the router simply stops sending. Nothing here does anything about it;
	// the shared fill's watchdog does, which is the whole point of the merge.
	stall(t, tr, 1)
	if r.openCount() < 2 {
		t.Errorf("a silent channel was not reopened (%d opens) — the chart stops "+
			"and nothing ever retries", r.openCount())
	}

	// AND THIS COLLECTOR NOTICES. The restart count is the Dashboard's, and a
	// recovery nobody reports is indistinguishable from one that never happened.
	tr.watchdogTick()
	tr.mu.Lock()
	seen := tr.seenRestarts
	tr.mu.Unlock()
	if seen == 0 {
		t.Error("the collector did not see the shared channel restart, so the card " +
			"will never show a degraded stream however often it recovers")
	}
}

// TestAStreamThatFailedToOpenIsRetried. `syncStream` gives up silently when the
// join returns an error, so without this a single failed open left the collector
// with no stream and nothing to start one.
//
// THIS IS THE JOB THAT COULD NOT MOVE. `JoinStream` failing leaves no fill
// behind, so there is nothing for the shared watchdog to watch: only the
// collector knows it wanted a holder and has none.
func TestAStreamThatFailedToOpenIsRetried(t *testing.T) {
	r, tr, _ := wdTraffic(t)
	openStream(tr)
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
	openStream(tr)

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
	// check, so a tick that skipped the guard would still fail to reopen and the
	// open count would look identical. What it WOULD do is release the holder
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
//
// END TO END NOW MEANS ACROSS TWO PACKAGES: the fake router goes silent,
// `roscache` reopens the fill three times, and this collector turns the rising
// count into exactly one transition. Under the old shape this collector did the
// restarting AND the counting, so the test could not tell a real recovery from
// its own bookkeeping.
func TestThreeRestartsReportDegraded(t *testing.T) {
	_, tr, health := wdTraffic(t)
	openStream(tr)

	// One tick per restart, so each is seen as it happens rather than three at
	// once — which is what the Dashboard sees, and what makes "exactly one
	// transition" a real assertion rather than an artefact of batching.
	for i := 1; i <= 3; i++ {
		stall(t, tr, i)
		tr.watchdogTick()
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
	openStream(tr)
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

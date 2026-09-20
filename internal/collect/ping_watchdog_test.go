package collect

// A PING STREAM THAT STOPS PRODUCING IS REOPENED.
//
// `/tool/ping` streams one row per interval — a lost ping is a row too — so a
// stream that has gone several intervals without one is dead. On the hAP AX3 the
// stream ended at 2026-09-13 04:19:40 with no error and no disconnect, and
// nothing noticed: `Stream` reports no end, and `startStream` refuses while an
// old handle is set. The Dashboard's Networks card, kept fresh by ping, went
// stale minutes after every page load for the rest of the morning.

import (
	"errors"
	"sync"
	"testing"
	"time"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// watchedStreamer counts opens and stops, keeps the latest row callback so a test
// can deliver rows, and can be told to fail the next open.
type watchedStreamer struct {
	mu    sync.Mutex
	opens int
	stops int
	fail  bool
	onRow func(routeros.Reply)
}

func (w *watchedStreamer) Connected() bool { return true }

func (w *watchedStreamer) Stream(_ routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.opens++
	if w.fail {
		return nil, errors.New("routeros: connection reset by peer")
	}
	w.onRow = onRow
	return func() {
		w.mu.Lock()
		w.stops++
		w.mu.Unlock()
	}, nil
}

func (w *watchedStreamer) counts() (opens, stops int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.opens, w.stops
}

// startedWithoutItsWatchdog is a started ping collector whose BACKGROUND
// watchdog loop has been stopped, so the only tick is the one the test calls.
//
// ── WHY, AND IT WAS A REAL FLAKE ────────────────────────────────────────────
//
// These tests age the stream by hand (goQuiet) and then call `watchdogTick`
// themselves: the tick under test is the one whose inputs the test set. `Start`
// also starts the real watchdog, and between `goQuiet` and the row the stream is
// deliberately stale — so a background tick landing in that window reopens it,
// and the test reports the second open as a failure of the code. It is not:
// reopening a stream that is stale WHEN THE WATCHDOG LOOKS is exactly right.
//
// THE FIRST TICK IS IMMEDIATE, AND THE INTERVAL DOES NOT HELP. `pollLoop.start`
// fires at once when a whole interval has passed since its last run, and a new
// loop has never run — so the watchdog's first tick is scheduled for now
// however long `wdEvery` is. Lengthening it still failed about twice in three
// hundred runs on one CPU. The loop has to be STOPPED.
//
// Measured after `TestALivePingStreamIsLeftAlone` failed inside a full
// `go test ./...` on a loaded machine (2026-09-20) with "2 opens, 1 stops",
// which is what a first tick arriving late produces. In production that tick
// reads a stream whose `streamStart` was stamped a moment earlier, finds it
// fresh, and does nothing.
func startedWithoutItsWatchdog(s *watchedStreamer) *Ping {
	p := NewPing(s, hub.Relay{}, 5000, "1.1.1.1")
	p.Start()
	// AFTER Start, which is what starts the loop. A ping that failed to open
	// its stream has no watchdog to stop.
	if p.wd != nil {
		p.wd.stop()
	}
	return p
}

// goQuiet makes the stream look silent for longer than the watchdog allows.
func goQuiet(p *Ping) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wdStaleMs = 1000
	past := time.Now().Add(-5 * time.Second).UnixMilli()
	p.streamStart, p.lastRow = past, past
}

func TestASilentPingStreamIsReopened(t *testing.T) {
	s := &watchedStreamer{}
	p := startedWithoutItsWatchdog(s)
	defer p.Stop()

	goQuiet(p)
	p.watchdogTick()

	opens, stops := s.counts()
	if opens != 2 {
		t.Errorf("%d stream open(s), want 2: a ping stream silent past its window was not reopened, "+
			"so the Dashboard's ping block and Networks card stay stale until a reconnect", opens)
	}
	if stops != 1 {
		t.Errorf("%d stop(s), want 1: the dead channel must be given up before a new one is opened", stops)
	}
}

func TestALivePingStreamIsLeftAlone(t *testing.T) {
	s := &watchedStreamer{}
	p := startedWithoutItsWatchdog(s)
	defer p.Stop()

	// The stream is old enough to be judged — and a row has JUST arrived, so it
	// is alive. Without the row this would reopen; that is what makes the row
	// the thing under test.
	goQuiet(p)
	s.mu.Lock()
	onRow := s.onRow
	s.mu.Unlock()
	onRow(routeros.Reply{"seq": "4", "time": "11ms", "status": ""})
	p.watchdogTick()

	if opens, stops := s.counts(); opens != 1 || stops != 0 {
		t.Errorf("a stream that just delivered a row was reopened (%d opens, %d stops)", opens, stops)
	}
}

func TestAPingStreamThatFailedToOpenIsRetried(t *testing.T) {
	s := &watchedStreamer{fail: true}
	p := startedWithoutItsWatchdog(s)
	defer p.Stop()

	s.mu.Lock()
	s.fail = false
	s.mu.Unlock()
	// A retry waits out the window from the failed attempt, so a persistent
	// error is retried once a window rather than on every tick.
	goQuiet(p)
	p.watchdogTick()

	p.mu.Lock()
	running := p.stop != nil
	p.mu.Unlock()
	if opens, _ := s.counts(); opens != 2 || !running {
		t.Errorf("after a failed open the watchdog did not retry (%d opens, running=%v): one "+
			"transient error would leave ping dead until the next reconnect", opens, running)
	}
}

func TestASuspendedPingIsNotReopened(t *testing.T) {
	s := &watchedStreamer{}
	p := startedWithoutItsWatchdog(s)
	p.Suspend()
	defer p.Stop()

	goQuiet(p)
	p.watchdogTick()

	if opens, _ := s.counts(); opens != 1 {
		t.Errorf("the watchdog reopened a ping stream the session had suspended (%d opens)", opens)
	}
}

func TestThePingWatchdogRunsOnlyWhileStreaming(t *testing.T) {
	s := &watchedStreamer{}
	p := NewPing(s, hub.Relay{}, 5000, "1.1.1.1")
	p.Start()
	if p.wd == nil || p.wd.stopped {
		t.Fatal("a streaming ping started without its watchdog")
	}
	p.Stop()
	if !p.wd.stopped {
		t.Error("Stop left the ping watchdog running")
	}

	polled := NewPing(&watchedStreamer{}, hub.Relay{}, 30000, "1.1.1.1")
	polled.Start()
	defer polled.Stop()
	if polled.wd != nil && !polled.wd.stopped {
		t.Error("a polling ping runs a stream watchdog; its poll loop already takes a fresh reading every tick")
	}
}

// slowOpenStreamer takes a moment to open, which is what a router does and what
// widens the window between "no stream is open" and "this one is".
type slowOpenStreamer struct{ watchedStreamer }

func (s *slowOpenStreamer) Stream(cmd routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	time.Sleep(time.Millisecond)
	return s.watchedStreamer.Stream(cmd, onRow)
}

// TestOnePingChannelWhateverRacesToReopenIt. The watchdog, SetPollMs and
// SetTarget each read "is a stream open" and then opened one, unserialised, so
// two of them together left two `/tool/ping` channels on one target: results
// counted twice, and one channel held until disconnect. It was also the flake
// in TestASilentPingStreamIsReopened, whose watchdog's first tick races the
// test's own (review loop).
func TestOnePingChannelWhateverRacesToReopenIt(t *testing.T) {
	s := &slowOpenStreamer{}
	p := NewPing(s, hub.Relay{}, 5000, "1.1.1.1")
	p.Start()

	var wg sync.WaitGroup
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				switch g % 3 {
				case 0:
					goQuiet(p)
					p.watchdogTick()
				case 1:
					p.SetPollMs(4000 + 1000*(i%2))
				case 2:
					p.SetTarget([]string{"1.1.1.1", "9.9.9.9"}[i%2])
				}
			}
		}(g)
	}
	wg.Wait()

	opens, stops := s.counts()
	if open := opens - stops; open != 1 {
		t.Errorf("%d opens and %d stops leave %d channels open, want exactly 1", opens, stops, open)
	}
	p.Stop()
	if opens, stops := s.counts(); opens != stops {
		t.Errorf("after Stop, %d opens and %d stops: a channel outlived the collector", opens, stops)
	}
}

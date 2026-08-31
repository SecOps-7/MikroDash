package wifiscan

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// ── the command ─────────────────────────────────────────────────────────────

func TestTheCommandCarriesWhatTheRouterNeeds(t *testing.T) {
	c := Cmd("*1", 60, true)
	if c.Path != "/interface/wifi/frequency-scan" {
		t.Errorf("path %q", c.Path)
	}
	joined := strings.Join(c.Args, " ")

	// `=.id=`, NOT `=number=`. The manual documents `number`; the binary API
	// rejects it with "missing =.id=".
	if !strings.Contains(joined, "=.id=*1") {
		t.Errorf("the command does not address the interface by .id: %q", joined)
	}
	if strings.Contains(joined, "=number=") {
		t.Error("the command uses =number=, which the binary API rejects outright")
	}
	// The proplist is load-bearing: without it RouterOS answers every
	// freeze-frame with a bare !empty and never sends a row.
	if !strings.Contains(joined, "=.proplist=") {
		t.Error("the command has no proplist -- the scan would run, take the radio " +
			"off the air for its full duration, and report nothing")
	}
	for _, f := range []string{"channel", "networks", "load", "nf", "max-signal", "min-signal"} {
		if !strings.Contains(joined, f) {
			t.Errorf("the proplist does not ask for %q", f)
		}
	}
	if !strings.Contains(joined, "=duration=00:01:00") {
		t.Errorf("the duration is not hh:mm:ss: %q", joined)
	}
	if !strings.Contains(joined, "=freeze-frame-interval=") {
		t.Error("no freeze-frame interval, so the router would report once at the end")
	}

	// The retry form drops =duration= and nothing else.
	r := strings.Join(Cmd("*1", 60, false).Args, " ")
	if strings.Contains(r, "=duration=") || strings.Contains(r, "=freeze-frame-interval=") {
		t.Errorf("the retry form still sends the timing parameters: %q", r)
	}
	if !strings.Contains(r, "=.proplist=") || !strings.Contains(r, "=.id=*1") {
		t.Errorf("the retry form dropped more than the duration: %q", r)
	}
}

// ── the runner ──────────────────────────────────────────────────────────────

type fakeConn struct {
	mu        sync.Mutex
	rows      []routeros.Reply
	onRow     func(routeros.Reply)
	onDone    func()
	connected atomic.Bool
	stops     int32
	openErr   error
	lastCmd   routeros.Cmd
}

func (f *fakeConn) StreamUntilDone(cmd routeros.Cmd, onRow func(routeros.Reply), onDone func()) (func(), error) {
	f.mu.Lock()
	f.lastCmd = cmd
	f.onRow, f.onDone = onRow, onDone
	err := f.openErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func() { atomic.AddInt32(&f.stops, 1) }, nil
}

func (f *fakeConn) Connected() bool { return f.connected.Load() }

func (f *fakeConn) send(rows ...routeros.Reply) {
	f.mu.Lock()
	fn := f.onRow
	f.mu.Unlock()
	for _, r := range rows {
		fn(r)
	}
}

func (f *fakeConn) endNaturally() {
	f.mu.Lock()
	fn := f.onDone
	f.mu.Unlock()
	fn()
}

type capture struct {
	mu     sync.Mutex
	rows   int
	errors []string
}

func (c *capture) Rows(string, []Row, bool) { c.mu.Lock(); c.rows++; c.mu.Unlock() }
func (c *capture) Error(_, code, _ string) {
	c.mu.Lock()
	c.errors = append(c.errors, code)
	c.mu.Unlock()
}

func row(freq string) routeros.Reply {
	return routeros.Reply{"channel": freq + "/20-Ce", "networks": "2", "load": "5",
		"nf": "-99", "max-signal": "-60", "min-signal": "-88"}
}

func runScan(t *testing.T, durationSec int, drive func(*fakeConn)) (Done, *capture, *fakeConn) {
	t.Helper()
	now := int64(1000)
	g := NewRegistry(func() int64 { return atomic.LoadInt64(&now) })

	doneCh := make(chan Done, 1)
	req := okReq()
	req.DurationSec = durationSec
	s, v := g.Begin(req, func(d Done) { doneCh <- d })
	if !v.OK {
		t.Fatalf("Begin refused: %+v", v)
	}

	conn := &fakeConn{}
	conn.connected.Store(true)
	cap := &capture{}

	go Run(g, s, conn, cap)
	// Wait for the stream to be opened before driving it.
	deadline := time.Now().Add(2 * time.Second)
	for {
		conn.mu.Lock()
		ready := conn.onRow != nil
		conn.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner never opened a stream")
		}
		time.Sleep(time.Millisecond)
	}
	drive(conn)

	select {
	case d := <-doneCh:
		return d, cap, conn
	case <-time.After(5 * time.Second):
		t.Fatal("the scan never finished")
	}
	return Done{}, nil, nil
}

// TestAStreamThatEndsByItselfCompletesAndIsNotCancelled.
func TestAStreamThatEndsByItselfCompletesAndIsNotCancelled(t *testing.T) {
	d, _, conn := runScan(t, 30, func(c *fakeConn) {
		c.send(row("2412"), row("2437"))
		c.endNaturally()
	})
	if d.Reason != "complete" {
		t.Errorf("reason %q, want complete", d.Reason)
	}
	if len(d.Rows) != 2 {
		t.Errorf("%d rows, want 2", len(d.Rows))
	}
	if d.SampleCount != 2 {
		t.Errorf("sampleCount %d, want 2", d.SampleCount)
	}
	if n := atomic.LoadInt32(&conn.stops); n != 0 {
		t.Errorf("a stream that ended by itself was cancelled %d times -- that is one "+
			"more write to a device that has just finished scanning", n)
	}
}

// TestALostConnectionEndsTheScanPromptly.
//
// The flush tick doubles as the liveness probe. Without it a router that reboots
// mid-scan leaves the entry sitting until the hard stop, blocking a retry on a
// radio that is already back.
func TestALostConnectionEndsTheScanPromptly(t *testing.T) {
	start := time.Now()
	d, _, _ := runScan(t, 120, func(c *fakeConn) {
		c.send(row("2412"))
		c.connected.Store(false)
	})
	if d.Reason != "disconnected" {
		t.Errorf("reason %q, want disconnected", d.Reason)
	}
	// The hard stop for a 120s scan is 125s away; this must not have waited for it.
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("the scan took %v to notice a dead connection", el)
	}
}

// TestRowsAreFlushedWhileTheScanRuns, and only when something changed.
func TestRowsAreFlushedWhileTheScanRuns(t *testing.T) {
	_, cap, _ := runScan(t, 30, func(c *fakeConn) {
		c.send(row("2412"))
		time.Sleep(3 * FlushInterval) // at least one tick with something to send
		c.endNaturally()
	})
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.rows == 0 {
		t.Error("no rows reached the browser while the scan was running")
	}
	// Three ticks, one change: a flush that ignored the dirty flag would emit on
	// every one of them.
	if cap.rows > 2 {
		t.Errorf("%d flushes for one change -- the dirty flag is not being honoured", cap.rows)
	}
}

// TestAFailureToOpenIsReportedAndFinishes.
func TestAFailureToOpenIsReportedAndFinishes(t *testing.T) {
	now := int64(1000)
	g := NewRegistry(func() int64 { return atomic.LoadInt64(&now) })
	doneCh := make(chan Done, 1)
	s, _ := g.Begin(okReq(), func(d Done) { doneCh <- d })
	conn := &fakeConn{openErr: errStr("no such command prefix")}
	conn.connected.Store(true)
	cap := &capture{}

	Run(g, s, conn, cap)

	select {
	case d := <-doneCh:
		if d.Reason != "error" {
			t.Errorf("reason %q, want error", d.Reason)
		}
	default:
		t.Fatal("a scan that could not open never finished -- the router stays marked busy")
	}
	if g.Size() != 0 {
		t.Error("the failed scan is still registered, so the router cannot be retried")
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.errors) != 1 {
		t.Fatalf("%d errors emitted, want 1", len(cap.errors))
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }

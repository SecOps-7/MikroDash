package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/diag"
	"mikrodash/internal/routeros"
)

// THE TOOLS PAGE'S OUTPUT IS LIVE (2026-09-18).
//
// A diagnostic used to be sent with Exec and drawn when its last row arrived, so
// a ten-second torch showed nothing for ten seconds. It is now streamed: each
// row is folded with everything before it, by the same pure Fold* the finished
// run uses, and sent as progress at most every progressEvery. These tests drive
// the runner with a fake stream — no router — and pin: progress, then the one
// final result, which is exactly the fold of every row; the throttle; a trap and
// a timeout reaching the caller as they did; and an early stop cancelling the
// stream and leaving nothing behind.

// fakeStream is a router connection whose rows the test sends.
type fakeStream struct {
	mu      sync.Mutex
	cmd     routeros.Cmd
	onRow   func(routeros.Reply)
	onDone  func(error)
	openErr error
	opened  chan struct{}
	stops   atomic.Int32
}

func newFakeStream() *fakeStream { return &fakeStream{opened: make(chan struct{})} }

func (f *fakeStream) StreamUntilDone(cmd routeros.Cmd, onRow func(routeros.Reply), onDone func(error)) (func(), error) {
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.mu.Lock()
	f.cmd, f.onRow, f.onDone = cmd, onRow, onDone
	f.mu.Unlock()
	close(f.opened)
	return func() { f.stops.Add(1) }, nil
}

// wait blocks until the runner has opened the stream.
func (f *fakeStream) wait(t *testing.T) {
	t.Helper()
	select {
	case <-f.opened:
	case <-time.After(2 * time.Second):
		t.Fatal("the runner never opened the stream")
	}
}

func (f *fakeStream) row(r routeros.Reply) { f.onRow(r) }
func (f *fakeStream) end(err error)        { f.onDone(err) }

// fastProgress makes the throttle short enough for a test to cross it.
func fastProgress(t *testing.T, d time.Duration) {
	old := progressEvery
	progressEvery = d
	t.Cleanup(func() { progressEvery = old })
}

type runOut[T any] struct {
	res       *T
	code, msg string
}

// runAsync starts a run and returns its outcome on a channel.
func runAsync[T any](run func() (*T, string, string)) <-chan runOut[T] {
	out := make(chan runOut[T], 1)
	go func() {
		r, c, m := run()
		out <- runOut[T]{r, c, m}
	}()
	return out
}

func outcome[T any](t *testing.T, ch <-chan runOut[T]) runOut[T] {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(3 * time.Second):
		t.Fatal("the run never returned: its goroutine and its connection's run slot would be held")
		return runOut[T]{}
	}
}

// A PING SHOWS EACH REPLY AS IT COMES, then the result the page has always
// drawn. The capture's replies are sent a throttle period apart, so each is
// reported on its own; the final result is the fold of every row, field for
// field, and the stream ended by itself, so no /cancel was sent.
func TestAPingShowsEachReplyAsItComes(t *testing.T) {
	fastProgress(t, 20*time.Millisecond)
	rows := readToolsCapture(t, "toolPing.json")[0]
	f := newFakeStream()
	var mu sync.Mutex
	var seen []int
	ch := runAsync(func() (*diag.PingResult, string, string) {
		return runPing(f, "127.0.0.1", 3, false, nil, func(p *diag.PingResult) {
			mu.Lock()
			seen = append(seen, len(p.Replies))
			mu.Unlock()
		})
	})
	f.wait(t)
	for _, r := range rows {
		f.row(r)
		time.Sleep(60 * time.Millisecond)
	}
	f.end(nil)
	o := outcome(t, ch)
	if o.code != "" || o.res == nil {
		t.Fatalf("the run failed: %s %s", o.code, o.msg)
	}
	want := diag.FoldPing("127.0.0.1", rows)
	if !reflect.DeepEqual(*o.res, want) {
		t.Errorf("the final result is not the fold of every row:\n got %+v\nwant %+v", *o.res, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != len(rows) {
		t.Fatalf("progress frames carried %v replies, want one frame per reply (%d)", seen, len(rows))
	}
	for i, n := range seen {
		if n != i+1 {
			t.Errorf("progress frame %d carried %d replies, want %d", i, n, i+1)
		}
	}
	if n := f.stops.Load(); n != 0 {
		t.Errorf("a run that ended by itself was stopped %d times: a /cancel to a finished command", n)
	}
	if f.cmd.Timeout <= 0 {
		t.Error("the streamed command carries no timeout: nothing would stop it on the router")
	}
}

// PROGRESS IS THROTTLED TO WHAT A PERSON CAN SEE. Two hundred rows at once are at
// most a frame or two of progress, and the final result still holds all of them.
func TestProgressIsThrottled(t *testing.T) {
	fastProgress(t, 50*time.Millisecond)
	f := newFakeStream()
	var frames atomic.Int32
	ch := runAsync(func() (*diag.PingResult, string, string) {
		return runPing(f, "198.51.100.1", 10, false, nil, func(*diag.PingResult) { frames.Add(1) })
	})
	f.wait(t)
	for i := 0; i < 200; i++ {
		f.row(routeros.Reply{"seq": fmt.Sprint(i), "host": "198.51.100.1", "status": "timeout", "sent": fmt.Sprint(i + 1)})
	}
	time.Sleep(120 * time.Millisecond)
	f.end(nil)
	o := outcome(t, ch)
	if o.res == nil || len(o.res.Replies) != 200 {
		t.Fatalf("the final result lost rows: %+v %s", o.res, o.code)
	}
	if n := frames.Load(); n < 1 || n > 3 {
		t.Errorf("%d progress frames for one burst over ~2 throttle periods, want 1 to 3", n)
	}
}

// A TRACEROUTE'S TABLE GROWS A SECTION AT A TIME: progress never draws a
// section still arriving, and the final table is the one the page always drew.
func TestATraceroutesTableGrowsASectionAtATime(t *testing.T) {
	fastProgress(t, 10*time.Millisecond)
	rows := readToolsCapture(t, "toolTraceroute.json")[1]
	f := newFakeStream()
	var mu sync.Mutex
	var hops []int
	ch := runAsync(func() (*diag.TracerouteResult, string, string) {
		return runTraceroute(f, "198.51.100.1", 3, nil, func(r *diag.TracerouteResult) {
			mu.Lock()
			hops = append(hops, len(r.Hops))
			mu.Unlock()
		})
	})
	f.wait(t)
	for _, r := range rows {
		f.row(r)
		time.Sleep(30 * time.Millisecond)
	}
	f.end(nil)
	o := outcome(t, ch)
	if want := diag.FoldTraceroute("198.51.100.1", rows); o.res == nil || !reflect.DeepEqual(*o.res, want) {
		t.Fatalf("the final table is not the fold of every row: %+v", o.res)
	}
	mu.Lock()
	defer mu.Unlock()
	// Sections 0, 1, 1, 2, 2, 2, 3, 3, 3: complete tables of 1, 2 and 3 hops.
	if !reflect.DeepEqual(hops, []int{1, 2, 3}) {
		t.Errorf("progress tables had %v hops, want [1 2 3]: a half-arrived section was drawn, or one was skipped", hops)
	}
}

// A BANDWIDTH TEST THAT IS REFUSED still says what RouterOS said, as a status
// rather than a trap, after its progress.
func TestARefusedBandwidthTestSaysWhy(t *testing.T) {
	fastProgress(t, 10*time.Millisecond)
	rows := readToolsCapture(t, "toolBandwidthTest.json")[1]
	cmd, err := diag.BandwidthTestCommand("198.51.100.53", "md-btest", "pw", 2, "tcp", "both")
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeStream()
	ch := runAsync(func() (*diag.BtestResult, string, string) {
		return streamBtest(f, cmd, "198.51.100.53", nil, func(*diag.BtestResult) {})
	})
	f.wait(t)
	for _, r := range rows {
		f.row(r)
	}
	f.end(nil)
	o := outcome(t, ch)
	if o.code != "failed" || !strings.Contains(o.msg, "authentication failed") || o.res == nil {
		t.Errorf("a refused test answered %q %q %+v", o.code, o.msg, o.res)
	}
	// The control: the finished capture is a result, not a failure.
	f = newFakeStream()
	ch = runAsync(func() (*diag.BtestResult, string, string) {
		return streamBtest(f, cmd, "198.51.100.53", nil, nil)
	})
	f.wait(t)
	done := readToolsCapture(t, "toolBandwidthTest.json")[0]
	for _, r := range done {
		f.row(r)
	}
	f.end(nil)
	if o := outcome(t, ch); o.code != "" || o.res == nil || !reflect.DeepEqual(*o.res, diag.FoldBandwidthTest("198.51.100.53", done)) {
		t.Errorf("a finished test answered %q %q %+v", o.code, o.msg, o.res)
	}
}

// A TRAP AND A TIMEOUT REACH THE PAGE AS THEY DID. A name that does not resolve
// is the router's own words; a run past its bound is a failure, not the partial
// rows folded as though it had finished.
func TestATrapOrATimeoutIsAFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"trap":    {&routeros.Trap{Message: "failure: resolve failed"}, "the router said: failure: resolve failed"},
		"timeout": {fmt.Errorf("routeros: timed out: %w", context.DeadlineExceeded), "timed out"},
		"control": {nil, ""},
	} {
		f := newFakeStream()
		ch := runAsync(func() (*diag.PingResult, string, string) {
			return runPing(f, "no.such.host", 2, false, nil, nil)
		})
		f.wait(t)
		f.row(routeros.Reply{"seq": "0", "host": "no.such.host", "status": "timeout", "sent": "1"})
		f.end(tc.err)
		o := outcome(t, ch)
		switch {
		case tc.want == "":
			if o.code != "" || o.res == nil {
				t.Errorf("%s: a clean end answered %q %q", name, o.code, o.msg)
			}
		case o.code != "failed" || !strings.Contains(o.msg, tc.want) || o.res != nil:
			t.Errorf("%s: answered %q %q %+v, want failed with %q and no result", name, o.code, o.msg, o.res, tc.want)
		}
	}
	// A stream that cannot open is a failure too.
	f := newFakeStream()
	f.openErr = &routeros.Trap{Message: "not connected"}
	if _, code, msg := runPing(f, "198.51.100.1", 2, false, nil, nil); code != "failed" || !strings.Contains(msg, "not connected") {
		t.Errorf("a stream that did not open answered %q %q", code, msg)
	}
}

// AN EARLY STOP — the operator pressing Stop or leaving the page, the socket
// closing, or a router switch — ends the run at once: the stream is stopped
// (which cancels it on the router), the runner returns THE RUN SO FAR with code
// "stopped" (the page draws it when it pressed Stop, and drops it otherwise),
// and nothing more is reported. Until Stop existed the answer was nothing,
// because nobody could be waiting for it.
func TestAnEarlyStopCancelsTheStream(t *testing.T) {
	fastProgress(t, 10*time.Millisecond)
	f := newFakeStream()
	quit := make(chan struct{})
	var frames atomic.Int32
	ch := runAsync(func() (*diag.PingResult, string, string) {
		return runPing(f, "198.51.100.1", 10, false, quit, func(*diag.PingResult) { frames.Add(1) })
	})
	f.wait(t)
	f.row(routeros.Reply{"seq": "0", "host": "198.51.100.1", "status": "timeout", "sent": "1"})
	time.Sleep(40 * time.Millisecond)
	close(quit)
	o := outcome(t, ch)
	if o.code != codeStopped || o.res == nil || len(o.res.Replies) != 1 || o.res.Sent != 1 {
		t.Errorf("a stopped run answered %q %+v, want %q and the one reply so far", o.code, o.res, codeStopped)
	}
	if n := f.stops.Load(); n != 1 {
		t.Errorf("the stream was stopped %d times, want 1: it would run on, holding its channel", n)
	}
	before := frames.Load()
	if before != 1 {
		t.Errorf("%d progress frames before the stop, want 1 (the control)", before)
	}
	f.row(routeros.Reply{"seq": "1", "host": "198.51.100.1", "status": "timeout", "sent": "2"})
	time.Sleep(40 * time.Millisecond)
	if frames.Load() != before {
		t.Error("progress was reported after the run was stopped")
	}
}

// readToolsCapture reads the rows of each exchange in one tools fixture.
func readToolsCapture(t *testing.T, name string) [][]routeros.Reply {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", "CHR Test", name))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Exchanges []struct {
			Rows []routeros.Reply `json:"rows"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	out := make([][]routeros.Reply, 0, len(c.Exchanges))
	for _, ex := range c.Exchanges {
		out = append(out, ex.Rows)
	}
	return out
}

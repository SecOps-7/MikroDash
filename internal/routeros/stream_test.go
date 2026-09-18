package routeros

// A BOUNDED COMMAND SENT AS A STREAM IS HELD TO THE SAME RULES AS ONE SENT BY Do.
//
// The Tools page streams ping, traceroute, torch and bandwidth test so the
// operator sees each row as it comes. Do had three guarantees those runs relied
// on, and a stream had none of them: a trap reached the caller, a command past
// its Timeout was cancelled on the router, and a command the router would not end
// cost the connection rather than a channel held for ever.

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3/proto"
)

// streamRouter logs a client in, then answers every command other than
// `/cancel` with `script`, in a goroutine of its own. A `/cancel` reports the tag
// it names and, if honourCancel, ends that command as RouterOS does: an
// interrupted `!trap`, then `!done`.
func streamRouter(t *testing.T, honourCancel bool, script func(send func(words ...string), tag string)) (int, <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	cancelled := make(chan string, 16)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r, w := proto.NewReader(c), proto.NewWriter(c)
		out := make(chan []string, 64)
		go func() {
			for words := range out {
				w.BeginSentence()
				for _, word := range words {
					w.WriteWord(word)
				}
				_ = w.EndSentence()
			}
		}()
		send := func(words ...string) { out <- words }
		if _, err := r.ReadSentence(); err != nil { // the /login
			return
		}
		send("!done")
		for {
			sen, err := r.ReadSentence()
			if err != nil {
				return
			}
			if sen.Word == "/cancel" {
				tag := sen.Map["tag"]
				cancelled <- tag
				if honourCancel {
					send("!trap", "=category=2", "=message=interrupted", ".tag="+tag)
					send("!done", ".tag="+tag)
				}
				send("!done", ".tag="+sen.Tag)
				continue
			}
			go script(func(words ...string) { send(append(words, ".tag="+sen.Tag)...) }, sen.Tag)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, cancelled
}

// endOf waits for a stream's onDone and returns what it was given.
func endOf(t *testing.T, ended <-chan error) error {
	t.Helper()
	select {
	case err := <-ended:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("the stream never reported its end")
		return nil
	}
}

// A STREAM REPORTS HOW IT ENDED: nil for `!done`, the router's own words for a
// trap. Before, both read as "it ended", so a name that did not resolve looked
// like a run with no replies.
func TestAStreamReportsHowItEnded(t *testing.T) {
	port, _ := streamRouter(t, true, func(send func(...string), _ string) {
		send("!re", "=seq=0")
		send("!re", "=seq=1")
		send("!done")
	})
	cl := dialFake(t, port)
	var rows atomic.Int32
	ended := make(chan error, 1)
	if _, err := cl.StreamUntilDone(Cmd{Path: "/tool/ping"}, func(Reply) { rows.Add(1) },
		func(err error) { ended <- err }); err != nil {
		t.Fatal(err)
	}
	if err := endOf(t, ended); err != nil {
		t.Errorf("a stream that ended on !done reported %v", err)
	}
	if n := rows.Load(); n != 2 {
		t.Errorf("%d rows delivered before the end, want 2", n)
	}

	port, _ = streamRouter(t, true, func(send func(...string), _ string) {
		send("!trap", "=message=failure: resolve failed")
		send("!done")
	})
	cl = dialFake(t, port)
	ended = make(chan error, 1)
	if _, err := cl.StreamUntilDone(Cmd{Path: "/tool/ping"}, nil, func(err error) { ended <- err }); err != nil {
		t.Fatal(err)
	}
	var trap *Trap
	if err := endOf(t, ended); !errors.As(err, &trap) || trap.Message != "failure: resolve failed" {
		t.Errorf("a trapped stream reported %v, want the router's trap", err)
	}
	if !cl.Connected() {
		t.Errorf("a trap ended the connection: %v", cl.Err())
	}
}

// A STREAM PAST ITS TIMEOUT IS CANCELLED ON THE ROUTER, and says it timed out.
// Cmd.Timeout bounds the command however it is sent.
func TestAStreamPastItsTimeoutIsCancelledOnTheRouter(t *testing.T) {
	port, cancelled := streamRouter(t, true, func(send func(...string), _ string) {
		send("!re", "=seq=0") // and then nothing, for ever
	})
	cl := dialFake(t, port)
	ended := make(chan error, 1)
	stop, err := cl.StreamUntilDone(Cmd{Path: "/tool/ping", Timeout: 100 * time.Millisecond}, nil,
		func(err error) { ended <- err })
	if err != nil {
		t.Fatal(err)
	}
	if err := endOf(t, ended); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a stream past its timeout reported %v, want a DeadlineExceeded timeout", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("the stream timed out and no /cancel reached the router: it would run on, holding its channel")
	}
	if !cl.Connected() {
		t.Errorf("a timed-out stream ended the connection: %v", cl.Err())
	}
	stop() // after the end: returns at once, and sends nothing more
	select {
	case tag := <-cancelled:
		t.Errorf("stop after the end sent a second /cancel for %s", tag)
	case <-time.After(50 * time.Millisecond):
	}
}

// A STREAM THE ROUTER WILL NOT END COSTS THE CONNECTION, not a channel held for
// ever. stop() used to wait on the router without limit, so one ignored cancel
// left the caller, its goroutine and its channel stuck.
func TestAStreamWhoseCancelIsIgnoredEndsTheConnection(t *testing.T) {
	old := cancelGrace
	cancelGrace = 200 * time.Millisecond
	t.Cleanup(func() { cancelGrace = old })

	port, cancelled := streamRouter(t, false, func(send func(...string), _ string) {
		send("!re", "=seq=0")
	})
	cl := dialFake(t, port)
	var ended atomic.Int32
	stop, err := cl.StreamUntilDone(Cmd{Path: "/tool/torch"}, nil, func(error) { ended.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("stop sent no /cancel")
	}
	if !waitFor(func() bool { return !cl.Connected() }) {
		t.Fatal("the router ignored the cancel and the connection stayed up: the stream holds its channel for ever")
	}
	if e := cl.Err(); e == nil || !strings.Contains(e.Error(), "/tool/torch") {
		t.Errorf("the connection's recorded failure %v does not name the command", e)
	}
	// The connect loops close a failed client; closing is what ends the stream.
	_ = cl.Close()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the failed connection did not release stop()")
	}
	if n := ended.Load(); n != 0 {
		t.Errorf("onDone ran %d times for a stream the caller stopped", n)
	}
}

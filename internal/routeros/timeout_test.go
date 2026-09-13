package routeros

// A COMMAND THAT TIMES OUT MUST NOT TAKE THE CONNECTION WITH IT.
//
// go-routeros's async RunArgsContext answers a finished context with
// `c.r.Cancel()` on the reader the WHOLE connection shares (run.go), and a
// cancelled read returns io.EOF (proto/io_context.go). So one command past its
// deadline ended every command on the connection, and the session logged
// "connection closed without a !fatal from the router: EOF". On 2026-09-13 the
// cAP AX and CHR Test both dropped 15 s after a restart — the session's default
// deadline, while every collector was making its first read at once.
//
// Do now waits on its own timer and never hands the library a context it can
// cancel. The command runs on to its reply, and Finished marks when it is over,
// which is when its router slot may be given back.

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3/proto"
)

// slowRouter logs a client in, then answers every command with `!done`: the
// first one after `delay`, every other one at once.
func slowRouter(t *testing.T, delay time.Duration) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r, w := proto.NewReader(c), proto.NewWriter(c)
		reply := func(tag string) {
			w.BeginSentence()
			w.WriteWord("!done")
			if tag != "" {
				w.WriteWord(".tag=" + tag)
			}
			_ = w.EndSentence()
		}
		if _, err := r.ReadSentence(); err != nil { // the /login
			return
		}
		reply("")
		first := true
		for {
			sen, err := r.ReadSentence()
			if err != nil {
				return
			}
			if first {
				first = false
				go func(tag string) { time.Sleep(delay); reply(tag) }(sen.Tag)
				continue
			}
			reply(sen.Tag)
		}
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func TestATimedOutCommandLeavesTheConnectionUp(t *testing.T) {
	port := slowRouter(t, 500*time.Millisecond)
	cl, err := Dial(Config{Host: "127.0.0.1", Port: port, Username: "u", Password: "p",
		DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer cl.Close()

	var finished atomic.Int32
	_, err = cl.Do(Cmd{Path: "/slow/print", Timeout: 100 * time.Millisecond,
		Finished: func() { finished.Add(1) }})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the slow command returned %v, want a DeadlineExceeded timeout", err)
	}
	if !cl.Connected() {
		t.Fatalf("one command timing out ended the connection (Err: %v): every other command "+
			"on it failed too, and the router showed as disconnected", cl.Err())
	}
	if n := finished.Load(); n != 0 {
		t.Errorf("Finished ran %d time(s) when the caller gave up; it must wait for the reply, "+
			"which is when the router's slot is really free", n)
	}

	if _, err := cl.Do(Cmd{Path: "/fast/print", Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("the next command on the same connection failed: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for finished.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the slow command's reply arrived and Finished never ran: its router slot would never be released")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := finished.Load(); n != 1 {
		t.Errorf("Finished ran %d times, want exactly 1", n)
	}
	if e := cl.Err(); e != nil {
		t.Errorf("a timeout was recorded as the connection's failure: %v", e)
	}
}

func TestFinishedRunsOnceWhenTheConnectionIsAlreadyGone(t *testing.T) {
	c := &Client{closed: true}
	var finished atomic.Int32
	if _, err := c.Do(Cmd{Path: "/x", Timeout: time.Second, Finished: func() { finished.Add(1) }}); err == nil {
		t.Fatal("a command on a closed client returned no error")
	}
	if n := finished.Load(); n != 1 {
		t.Errorf("Finished ran %d times on the early return, want 1: a slot taken for this command would leak", n)
	}
}

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
// cancel. The command is then cancelled on the router by its tag, and Finished
// marks when it is over, which is when its router slot may be given back.

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
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

// stuckRouter logs a client in, then never answers `/stuck/print`. When told
// `/cancel =tag=<tag>` it reports the tag and, if honourCancel, ends that one
// command the way RouterOS does: an interrupted `!trap`, then `!done`. Every
// other command gets `!done` at once.
func stuckRouter(t *testing.T, honourCancel bool) (int, <-chan string) {
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
		send := func(words ...string) {
			w.BeginSentence()
			for _, word := range words {
				w.WriteWord(word)
			}
			_ = w.EndSentence()
		}
		if _, err := r.ReadSentence(); err != nil { // the /login
			return
		}
		send("!done")
		stuck := map[string]bool{}
		for {
			sen, err := r.ReadSentence()
			if err != nil {
				return
			}
			switch sen.Word {
			case "/stuck/print":
				stuck[sen.Tag] = true
			case "/cancel":
				tag := sen.Map["tag"]
				cancelled <- tag
				if honourCancel && stuck[tag] {
					delete(stuck, tag)
					send("!trap", "=category=2", "=message=interrupted", ".tag="+tag)
					send("!done", ".tag="+tag)
				}
				send("!done", ".tag="+sen.Tag)
			default:
				send("!done", ".tag="+sen.Tag)
			}
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, cancelled
}

func dialFake(t *testing.T, port int) *Client {
	t.Helper()
	cl, err := Dial(Config{Host: "127.0.0.1", Port: port, Username: "u", Password: "p",
		DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// waitFor polls cond for up to two seconds.
func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return true
}

// A COMMAND THE ROUTER NEVER ANSWERS IS CANCELLED, AND ITS SLOT COMES BACK.
//
// 0.8.53 left it running, and on 2026-09-13 the hAP AX3 filled all eight of its
// router slots with such commands; every poll on it then waited for ever.
func TestATimedOutCommandIsCancelledOnTheRouter(t *testing.T) {
	old := cancelGrace
	cancelGrace = time.Minute // so only the cancel can end it inside this test
	t.Cleanup(func() { cancelGrace = old })

	port, cancelled := stuckRouter(t, true)
	cl := dialFake(t, port)

	// Other commands going out at the same moment. The router ends the stuck
	// command only for ITS tag, so a tag read off the wire that belonged to one
	// of these would cancel the wrong command and leave the stuck one holding on.
	stop := make(chan struct{})
	var noise sync.WaitGroup
	for i := 0; i < 8; i++ {
		noise.Add(1)
		go func() {
			defer noise.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := cl.Do(Cmd{Path: "/noise/print", Timeout: 2 * time.Second}); err != nil {
					t.Errorf("a concurrent command failed: %v", err)
					return
				}
			}
		}()
	}

	var finished atomic.Int32
	_, err := cl.Do(Cmd{Path: "/stuck/print", Timeout: 100 * time.Millisecond,
		Finished: func() { finished.Add(1) }})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the unanswered command returned %v, want a DeadlineExceeded timeout", err)
	}
	if !waitFor(func() bool { return finished.Load() > 0 }) {
		t.Fatal("the unanswered command was never ended: its router slot would be held for ever, " +
			"and eight of them stop every poll on the router")
	}
	close(stop)
	noise.Wait()

	select {
	case <-cancelled:
	default:
		t.Fatal("the command ended without a /cancel reaching the router")
	}
	if n := finished.Load(); n != 1 {
		t.Errorf("Finished ran %d times, want exactly 1", n)
	}
	if !cl.Connected() {
		t.Fatalf("cancelling one command ended the connection (Err: %v)", cl.Err())
	}
	if _, err := cl.Do(Cmd{Path: "/fast/print", Timeout: time.Second}); err != nil {
		t.Fatalf("the next command on the same connection failed: %v", err)
	}
}

// A ROUTER THAT WILL NOT END A CANCELLED COMMAND LOSES THE CONNECTION, AND SAYS WHY.
func TestACommandThatIgnoresItsCancelEndsTheConnection(t *testing.T) {
	old := cancelGrace
	cancelGrace = 200 * time.Millisecond
	t.Cleanup(func() { cancelGrace = old })

	port, cancelled := stuckRouter(t, false)
	cl := dialFake(t, port)

	var finished atomic.Int32
	_, err := cl.Do(Cmd{Path: "/stuck/print", Timeout: 100 * time.Millisecond,
		Finished: func() { finished.Add(1) }})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the unanswered command returned %v, want a DeadlineExceeded timeout", err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("no /cancel was sent for the timed-out command")
	}
	if !waitFor(func() bool { return !cl.Connected() }) {
		t.Fatal("the router ignored the cancel and the connection stayed up: the command keeps " +
			"its router slot, and nothing ever redials to free it")
	}
	if e := cl.Err(); e == nil || !strings.Contains(e.Error(), "/stuck/print") {
		t.Errorf("the connection's recorded failure %v does not name the command", e)
	}
	// The connect loops close a failed client; closing is what ends the command.
	_ = cl.Close()
	if !waitFor(func() bool { return finished.Load() == 1 }) {
		t.Fatal("closing the failed connection did not end the command: its slot would leak")
	}
}

// NO REPLY IS LOST BEFORE ITS TAG IS REGISTERED.
//
// go-routeros v3.0.1 wrote a command and only then registered its tag, and its
// async loop drops a reply whose tag it does not know: with eight callers at once
// against a local router, 38 of 1,600 commands never returned. On a live router
// each one held a command slot for ever. third_party/go-routeros registers first;
// this fails on a library that does not.
func TestNoReplyIsLostBeforeItsTagIsRegistered(t *testing.T) {
	port, _ := stuckRouter(t, true)
	cl := dialFake(t, port)

	var lost atomic.Int32
	var callers sync.WaitGroup
	for g := 0; g < 8; g++ {
		callers.Add(1)
		go func() {
			defer callers.Done()
			for i := 0; i < 200; i++ {
				if _, err := cl.Do(Cmd{Path: "/noise/print", Timeout: 500 * time.Millisecond}); err != nil {
					lost.Add(1)
				}
			}
		}()
	}
	callers.Wait()
	if n := lost.Load(); n != 0 {
		t.Errorf("%d of 1600 commands got no reply from a router that answered every one: the "+
			"library dropped replies that arrived before their tags were registered", n)
	}
}

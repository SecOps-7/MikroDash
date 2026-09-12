package routeros

// WHAT `Err()` SAYS WHEN A ROUTER ENDS THE SESSION, END TO END.
//
// A fake RouterOS server accepts the login and then closes the connection, with
// or without a `!fatal` first. This drives the real `Dial`, so it proves the
// watcher is installed on the connection and that the async loop's EOF is
// recorded with the reason the watcher saw.

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3/proto"
)

// fakeRouter logs any client in, then runs `then` on the connection and closes it.
func fakeRouter(t *testing.T, then func(w proto.Writer)) (port int) {
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
		if _, err := r.ReadSentence(); err != nil { // the /login
			return
		}
		w.BeginSentence()
		w.WriteWord("!done")
		_ = w.EndSentence()
		time.Sleep(50 * time.Millisecond) // let the client reach async mode
		then(w)
	}()
	return l.Addr().(*net.TCPAddr).Port
}

func dialAndWaitForTheDrop(t *testing.T, port int) *Client {
	t.Helper()
	cl, err := Dial(Config{Host: "127.0.0.1", Port: port, Username: "u", Password: "p",
		DialTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	deadline := time.Now().Add(3 * time.Second)
	for cl.Connected() {
		if time.Now().After(deadline) {
			t.Fatal("the fake router closed the connection and the client still reports connected")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cl
}

func TestErrNamesTheReasonARouterGaveForEndingTheSession(t *testing.T) {
	port := fakeRouter(t, func(w proto.Writer) {
		w.BeginSentence()
		w.WriteWord("!fatal")
		w.WriteWord("=message=session terminated on request")
		_ = w.EndSentence()
	})
	got := dialAndWaitForTheDrop(t, port).Err()
	if got == nil || !strings.Contains(got.Error(), `router ended the session: "session terminated on request"`) {
		t.Fatalf("Err() = %v; want it to name the reason the router sent before closing", got)
	}
	if !errors.Is(got, io.EOF) {
		t.Errorf("Err() = %v no longer wraps the EOF underneath", got)
	}
}

func TestErrSaysSoWhenTheConnectionClosedWithoutAFatal(t *testing.T) {
	port := fakeRouter(t, func(proto.Writer) {})
	got := dialAndWaitForTheDrop(t, port).Err()
	if got == nil || !strings.Contains(got.Error(), "without a !fatal") {
		t.Fatalf("Err() = %v; want it to say the connection closed without a !fatal", got)
	}
	if !errors.Is(got, io.EOF) {
		t.Errorf("Err() = %v no longer wraps the EOF underneath", got)
	}
}

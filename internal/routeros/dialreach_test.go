package routeros

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/netguard"
)

// startReach is what init left, read before TestMain changes it.
var startReach netguard.Reach

// TestMain lets this package's fake routers, which listen on loopback, be
// dialled. Every test below that is about the guard sets the reach it needs
// and puts it back.
func TestMain(m *testing.M) {
	startReach = DialReach()
	SetDialReach(netguard.Any)
	os.Exit(m.Run())
}

// TestDialRefusesALoopbackRouterAndSendsItNothing (GHSA-mcwr-rm79-fr4h). Dial
// sends the stored username and password to whatever the record names, so at
// the default reach a router on loopback is refused before a byte is written:
// the listener sees no login. At `any` the same listener does, which is the
// control that shows the refusal is the guard's and not the listener's.
func TestDialRefusesALoopbackRouterAndSendsItNothing(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan string, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			buf := make([]byte, 256)
			n, _ := c.Read(buf)
			got <- string(buf[:n])
			_ = c.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	cfg := Config{Host: "127.0.0.1", Port: port, Username: "u", Password: "the-secret",
		DialTimeout: time.Second}

	defer SetDialReach(DialReach())
	for _, tc := range []struct {
		reach netguard.Reach
		tls   bool
	}{{netguard.Private, false}, {netguard.Private, true}, {netguard.Public, false}} {
		SetDialReach(tc.reach)
		cfg.TLS = tc.tls
		_, err := Dial(cfg)
		if err == nil || !strings.Contains(err.Error(), "address") {
			t.Fatalf("%v tls=%v: a loopback router was dialled: %v", tc.reach, tc.tls, err)
		}
		select {
		case b := <-got:
			t.Fatalf("%v tls=%v: the loopback listener received %q", tc.reach, tc.tls, b)
		case <-time.After(150 * time.Millisecond):
		}
	}

	SetDialReach(netguard.Any)
	cfg.TLS = false
	_, _ = Dial(cfg)
	select {
	case b := <-got:
		if !strings.Contains(b, "the-secret") {
			t.Fatalf("at any, the listener received %q, not the login", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("at any the listener received nothing either, so the refusals above prove nothing")
	}
}

// TestASuppliedDialerIsJudgedToo. The tunnel hands Dial its own dialer, which
// no Control hook reaches, so the address is judged before that dialer is
// called; at the default reach a loopback address never reaches it.
func TestASuppliedDialerIsJudgedToo(t *testing.T) {
	defer SetDialReach(DialReach())
	SetDialReach(netguard.Private)
	called := false
	_, err := Dial(Config{Host: "127.0.0.1", Port: 8728, DialTimeout: time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			called = true
			return nil, errors.New("not reached")
		}})
	if called || err == nil || !strings.Contains(err.Error(), "local address") {
		t.Fatalf("the supplied dialer was called (%v) or the error was %v", called, err)
	}
}

// TestTheDefaultReachIsPrivate. A process that never calls SetDialReach - every
// cmd/ tool, and the server before its flag is read - must still refuse
// loopback and link-local. The zero value would be Public, which is why init
// sets it; this reads what init left, before TestMain changed it.
func TestTheDefaultReachIsPrivate(t *testing.T) {
	if startReach != netguard.Private {
		t.Fatalf("the default reach is %v, want private", startReach)
	}
}

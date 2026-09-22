package routeros

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-routeros/routeros/v3/proto"
)

// A CONFIGURED DialContext OPENS THE CONNECTION: zero-touch provisioning
// reaches a remote router through its own userspace tunnel. The router here is
// at an address nothing on this machine routes to (a tunnel address), and the
// login still succeeds, so it can only have gone through the hook, which is
// handed the router's own address.
func TestADialHookOpensTheConnection(t *testing.T) {
	port := fakeRouter(t, func(w proto.Writer) { time.Sleep(200 * time.Millisecond) })
	var asked string
	c, err := Dial(Config{Host: "10.249.1.2", Port: 8728, Username: "u", Password: "p", DialTimeout: 5 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			asked = network + " " + addr
			return new(net.Dialer).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
		}})
	if err != nil {
		t.Fatalf("Dial through the hook: %v", err)
	}
	c.Close()
	if asked != "tcp 10.249.1.2:8728" {
		t.Errorf("the hook was asked for %q, want the router's own address", asked)
	}

	// A HOOK THAT FAILS fails the dial with the wording the rest of the app
	// reads (safe.Message, TestConnReason): "could not connect to router os".
	_, err = Dial(Config{Host: "10.249.1.2", Port: 8728, DialTimeout: time.Second,
		DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("tunnel down") }})
	if err == nil || !strings.Contains(err.Error(), "could not connect to router os") || !strings.Contains(err.Error(), "tunnel down") {
		t.Errorf("a failing hook gave %v", err)
	}
}

// THE TUNNEL IS A ROUTE: with it set, an address inside its prefix is dialled
// through it and one outside is not; cleared, nothing is. The hook's own
// DialContext still wins over the route.
func TestATunnelRouteCarriesOnlyItsOwnAddresses(t *testing.T) {
	port := fakeRouter(t, func(w proto.Writer) { time.Sleep(200 * time.Millisecond) })
	var used []string
	SetTunnel(netip.MustParsePrefix("10.249.0.0/16"), func(ctx context.Context, network, addr string) (net.Conn, error) {
		used = append(used, addr)
		return new(net.Dialer).DialContext(ctx, "tcp", "127.0.0.1:"+strconv.Itoa(port))
	})
	t.Cleanup(ClearTunnel)

	c, err := Dial(Config{Host: "10.249.1.2", Port: 8728, Username: "u", Password: "p", DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("an address inside the tunnel prefix was not dialled through it: %v", err)
	}
	c.Close()
	if len(used) != 1 || used[0] != "10.249.1.2:8728" {
		t.Errorf("the tunnel was asked for %v", used)
	}

	// Outside the prefix: the system dialer, which cannot reach TEST-NET-3 on
	// this port within the timeout. The tunnel must not be asked.
	_, _ = Dial(Config{Host: "203.0.113.9", Port: 8728, DialTimeout: 300 * time.Millisecond})
	if len(used) != 1 {
		t.Errorf("an address outside the prefix went through the tunnel: %v", used)
	}

	// Cleared: not even a tunnel address is.
	ClearTunnel()
	_, _ = Dial(Config{Host: "10.249.1.2", Port: 8728, DialTimeout: 300 * time.Millisecond})
	if len(used) != 1 {
		t.Errorf("a cleared route was still used: %v", used)
	}
}

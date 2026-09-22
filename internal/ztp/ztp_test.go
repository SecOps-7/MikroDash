package ztp

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestAKeyPairAgreesWithItsPublicKey(t *testing.T) {
	priv, pub, err := NewKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := PublicKey(priv); err != nil || got != pub {
		t.Fatalf("PublicKey(private) = %q, %v; want %q", got, err, pub)
	}
	if !ValidKey(priv) || !ValidKey(pub) || ValidKey("not-a-key") || ValidKey("AAAA") {
		t.Error("ValidKey accepts the wrong things")
	}
	if p2, _, _ := NewKeyPair(); p2 == priv {
		t.Error("two key pairs are the same")
	}
}

// THE ADDRESS PLAN: the server at .0.1, devices after the server's /24 and
// before the enrolment /24, no .0 or .255, nothing handed out twice.
func TestTheAddressPlan(t *testing.T) {
	p, err := ParsePlan("10.249.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	if p.Server().String() != "10.249.0.1" || p.Enrolment().String() != "10.249.255.0/24" {
		t.Fatalf("server %s, enrolment %s", p.Server(), p.Enrolment())
	}
	used := map[netip.Addr]bool{}
	var got []string
	for i := 0; i < 256; i++ {
		a, err := p.NextFree(used)
		if err != nil {
			t.Fatal(err)
		}
		if used[a] || !p.IsDevice(a) {
			t.Fatalf("%s handed out twice, or not a device address", a)
		}
		used[a] = true
		got = append(got, a.String())
	}
	if got[0] != "10.249.1.1" || got[253] != "10.249.1.254" || got[254] != "10.249.2.1" {
		t.Errorf("order: first %s, 254th %s, 255th %s", got[0], got[253], got[254])
	}
	for _, a := range []string{"10.249.0.1", "10.249.0.9", "10.249.1.0", "10.249.1.255", "10.249.255.7", "10.250.1.1"} {
		if p.IsDevice(netip.MustParseAddr(a)) {
			t.Errorf("%s is taken for a device address", a)
		}
	}
	for _, bad := range []string{"10.249.0.0/24", "10.249.0.0/23", "2001:db8::/48", "nope"} {
		if _, err := ParsePlan(bad); err == nil {
			t.Errorf("%q was accepted as a tunnel prefix", bad)
		}
	}
	// A full /22: four /24s, less the server's and the enrolment one, 254 each.
	small, _ := ParsePlan("10.9.0.0/22")
	full := map[netip.Addr]bool{}
	for {
		a, err := small.NextFree(full)
		if err != nil {
			break
		}
		full[a] = true
	}
	if len(full) != 508 {
		t.Errorf("a /22 held %d device addresses, want 508", len(full))
	}
}

func TestParseStatus(t *testing.T) {
	dump := "private_key=00\nlisten_port=13231\n" +
		"public_key=" + strings.Repeat("ab", 32) + "\nendpoint=192.0.2.10:51820\n" +
		"last_handshake_time_sec=1790000000\nlast_handshake_time_nsec=0\nrx_bytes=10\ntx_bytes=20\n" +
		"public_key=" + strings.Repeat("cd", 32) + "\nlast_handshake_time_sec=0\n"
	got := parseStatus(dump)
	if len(got) != 2 {
		t.Fatalf("%d peers, want 2: %+v", len(got), got)
	}
	if got[0].Endpoint != "192.0.2.10:51820" || got[0].RxBytes != 10 || got[0].TxBytes != 20 ||
		got[0].LastHandshake.Unix() != 1790000000 || !ValidKey(got[0].PublicKey) {
		t.Errorf("first peer: %+v", got[0])
	}
	if !got[1].LastHandshake.IsZero() || got[1].Endpoint != "" {
		t.Errorf("a peer that never shook hands reads as one that did: %+v", got[1])
	}
}

// pair starts a "MikroDash" at 10.249.0.1 and a "router" claiming routerAddr,
// the server allowing the router only allowed, over UDP on loopback.
func pair(t *testing.T, routerAddr, allowed string) (server, router *Engine) {
	t.Helper()
	sPriv, sPub, _ := NewKeyPair()
	rPriv, rPub, _ := NewKeyPair()
	var err error
	if server, err = Start(Config{PrivateKey: sPriv, Address: netip.MustParseAddr("10.249.0.1")}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	port, err := server.ListenPort()
	if err != nil || port == 0 {
		t.Fatalf("listen port %d, %v", port, err)
	}
	if router, err = Start(Config{PrivateKey: rPriv, Address: netip.MustParseAddr(routerAddr)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Close)
	// The server knows the device by key and /32; the device knows the server's
	// endpoint, as the bootstrap will configure it.
	if err := server.SetPeer(Peer{PublicKey: rPub, Allowed: []netip.Prefix{netip.MustParsePrefix(allowed)}}); err != nil {
		t.Fatal(err)
	}
	if err := router.SetPeer(Peer{PublicKey: sPub, Allowed: []netip.Prefix{netip.MustParsePrefix("10.249.0.1/32")},
		Endpoint: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)), Keepalive: 1}); err != nil {
		t.Fatal(err)
	}
	return server, router
}

// serveHello answers every request with the caller's address, on the server's
// tunnel address port 80, as /enrol will listen.
func serveHello(t *testing.T, e *Engine) {
	t.Helper()
	l, err := e.ListenTCP(80)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, "hello %s", r.RemoteAddr)
		}))
	}()
}

func callHome(router *Engine, timeout time.Duration) (string, error) {
	hc := &http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: router.DialContext}}
	resp, err := hc.Get("http://10.249.0.1/enrol")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b := make([]byte, 64)
	n, _ := resp.Body.Read(b)
	return string(b[:n]), nil
}

// THE ENGINE, END TO END ON ONE MACHINE: each end a userspace WireGuard with
// its own IP stack. The router calls home, and MikroDash dials the router's
// "API". Nothing here needs root, a tun device or a route: the point of the
// design.
func TestTheTunnelCarriesBothDirections(t *testing.T) {
	server, router := pair(t, "10.249.1.2", "10.249.1.2/32")
	serveHello(t, server)

	got, err := callHome(router, 10*time.Second)
	if err != nil {
		t.Fatalf("the router could not call home through the tunnel: %v", err)
	}
	if !strings.HasPrefix(got, "hello 10.249.1.2:") {
		t.Errorf("the server saw %q: the caller's tunnel address is how it knows who called", got)
	}

	api, err := router.ListenTCP(8728)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	go func() {
		c, err := api.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("api ok\n"))
		c.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := server.DialContext(ctx, "tcp", "10.249.1.2:8728")
	if err != nil {
		t.Fatalf("MikroDash could not reach the router's API through the tunnel: %v", err)
	}
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if line != "api ok\n" {
		t.Errorf("read %q", line)
	}

	st, err := server.Status()
	if err != nil || len(st) != 1 || st[0].LastHandshake.IsZero() {
		t.Errorf("status after a handshake: %+v, %v", st, err)
	}
}

// A REMOVED PEER IS GONE: it can no longer call home. The control is that the
// same pair called home before the removal.
func TestARemovedPeerCannotCallHome(t *testing.T) {
	server, router := pair(t, "10.249.1.2", "10.249.1.2/32")
	serveHello(t, server)
	if _, err := callHome(router, 10*time.Second); err != nil {
		t.Fatalf("control: the pair could not call home: %v", err)
	}
	st, _ := server.Status()
	if err := server.RemovePeer(st[0].PublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := callHome(router, 3*time.Second); err == nil {
		t.Error("a removed peer could still call home")
	}
}

// A PEER MAY SEND ONLY FROM ITS OWN /32: a device claiming another's address is
// not heard. The control is TestTheTunnelCarriesBothDirections, where the same
// pair with a matching address is.
func TestAPeerCannotSpeakFromAnotherAddress(t *testing.T) {
	server, router := pair(t, "10.249.1.9", "10.249.1.2/32")
	serveHello(t, server)
	if _, err := callHome(router, 3*time.Second); err == nil {
		t.Error("a peer was heard from an address it was not given")
	}
}

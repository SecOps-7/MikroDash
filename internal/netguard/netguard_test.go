package netguard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestOnlyAPublicAddressIsDialled. Every range that is not the public internet
// is refused, IPv4-mapped forms included, and ordinary public addresses pass.
func TestOnlyAPublicAddressIsDialled(t *testing.T) {
	for _, a := range []string{"127.0.0.1:80", "10.1.2.3:443", "192.168.88.1:80", "172.16.0.9:80",
		"169.254.1.1:80", "[::1]:80", "[fe80::1]:80", "[fd00::1]:443", "[::ffff:192.168.1.1]:80",
		"0.0.0.0:80", "224.0.0.1:80",
		// Carrier-grade NAT and Tailscale, this network, IETF assignments,
		// benchmarking, documentation, the reserved block and broadcast, and the
		// IPv6 forms that carry an IPv4 address somewhere else.
		"100.64.0.1:80", "100.127.255.254:443", "0.1.2.3:80", "192.0.0.8:80", "198.18.0.10:80",
		"198.19.255.1:80", "198.51.100.5:80", "203.0.113.9:80", "240.0.0.1:80", "255.255.255.255:80",
		"[64:ff9b::808:808]:443", "[64:ff9b:1::1]:443", "[2002:c000:204::1]:443", "[2001:db8::1]:443"} {
		if PublicOnly("tcp", a, nil) == nil {
			t.Errorf("%s was allowed", a)
		}
	}
	for _, a := range []string{"93.184.215.14:443", "[2606:4700::6810:84e5]:443", "100.63.255.255:80", "198.20.0.1:80"} {
		if err := PublicOnly("tcp", a, nil); err != nil {
			t.Errorf("%s was refused: %v", a, err)
		}
	}
}

// TestTheTransportRefusesALoopbackServer, through a real dial: the hook is
// wired to the transport, not only defined.
func TestTheTransportRefusesALoopbackServer(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer local.Close()
	c := &http.Client{Transport: Transport(time.Second), Timeout: 2 * time.Second}
	_, err := c.Get(local.URL)
	if err == nil || !strings.Contains(err.Error(), "not a public address") {
		t.Fatalf("a server on loopback was reached: %v", err)
	}
}

// TestARouterMayBeOnTheLANButNotOnThisMachine. Private reach, the default for
// a router (GHSA-mcwr-rm79-fr4h), takes the LAN, Tailscale and the public
// internet, and refuses loopback, link-local and the cloud metadata address;
// Public refuses the LAN too; Any takes everything.
func TestARouterMayBeOnTheLANButNotOnThisMachine(t *testing.T) {
	lan := []string{"192.168.88.1:8728", "10.0.0.1:8729", "172.16.4.2:8728", "100.101.102.103:8728",
		"[fd00::1]:8728", "93.184.215.14:8728"}
	here := []string{"127.0.0.1:8728", "[::1]:8728", "169.254.169.254:80", "169.254.1.1:8728",
		"[fe80::1]:8728", "0.0.0.0:8728", "224.0.0.1:8728", "255.255.255.255:8728", "[::ffff:127.0.0.1]:8728"}
	for _, a := range lan {
		if err := Check(a, Private); err != nil {
			t.Errorf("private: %s was refused: %v", a, err)
		}
	}
	for _, a := range here {
		if Check(a, Private) == nil {
			t.Errorf("private: %s was allowed", a)
		}
		if err := Check(a, Any); err != nil {
			t.Errorf("any: %s was refused: %v", a, err)
		}
	}
	if Check("192.168.88.1:8728", Public) == nil {
		t.Error("public: a LAN address was allowed")
	}
	if err := Check("router.example:8728", Private); err != nil {
		t.Errorf("a name was refused by Check, which only judges literals: %v", err)
	}
	for _, s := range []string{"public", "private", "any"} {
		if r, err := ParseReach(s); err != nil || r.String() != s {
			t.Errorf("ParseReach(%q) = %v, %v", s, r, err)
		}
	}
	if _, err := ParseReach("lan"); err == nil {
		t.Error("an unknown reach was accepted")
	}
}

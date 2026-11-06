// Package netguard keeps a connection the server makes on someone's behalf on
// the public internet.
//
// ── CHECKED ON THE ADDRESS THE DIALER CONNECTS TO ───────────────────────────
//
// Not on the URL's host. A name can resolve inward, resolve differently the
// second time it is asked, or be a redirect's destination, and every one of
// those reaches the dialer as an IP address. `PublicOnly` is a `net.Dialer`
// Control hook, so it sees that address after resolution and before the
// connection opens, and nothing that follows a redirect or a DNS answer gets
// round it.
//
// Used wherever a destination is somebody's own text: a DNS list's source
// (administrators), and a personal notification channel or a user's own alert
// settings (anyone signed in). An install channel is not held to it: only an
// administrator may make one, and a self-hosted ntfy, Gotify or mail server on
// the LAN is exactly what one points at.
package netguard

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// local are the ranges that are never a remote device: loopback, link-local
// (cloud metadata services among it), multicast and unspecified are matched
// by the netip predicates below; these are the rest, this network, the
// reserved block and broadcast.
var local = prefixes("0.0.0.0/8", "240.0.0.0/4")

// private are the ranges that are not the public internet but can hold a real
// device: RFC 1918 and ULA (netip's IsPrivate), carrier-grade NAT (Tailscale
// among it), IETF protocol assignments, the documentation and benchmarking
// ranges, and the IPv6 forms that carry an IPv4 address somewhere else (NAT64,
// 6to4) or are for documentation.
var private = prefixes("100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16",
	"2001:db8::/32")

func prefixes(ps ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ps))
	for _, p := range ps {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}

// Reach is how far a connection may go.
type Reach int

const (
	// Public is the public internet only.
	Public Reach = iota
	// Private adds the private ranges, where a router a dashboard watches
	// usually is. Loopback, link-local (cloud metadata) and multicast are still
	// refused.
	Private
	// Any is every address: a router reached through a tunnel on this machine.
	Any
)

// ParseReach reads "public", "private" or "any".
func ParseReach(s string) (Reach, error) {
	switch s {
	case "public":
		return Public, nil
	case "private":
		return Private, nil
	case "any":
		return Any, nil
	}
	return Private, fmt.Errorf("%q is not public, private or any", s)
}

func (r Reach) String() string {
	return [...]string{"public", "private", "any"}[r]
}

// Allowed reports whether an address is within reach, and why not.
func Allowed(ip netip.Addr, reach Reach) error {
	if reach == Any {
		return nil
	}
	ip = ip.Unmap()
	isLocal := ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip == netip.AddrFrom4([4]byte{255, 255, 255, 255})
	for _, p := range local {
		isLocal = isLocal || p.Contains(ip)
	}
	if isLocal && reach == Public {
		return fmt.Errorf("%s is not a public address", ip)
	}
	if isLocal {
		return fmt.Errorf("%s is a local address", ip)
	}
	if reach == Private {
		return nil
	}
	isPrivate := ip.IsPrivate()
	for _, p := range private {
		isPrivate = isPrivate || p.Contains(ip)
	}
	if isPrivate {
		return fmt.Errorf("%s is not a public address", ip)
	}
	return nil
}

// Control returns a net.Dialer Control hook that refuses an address out of
// reach. It sees the address after DNS, so a name cannot get round it.
func Control(reach Reach) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		return check(address, reach)
	}
}

// Check refuses a "host:port" whose host is an IP address out of reach. A
// host that is a name passes: only a dialer's Control hook sees what it
// resolves to, so a caller that dials without one must not rely on this.
func Check(address string, reach Reach) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return nil
	}
	return check(address, reach)
}

func check(address string, reach Reach) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	return Allowed(ip, reach)
}

// PublicOnly refuses a connection to an address that is not a public one. Its
// signature is net.Dialer's Control.
func PublicOnly(network, address string, c syscall.RawConn) error {
	return Control(Public)(network, address, c)
}

// Dialer connects only to public addresses.
func Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: PublicOnly}
}

// Transport is an HTTP transport that connects only to public addresses.
//
// PROXY IS NIL, ON PURPOSE. Through a proxy the dialer would check the proxy's
// address, and the destination would be whatever the proxy was asked for.
func Transport(dialTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DialContext:         Dialer(dialTimeout).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

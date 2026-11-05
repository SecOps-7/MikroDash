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

// notPublic are the ranges that are not the public internet and that the
// netip predicates do not cover: this network, carrier-grade NAT (Tailscale
// among it), IETF protocol assignments, the documentation and benchmarking
// ranges, the reserved block and broadcast, and the IPv6 forms that carry an
// IPv4 address somewhere else (NAT64, 6to4) or are for documentation.
var notPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4",
		"64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001:db8::/32"} {
		out = append(out, netip.MustParsePrefix(p))
	}
	return out
}()

// PublicOnly refuses a connection to an address that is not a public one. Its
// signature is net.Dialer's Control.
func PublicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	ip = ip.Unmap()
	if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("%s is not a public address", ip)
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return fmt.Errorf("%s is not a public address", ip)
		}
	}
	return nil
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

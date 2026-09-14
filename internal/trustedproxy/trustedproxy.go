// Package trustedproxy decides which address a request came from when a reverse
// proxy or a tunnel may sit in front of MikroDash.
//
// ── X-Forwarded-For IS A CLAIM, AND ONLY A TRUSTED PEER MAY MAKE IT ────────────
//
// The server took the first X-Forwarded-For entry from any request. That value
// keys every rate limiter and fills the audit trail, so a client reaching
// MikroDash directly could send a new address per request, rotate past the login
// limiter and write any IP it liked into the trail. Raised on issue #111, where a
// tunnel puts the login page on the internet.
//
// Now the header is read only when the TCP peer is in the operator's trusted
// list, and then FROM THE RIGHT: each proxy appends the address it received the
// request from, so the rightmost entries were written by proxies and the leftmost
// by whoever sent the request. Cloudflare documents exactly that append. The
// client is the first address, walking leftwards, that is not itself trusted.
// Taking the leftmost entry, as before, would still be forgeable behind a proxy.
package trustedproxy

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Parse reads the operator's list: IP addresses or CIDR ranges, separated by
// commas, spaces or new lines. A bare address trusts that address alone.
//
// A /0 IS REFUSED. Trusting every address is the forgeable behaviour this package
// exists to remove, and a single line in a compose file must not be able to
// restore it.
func Parse(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t'
	}) {
		p, err := parsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR range", f)
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("%q would trust every address, which lets any client choose its own IP", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		if p.Addr().Is4In6() && p.Bits() >= 96 {
			return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96), nil
		}
		return p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Client returns the address to attribute a request to. remoteAddr is the TCP
// peer, as net/http reports it; forwardedFor is every X-Forwarded-For header
// value in the order received.
func Client(remoteAddr string, forwardedFor []string, trusted []netip.Prefix) string {
	peerText := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		peerText = h
	}
	peer, err := netip.ParseAddr(peerText)
	if err != nil {
		return strings.TrimPrefix(peerText, "::ffff:")
	}
	peer = peer.Unmap()
	if !contains(trusted, peer) {
		return peer.String()
	}

	var hops []string
	for _, v := range forwardedFor {
		hops = append(hops, strings.Split(v, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a, ok := parseHop(hops[i])
		if !ok {
			// A MALFORMED HOP ENDS THE WALK at the last address a trusted proxy
			// vouched for. Everything to its left is unverifiable.
			break
		}
		client = a
		if !contains(trusted, a) {
			break
		}
	}
	return client.String()
}

func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if h, _, err := net.SplitHostPort(s); err == nil {
		s = h
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func contains(trusted []netip.Prefix, a netip.Addr) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

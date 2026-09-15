package collect

import (
	"errors"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// A FAILED IPV6 READ IS NOT AN EMPTY IPV6 TABLE (#97).
//
// The IPv4 answer drives the collector, and the IPv6 menu is read alongside it.
// Three answers from that second read mean three different things:
//
//	rows              the router's IPv6 addresses
//	a transient error  nothing learned: the last IPv6 rows stay on the page
//	no such command    the router has no IPv6 package: there is nothing to show
//
// Treating the second like the third blanked every IPv6 row for a poll on any
// hiccup, and treating the third like the second would show addresses from a
// package the router no longer has.

type ipv6ScriptReader struct {
	v4    []routeros.Reply
	v6    []routeros.Reply
	v6Err error
}

func (r *ipv6ScriptReader) Connected() bool { return true }
func (r *ipv6ScriptReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	switch cmd.Path {
	case "/ip/address/print":
		return r.v4, nil
	case "/ipv6/address/print":
		if r.v6Err != nil {
			return nil, r.v6Err
		}
		return r.v6, nil
	}
	return nil, nil
}

func ipv6Count(p *IPAddressesPayload) int {
	n := 0
	if p == nil {
		return 0
	}
	for _, a := range p.Addresses {
		if a.Family == "ipv6" {
			n++
		}
	}
	return n
}

func TestATransientIPv6FailureKeepsTheLastIPv6Rows(t *testing.T) {
	r := &ipv6ScriptReader{
		v4: []routeros.Reply{{".id": "*1", "address": "198.51.100.1/24", "interface": "bridge"}},
		v6: []routeros.Reply{{".id": "*A", "address": "2001:db8::1/64", "interface": "bridge"}},
	}
	c := NewIPAddresses(r, hub.Relay{}, 10000)

	c.Tick()
	if got := ipv6Count(c.Last()); got != 1 {
		t.Fatalf("first read: %d IPv6 rows, want 1", got)
	}

	r.v6Err = errors.New("connection reset by peer")
	c.Tick()
	if got := ipv6Count(c.Last()); got != 1 {
		t.Errorf("after a transient IPv6 failure: %d IPv6 rows, want the last 1 kept", got)
	}

	r.v6Err = errors.New("no such command prefix")
	// A changed IPv4 answer too, so the emit is not suppressed as unchanged.
	r.v4 = append(r.v4, routeros.Reply{".id": "*2", "address": "198.51.100.2/24", "interface": "bridge"})
	c.Tick()
	if got := ipv6Count(c.Last()); got != 0 {
		t.Errorf("after the router answered that IPv6 is not there: %d IPv6 rows, want 0", got)
	}
}

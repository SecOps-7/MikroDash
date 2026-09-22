package ztp

import (
	"errors"
	"fmt"
	"net/netip"
)

// The tunnel's address plan, from one IPv4 prefix (default 10.249.0.0/16):
//
//	x.x.0.1        this MikroDash
//	x.x.0.0/24     reserved to the server (nothing else is handed out there)
//	last /24       the enrolment range: a generic script's shared key comes up
//	               at a random address here, and reaches only the enrolment
//	               endpoint, until it has its own
//	everything between: one /32 per device, handed out in order
//
// A /32 per device is also the spoofing rule: WireGuard accepts a packet from a
// peer only from the addresses it was given, so one device cannot claim
// another's.

// Plan is one prefix's addresses.
type Plan struct {
	Prefix netip.Prefix
}

// ParsePlan checks the prefix: IPv4, and room for the server /24, the
// enrolment /24 and at least one device /24 (so a /22 or wider).
func ParsePlan(s string) (Plan, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() {
		return Plan{}, fmt.Errorf("%q is not an IPv4 prefix", s)
	}
	if p.Bits() > 22 {
		return Plan{}, errors.New("the tunnel prefix must be a /22 or wider")
	}
	return Plan{Prefix: p.Masked()}, nil
}

func (p Plan) base() uint32 {
	b := p.Prefix.Addr().As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// size is how many addresses the prefix holds.
func (p Plan) size() uint32 { return 1 << (32 - p.Prefix.Bits()) }

func (p Plan) at(offset uint32) netip.Addr {
	n := p.base() + offset
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

func (p Plan) offset(a netip.Addr) uint32 {
	b := a.As4()
	return (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])) - p.base()
}

// Server is this MikroDash's tunnel address: the prefix's .0.1.
func (p Plan) Server() netip.Addr { return p.at(1) }

// Enrolment is the last /24, where a generic script's shared key lives.
func (p Plan) Enrolment() netip.Prefix {
	return netip.PrefixFrom(p.at(p.size()-256), 24)
}

// NextFree is the first device address not in use: after the server's /24 and
// before the enrolment /24, skipping each /24's .0 and .255 so no address reads
// as a network or a broadcast to a person.
func (p Plan) NextFree(used map[netip.Addr]bool) (netip.Addr, error) {
	for off := uint32(256); off < p.size()-256; off++ {
		if last := off & 0xff; last == 0 || last == 255 {
			continue
		}
		if a := p.at(off); !used[a] {
			return a, nil
		}
	}
	return netip.Addr{}, errors.New("the tunnel prefix has no free device address left")
}

// IsDevice is whether a is an address NextFree could have handed out.
func (p Plan) IsDevice(a netip.Addr) bool {
	if !a.Is4() || !p.Prefix.Contains(a) {
		return false
	}
	off := p.offset(a)
	last := off & 0xff
	return off >= 256 && off < p.size()-256 && last != 0 && last != 255
}

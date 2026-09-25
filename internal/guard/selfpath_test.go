package guard

import (
	"testing"

	"mikrodash/internal/routeros"
)

// Row shapes are the ones the live routers actually return, taken from the
// captured corpus: /user/active carries .id, address, group, name, radius, via,
// when; /ip/address carries address, interface, disabled (and actual-interface,
// which the collectors' proplists omit but this guard reads without one).
func active(rows ...[2]string) []routeros.Reply {
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		out = append(out, routeros.Reply{"name": r[0], "address": r[1], "group": "full", "via": "api"})
	}
	return out
}

func addrs(rows ...[3]string) []routeros.Reply {
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		rr := routeros.Reply{"address": r[0], "interface": r[1], "disabled": "false"}
		if r[2] != "" {
			rr["actual-interface"] = r[2]
		}
		out = append(out, rr)
	}
	return out
}

// The common case on a correctly hardened router: the README's read-only API
// user cannot read /user/active, so the read fails and the caller passes
// nothing. It must produce NO warning — failing closed here would block every
// VLAN and bridge edit on every hardened router.
func TestFailsOpenWhenUserActiveIsDenied(t *testing.T) {
	p := ResolveManagementInterfaces(nil, addrs([3]string{"10.0.0.1/24", "bridge", ""}), []string{"mikrodash"})
	if p.Resolved {
		t.Fatal("resolved from no /user/active rows")
	}
	if v := CheckInterfaceEdit(p, []string{"bridge"}, "delete"); v.Warned() {
		t.Errorf("warned despite an unresolved path: %+v", v)
	}
}

// Containment, not equality: the router holds 10.0.0.1/24 and sees us at
// 10.0.0.5.
func TestMatchesBySubnetContainment(t *testing.T) {
	p := ResolveManagementInterfaces(
		active([2]string{"mikrodash", "10.0.0.5"}),
		addrs([3]string{"10.0.0.1/24", "bridge", "ether2"},
			[3]string{"192.168.88.1/24", "ether5", ""}),
		[]string{"mikrodash"})
	if !p.Resolved {
		t.Fatal("did not resolve")
	}
	if p.Address != "10.0.0.5" {
		t.Errorf("matched address = %q", p.Address)
	}
	// BOTH names: `interface` is the bridge, `actual-interface` the port behind
	// it, and an edit may name either.
	if len(p.Interfaces) != 2 || !contains(p.Interfaces, "bridge") || !contains(p.Interfaces, "ether2") {
		t.Errorf("interfaces = %v, want both bridge and ether2", p.Interfaces)
	}
}

// We know where the router sees us from, but no configured prefix contains it —
// we arrive over a route. No single interface is "the" management interface,
// which is wanGuard's question rather than this one.
func TestOffSubnetDoesNotResolve(t *testing.T) {
	p := ResolveManagementInterfaces(
		active([2]string{"mikrodash", "203.0.113.9"}),
		addrs([3]string{"10.0.0.1/24", "bridge", ""}),
		[]string{"mikrodash"})
	if p.Resolved {
		t.Error("resolved an address no prefix contains")
	}
	if p.Address != "203.0.113.9" {
		t.Errorf("address = %q, want it carried through for the caller", p.Address)
	}
}

// Several logins per router, and they need not share a source address.
func TestCollectsEveryLoginsAddress(t *testing.T) {
	p := ResolveManagementInterfaces(
		active([2]string{"mikrodash", "10.0.0.5"},
			[2]string{"MikroDash", "10.0.0.6"}, // name match is case-insensitive
			[2]string{"someone-else", "10.0.0.7"}),
		addrs([3]string{"10.0.0.1/24", "bridge", ""}),
		[]string{"mikrodash"})
	if len(p.Addresses) != 2 {
		t.Errorf("addresses = %v, want both of ours and not the third party's", p.Addresses)
	}
	if contains(p.Addresses, "10.0.0.7") {
		t.Error("collected an address belonging to another user")
	}
}

func TestWarnsOnlyWhenTheEditTouchesOurInterface(t *testing.T) {
	p := ResolveManagementInterfaces(
		active([2]string{"mikrodash", "10.0.0.5"}),
		addrs([3]string{"10.0.0.1/24", "bridge", "ether2"}),
		[]string{"mikrodash"})

	if v := CheckInterfaceEdit(p, []string{"ether9"}, "delete"); v.Warned() {
		t.Error("warned about an interface that is not ours")
	}
	v := CheckInterfaceEdit(p, []string{"ether2"}, "delete")
	if !v.Warned() || v.Code != "self-cutoff" {
		t.Fatalf("no warning for our own port: %+v", v)
	}
	if v.Detail["interface"] != "ether2" || v.Detail["address"] != "10.0.0.5" {
		t.Errorf("detail = %v; it must name what the reader needs to judge it", v.Detail)
	}
	if v.Fingerprint == "" {
		t.Error("a warning with no fingerprint cannot be acknowledged")
	}
	// RouterOS is case sensitive here; the guard over-matches on purpose.
	if !CheckInterfaceEdit(p, []string{"ETHER2"}, "update").Warned() {
		t.Error("case difference defeated the guard")
	}
}

// The acknowledgement is bound to the exact inputs, so it cannot be carried
// from one row to another or replayed against a different write.
func TestFingerprintBindsToTheInputs(t *testing.T) {
	p := ResolveManagementInterfaces(
		active([2]string{"mikrodash", "10.0.0.5"}),
		addrs([3]string{"10.0.0.1/24", "bridge", "ether2"}),
		[]string{"mikrodash"})

	a := CheckInterfaceEdit(p, []string{"ether2"}, "delete").Fingerprint
	if b := CheckInterfaceEdit(p, []string{"ether2"}, "delete").Fingerprint; a != b {
		t.Error("fingerprint is not stable across identical inputs")
	}
	if b := CheckInterfaceEdit(p, []string{"ether2"}, "update").Fingerprint; a == b {
		t.Error("delete and update share a fingerprint; an ack for one would clear the other")
	}
	if b := CheckInterfaceEdit(p, []string{"bridge"}, "delete").Fingerprint; a == b {
		t.Error("two different targets share a fingerprint")
	}
}

// IPv6 arrives through the same path and must not silently fail to match.
func TestIPv6Containment(t *testing.T) {
	p := ResolveManagementInterfaces(
		active([2]string{"mikrodash", "2001:db8::5"}),
		addrs([3]string{"2001:db8::1/64", "bridge", ""}),
		[]string{"mikrodash"})
	if !p.Resolved || !contains(p.Interfaces, "bridge") {
		t.Errorf("IPv6 management address did not resolve: %+v", p)
	}
}

// A malformed row must cost the warning, never the write.
func TestGarbageRowsAreIgnored(t *testing.T) {
	p := ResolveManagementInterfaces(
		active([2]string{"mikrodash", "not-an-ip"}),
		addrs([3]string{"also-not-a-prefix", "bridge", ""}),
		[]string{"mikrodash"})
	if p.Resolved {
		t.Errorf("resolved from unparseable rows: %+v", p)
	}
}

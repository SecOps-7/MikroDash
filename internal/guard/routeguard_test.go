package guard

import "testing"

func route(dst string, disabled bool) RouteChange {
	return RouteChange{Present: true, Dst: dst, Gateway: "10.0.0.1", Distance: "1", Table: "main", Disabled: disabled}
}

// The management address arrives from 203.0.113.50, which no configured subnet
// holds, so its return path is a route.
var (
	offSubnet = active([2]string{"mikrodash", "203.0.113.50"})
	onSubnet  = active([2]string{"mikrodash", "10.0.0.5"})
	lan       = addrs([3]string{"10.0.0.1/24", "bridge", "bridge"})
	us        = []string{"mikrodash"}
)

func TestRemovingTheDefaultRouteWarnsWhenWeArriveOverIt(t *testing.T) {
	v := CheckRouteEdit(offSubnet, lan, us, "delete", route("0.0.0.0/0", false), RouteChange{})
	if !v.Warned() || v.Code != "route-cutoff" {
		t.Fatalf("deleting the default route under an off-subnet session: %+v", v)
	}
	if v.Detail["address"] != "203.0.113.50" || v.Detail["destination"] != "0.0.0.0/0" {
		t.Errorf("detail %v does not name the address and the route", v.Detail)
	}
}

func TestAConnectedManagementAddressNeedsNoRoute(t *testing.T) {
	if v := CheckRouteEdit(onSubnet, lan, us, "delete", route("0.0.0.0/0", false), RouteChange{}); v.Warned() {
		t.Errorf("a session on a connected subnet was warned about a route: %+v", v)
	}
}

func TestAnUnrelatedRouteIsQuiet(t *testing.T) {
	if v := CheckRouteEdit(offSubnet, lan, us, "delete", route("198.51.100.0/24", false), RouteChange{}); v.Warned() {
		t.Errorf("a route not covering the management address warned: %+v", v)
	}
}

func TestANewMoreSpecificRouteWarns(t *testing.T) {
	v := CheckRouteEdit(offSubnet, lan, us, "create", RouteChange{}, route("203.0.113.0/24", false))
	if !v.Warned() || v.Code != "route-cutoff" {
		t.Errorf("adding a route that captures the management address: %+v", v)
	}
}

func TestACommentOnlyEditIsQuiet(t *testing.T) {
	r := route("0.0.0.0/0", false)
	if v := CheckRouteEdit(offSubnet, lan, us, "update", r, r); v.Warned() {
		t.Errorf("an edit that changes nothing about forwarding warned: %+v", v)
	}
}

func TestADisabledRouteIsQuietUntilEnabled(t *testing.T) {
	before, after := route("0.0.0.0/0", true), route("0.0.0.0/0", true)
	after.Gateway = "10.0.0.2"
	if v := CheckRouteEdit(offSubnet, lan, us, "update", before, after); v.Warned() {
		t.Errorf("a route disabled on both sides warned: %+v", v)
	}
	enabled := route("0.0.0.0/0", false)
	if v := CheckRouteEdit(offSubnet, lan, us, "update", before, enabled); !v.Warned() {
		t.Error("enabling a route that covers the management address did not warn")
	}
}

// WHEN THE ROUTER'S VIEW OF US CANNOT BE READ, a forwarding change warns that the
// guard cannot tell, rather than claiming it is safe. A comment edit still does
// not, because it cannot move a packet.
func TestAnUnreadableManagementPathWarnsThatItCannotTell(t *testing.T) {
	v := CheckRouteEdit(nil, lan, us, "delete", route("0.0.0.0/0", false), RouteChange{})
	if !v.Warned() || v.Code != "route-cutoff-unknown" {
		t.Errorf("an undeterminable path: %+v", v)
	}
	r := route("0.0.0.0/0", false)
	if v := CheckRouteEdit(nil, lan, us, "update", r, r); v.Warned() {
		t.Errorf("a comment edit warned with an undeterminable path: %+v", v)
	}
}

func TestIPv6RoutesAreJudgedToo(t *testing.T) {
	v6 := active([2]string{"mikrodash", "2001:db8:ffff::10"})
	v6lan := addrs([3]string{"2001:db8:1::1/64", "bridge", "bridge"})
	if v := CheckRouteEdit(v6, v6lan, us, "delete", route("::/0", false), RouteChange{}); !v.Warned() {
		t.Error("deleting the IPv6 default route under an off-subnet IPv6 session did not warn")
	}
}

func TestTheRouteFingerprintBindsToTheInputs(t *testing.T) {
	a := CheckRouteEdit(offSubnet, lan, us, "delete", route("0.0.0.0/0", false), RouteChange{})
	b := CheckRouteEdit(offSubnet, lan, us, "delete", route("203.0.113.0/24", false), RouteChange{})
	if a.Fingerprint == "" || a.Fingerprint == b.Fingerprint {
		t.Errorf("fingerprints do not tell two different routes apart: %q vs %q", a.Fingerprint, b.Fingerprint)
	}
}

package guard

import (
	"testing"

	"mikrodash/internal/routeros"
)

func pol(dst, action, peer string) IPsecPolicy {
	return IPsecPolicy{Present: true, Dst: dst, Protocol: "all", Action: action, Peer: peer}
}

// A POLICY THAT SWALLOWS OUR ADDRESS warns; the ordinary site-to-site policy
// for the far LAN — the control — does not.
func TestAPolicyCoveringUsWarnsAndASiteToSiteOneDoesNot(t *testing.T) {
	v := CheckIPsecPolicyEdit(onSubnet, us, "create", IPsecPolicy{}, pol("0.0.0.0/0", "encrypt", "far"))
	if !v.Warned() || v.Code != "ipsec-cutoff" || v.Detail["address"] != "10.0.0.5" {
		t.Fatalf("an encrypt policy for 0.0.0.0/0: %+v", v)
	}
	if v := CheckIPsecPolicyEdit(onSubnet, us, "create", IPsecPolicy{}, pol("203.0.113.0/24", "encrypt", "far")); v.Warned() {
		t.Errorf("a site-to-site policy for another network warned: %+v", v)
	}
}

// A policy the router's replies cannot take: `none` (bypass), a template, a
// disabled one, and one for a protocol the API does not use.
func TestPoliciesThatCannotCarryUsAreQuiet(t *testing.T) {
	tmpl := pol("0.0.0.0/0", "encrypt", "")
	tmpl.Template = true
	off := pol("0.0.0.0/0", "encrypt", "far")
	off.Disabled = true
	udp := pol("0.0.0.0/0", "encrypt", "far")
	udp.Protocol = "udp"
	for _, p := range []IPsecPolicy{pol("0.0.0.0/0", "none", ""), tmpl, off, udp} {
		if v := CheckIPsecPolicyEdit(onSubnet, us, "create", IPsecPolicy{}, p); v.Warned() {
			t.Errorf("%+v warned: %+v", p, v)
		}
	}
}

// Removing the policy MikroDash arrives through warns, as adding one does; an
// unchanged policy does not.
func TestRemovingThePolicyWeArriveThroughWarns(t *testing.T) {
	carries := pol("10.0.0.0/24", "encrypt", "hq")
	if v := CheckIPsecPolicyEdit(onSubnet, us, "delete", carries, IPsecPolicy{}); !v.Warned() {
		t.Error("deleting the policy that covers us did not warn")
	}
	if v := CheckIPsecPolicyEdit(onSubnet, us, "update", carries, carries); v.Warned() {
		t.Errorf("an unchanged policy warned: %+v", v)
	}
	if v := CheckIPsecPolicyEdit(nil, us, "create", IPsecPolicy{}, pol("198.51.100.0/24", "discard", "")); v.Code != "ipsec-cutoff-unknown" {
		t.Errorf("an unreadable management address: %+v", v)
	}
}

// THE PEER AND ITS IDENTITY carry MikroDash when a policy that encrypts through
// the peer covers its address. The control is a peer only other networks go
// through, and a comment-only edit.
func TestAPeerCarryingUsWarnsOnChange(t *testing.T) {
	policies := []routeros.Reply{
		{"dst-address": "10.0.0.0/24", "action": "encrypt", "peer": "hq", "protocol": "all"},
		{"dst-address": "203.0.113.0/24", "action": "encrypt", "peer": "branch", "protocol": "all"},
	}
	v := CheckIPsecPeerEdit(onSubnet, us, policies, "delete", "hq", true)
	if !v.Warned() || v.Code != "ipsec-peer-cutoff" || v.Detail["peer"] != "hq" {
		t.Fatalf("removing the peer we arrive through: %+v", v)
	}
	if v := CheckIPsecPeerEdit(onSubnet, us, policies, "delete", "branch", true); v.Warned() {
		t.Errorf("a peer only another network uses warned: %+v", v)
	}
	if v := CheckIPsecPeerEdit(onSubnet, us, policies, "update", "hq", false); v.Warned() {
		t.Errorf("a comment-only edit warned: %+v", v)
	}
}

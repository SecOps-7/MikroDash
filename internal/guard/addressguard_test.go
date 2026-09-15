package guard

import "testing"

// THE ADDRESS MIKRODASH REACHES THE ROUTER ON WARNS BEFORE IT CHANGES (#97).
//
// `onSubnet` arrives from 10.0.0.5, so 10.0.0.1/24 is the address the router
// answers MikroDash on. `active` and `us` are shared with routeguard_test.go.

func addr(address string, disabled bool) AddressChange {
	return AddressChange{Present: true, Address: address, Interface: "bridge", Disabled: disabled}
}

func TestRemovingTheAddressWeArriveOnWarns(t *testing.T) {
	v := CheckAddressEdit(onSubnet, us, "delete", addr("10.0.0.1/24", false), AddressChange{})
	if !v.Warned() || v.Code != "address-cutoff" {
		t.Fatalf("removing 10.0.0.1/24 under a session from 10.0.0.5: %+v", v)
	}
	if v.Detail["address"] != "10.0.0.5" || v.Detail["prefix"] != "10.0.0.1/24" || v.Detail["action"] != "delete" {
		t.Errorf("detail %v does not name where MikroDash is, the address and the action", v.Detail)
	}
}

func TestDisablingOrMovingTheAddressWeArriveOnWarns(t *testing.T) {
	if v := CheckAddressEdit(onSubnet, us, "update", addr("10.0.0.1/24", false), addr("10.0.0.1/24", true)); !v.Warned() {
		t.Errorf("disabling the address MikroDash arrives on was quiet: %+v", v)
	}
	if v := CheckAddressEdit(onSubnet, us, "update", addr("10.0.0.1/24", false), addr("10.9.0.1/24", false)); !v.Warned() {
		t.Errorf("moving it to another subnet was quiet: %+v", v)
	}
	moved := addr("10.0.0.1/24", false)
	moved.Interface = "ether2"
	if v := CheckAddressEdit(onSubnet, us, "update", addr("10.0.0.1/24", false), moved); !v.Warned() {
		t.Errorf("moving it to another interface was quiet: %+v", v)
	}
}

func TestAnAddressChangeThatCutsNothingIsQuiet(t *testing.T) {
	cases := map[string]Verdict{
		"a comment-only edit": CheckAddressEdit(onSubnet, us, "update", addr("10.0.0.1/24", false), addr("10.0.0.1/24", false)),
		"another subnet":      CheckAddressEdit(onSubnet, us, "delete", addr("198.51.100.1/24", false), AddressChange{}),
		"a create":            CheckAddressEdit(onSubnet, us, "create", AddressChange{}, addr("10.0.0.2/24", false)),
		"an address that was already disabled": CheckAddressEdit(onSubnet, us, "delete",
			addr("10.0.0.1/24", true), AddressChange{}),
	}
	for name, v := range cases {
		if v.Warned() {
			t.Errorf("%s warned: %+v", name, v)
		}
	}
}

func TestAnUnreadableSessionWarnsItCannotTell(t *testing.T) {
	v := CheckAddressEdit(nil, us, "delete", addr("10.0.0.1/24", false), AddressChange{})
	if !v.Warned() || v.Code != "address-cutoff-unknown" {
		t.Errorf("with /user/active unreadable, removing a live address gave %+v, want address-cutoff-unknown", v)
	}
	if v := CheckAddressEdit(nil, us, "update", addr("10.0.0.1/24", false), addr("10.0.0.1/24", false)); v.Warned() {
		t.Errorf("a comment-only edit warned without a readable session: %+v", v)
	}
}

func TestAnIPv6AddressWeArriveOnWarns(t *testing.T) {
	v6 := active([2]string{"mikrodash", "2001:db8::50"})
	if v := CheckAddressEdit(v6, us, "delete", addr("2001:db8::1/64", false), AddressChange{}); !v.Warned() || v.Code != "address-cutoff" {
		t.Errorf("removing 2001:db8::1/64 under a session from 2001:db8::50: %+v", v)
	}
	if v := CheckAddressEdit(v6, us, "delete", addr("2001:db8:1::1/64", false), AddressChange{}); v.Warned() {
		t.Errorf("an IPv6 address on another prefix warned: %+v", v)
	}
}

func TestAnAddressAcknowledgementIsBoundToItsInputs(t *testing.T) {
	a := CheckAddressEdit(onSubnet, us, "delete", addr("10.0.0.1/24", false), AddressChange{})
	b := CheckAddressEdit(onSubnet, us, "update", addr("10.0.0.1/24", false), addr("10.0.0.1/24", true))
	c := CheckAddressEdit(onSubnet, us, "delete", addr("10.0.0.2/24", false), AddressChange{})
	if a.Fingerprint == "" || a.Fingerprint == b.Fingerprint || a.Fingerprint == c.Fingerprint {
		t.Errorf("fingerprints do not tell the writes apart: %q %q %q", a.Fingerprint, b.Fingerprint, c.Fingerprint)
	}
}

package guard

import "testing"

// The hAP AC2's own case: dialled at 10.0.0.53, which its client on ether1 holds.
var bound = DHCPClientChange{Present: true, Interface: "ether1", Address: "10.0.0.53/24"}

func TestRemovingTheClientThatHoldsOurAddressWarns(t *testing.T) {
	for name, after := range map[string]DHCPClientChange{
		"delete":  {},
		"disable": {Present: true, Disabled: true, Interface: "ether1", Address: "10.0.0.53/24"},
		"move":    {Present: true, Interface: "ether2"},
	} {
		act := "update"
		if name == "delete" {
			act = "delete"
		}
		v := CheckDHCPClientEdit([]string{"10.0.0.53"}, act, bound, after)
		if !v.Warned() || v.Code != "dhcp-client-cutoff" || v.Detail["address"] != "10.0.0.53" || v.Detail["interface"] != "ether1" {
			t.Errorf("%s of the client holding our address: %+v", name, v)
		}
	}
}

// The controls: a client holding another address, an edit that keeps the
// client where it is, a client that holds nothing, and a host that did not
// resolve.
func TestAClientWeDoNotDialIsQuiet(t *testing.T) {
	if v := CheckDHCPClientEdit([]string{"192.0.2.9"}, "delete", bound, DHCPClientChange{}); v.Warned() {
		t.Errorf("a client holding an address we do not dial warned: %+v", v)
	}
	if v := CheckDHCPClientEdit([]string{"10.0.0.53"}, "update", bound, bound); v.Warned() {
		t.Errorf("an edit that keeps the client in place warned: %+v", v)
	}
	unbound := DHCPClientChange{Present: true, Interface: "ether1"}
	if v := CheckDHCPClientEdit([]string{"10.0.0.53"}, "delete", unbound, DHCPClientChange{}); v.Warned() {
		t.Errorf("a client holding nothing warned: %+v", v)
	}
	if v := CheckDHCPClientEdit(nil, "delete", bound, DHCPClientChange{}); v.Warned() {
		t.Errorf("an unresolved host warned; the guard fails open: %+v", v)
	}
}

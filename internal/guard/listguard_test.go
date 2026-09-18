package guard

import "testing"

// The default MikroTik firewall's last input rule, and an accept on a
// management address list: the two shapes a list-membership lockout takes.
var (
	defconfDrop = ListRule{ID: "*9", Match: "!LAN",
		Rule: FWRule{Chain: "input", Action: "drop"}}
	mgmtAccept = ListRule{ID: "*3", Match: "mgmt",
		Rule: FWRule{Chain: "input", Action: "accept", Protocol: "tcp", DstPort: "8729"}}
	blockDrop = ListRule{ID: "*4", Match: "blocklist",
		Rule: FWRule{Chain: "input", Action: "drop"}}
)

func ifCtx() FWContext {
	return FWContext{Resolved: true, Addresses: []string{"198.51.100.5"}, Interfaces: []string{"ether2"}, APIPort: 8729}
}

func member(list, value string) ListMember {
	return ListMember{Present: true, List: list, Value: value}
}

var gone = ListMember{}

func TestRemovingTheManagementPortFromLANWarns(t *testing.T) {
	v := CheckListMember(ifCtx(), "interface", []ListRule{defconfDrop}, "delete", member("LAN", "ether2"), gone)
	if !v.Warned() || v.Code != "list-cutoff" || v.Detail["effect"] != "starts-drop" || v.Detail["list"] != "LAN" || v.Detail["move"] != "leaves" {
		t.Fatalf("removing the management port from LAN under `!LAN drop`: %+v", v)
	}
	if v.Fingerprint == "" {
		t.Error("a warning without a fingerprint cannot be acknowledged")
	}
}

// Every way a member stops counting leaves the list: disabling it, moving it to
// another list, or pointing it at another interface.
func TestEveryWayOutOfTheListWarns(t *testing.T) {
	before := member("LAN", "ether2")
	for name, after := range map[string]ListMember{
		"disabled":          {Present: true, List: "LAN", Value: "ether2", Disabled: true},
		"moved to WAN":      member("WAN", "ether2"),
		"another interface": member("LAN", "ether3"),
	} {
		if v := CheckListMember(ifCtx(), "interface", []ListRule{defconfDrop}, "update", before, after); !v.Warned() {
			t.Errorf("%s: no warning", name)
		}
	}
}

// THE CONTROLS: each changes one thing that makes the rule unable to flip on us.
func TestWhatCannotCutUsStaysQuiet(t *testing.T) {
	ctx := ifCtx()
	cases := map[string]struct {
		rules         []ListRule
		before, after ListMember
		ctx           FWContext
	}{
		"not our interface": {[]ListRule{defconfDrop}, member("LAN", "ether3"), gone, ctx},
		"comment only":      {[]ListRule{defconfDrop}, member("LAN", "ether2"), member("LAN", "ether2"), ctx},
		"rule disabled": {[]ListRule{{ID: "*9", Match: "!LAN", Rule: FWRule{Chain: "input", Action: "drop", Disabled: true}}},
			member("LAN", "ether2"), gone, ctx},
		"forward chain": {[]ListRule{{ID: "*9", Match: "!LAN", Rule: FWRule{Chain: "forward", Action: "drop"}}},
			member("LAN", "ether2"), gone, ctx},
		"another port": {[]ListRule{{ID: "*9", Match: "!LAN", Rule: FWRule{Chain: "input", Action: "drop", Protocol: "tcp", DstPort: "80"}}},
			member("LAN", "ether2"), gone, ctx},
		"another list": {[]ListRule{{ID: "*9", Match: "!IOT", Rule: FWRule{Chain: "input", Action: "drop"}}},
			member("LAN", "ether2"), gone, ctx},
		"a log rule": {[]ListRule{{ID: "*9", Match: "!LAN", Rule: FWRule{Chain: "input", Action: "log"}}},
			member("LAN", "ether2"), gone, ctx},
		// Joining LAN under `!LAN drop` makes the rule stop matching: safer, not a cut.
		"joining the protected list": {[]ListRule{defconfDrop}, gone, member("LAN", "ether2"), ctx},
		// FAIL OPEN, as fwGuard does.
		"path unresolved": {[]ListRule{defconfDrop}, member("LAN", "ether2"), gone, FWContext{}},
	}
	for name, c := range cases {
		if v := CheckListMember(c.ctx, "interface", c.rules, "update", c.before, c.after); v.Level != "none" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

func TestAddressListsBothWays(t *testing.T) {
	ctx := ifCtx()
	rules := []ListRule{mgmtAccept, blockDrop}

	// Removing the prefix that covers us from the list an accept rule matches.
	if v := CheckListMember(ctx, "address", rules, "delete", member("mgmt", "198.51.100.0/24"), gone); !v.Warned() || v.Detail["effect"] != "loses-accept" {
		t.Errorf("removing our prefix from mgmt: %+v", v)
	}
	// Putting our own address on a list a drop rule matches.
	if v := CheckListMember(ctx, "address", rules, "create", gone, member("blocklist", "198.51.100.5")); !v.Warned() || v.Detail["effect"] != "starts-drop" {
		t.Errorf("blocking our own address: %+v", v)
	}
	// A range is an address-list form the firewall's own clause does not take.
	if v := CheckListMember(ctx, "address", rules, "create", gone, member("blocklist", "198.51.100.1-198.51.100.9")); !v.Warned() {
		t.Errorf("a range covering us on the blocklist: %+v", v)
	}
	// Controls: someone else's address, a range that misses, a DNS name.
	for _, value := range []string{"198.51.100.77", "198.51.100.10-198.51.100.20", "example.com"} {
		if v := CheckListMember(ctx, "address", rules, "create", gone, member("blocklist", value)); v.Level != "none" {
			t.Errorf("blocking %s warned: %+v", value, v)
		}
	}
	// Unblocking is never a cut.
	if v := CheckListMember(ctx, "address", rules, "delete", member("blocklist", "198.51.100.5"), gone); v.Level != "none" {
		t.Errorf("removing our address from the blocklist warned: %+v", v)
	}
}

// The acknowledgement is bound to its inputs: the same change gives the same
// fingerprint, and a different rule gives another.
func TestTheListFingerprintIsBoundToItsInputs(t *testing.T) {
	a := CheckListMember(ifCtx(), "interface", []ListRule{defconfDrop}, "delete", member("LAN", "ether2"), gone)
	b := CheckListMember(ifCtx(), "interface", []ListRule{defconfDrop}, "delete", member("LAN", "ether2"), gone)
	other := ListRule{ID: "*10", Match: "!LAN", Rule: FWRule{Chain: "input", Action: "drop"}}
	c := CheckListMember(ifCtx(), "interface", []ListRule{other}, "delete", member("LAN", "ether2"), gone)
	if a.Fingerprint != b.Fingerprint {
		t.Error("the same change gave two fingerprints")
	}
	if a.Fingerprint == c.Fingerprint {
		t.Error("a different rule gave the same fingerprint, so one acknowledgement would carry to another")
	}
}

// A LIST'S DEFINITION: deleting, renaming or re-including `LAN` moves members as
// surely as removing one, so each warns while `!LAN drop` matches us; a comment,
// a list no rule names, and a new list do not.
func TestRedefiningAListARuleMatchesWarns(t *testing.T) {
	lan := ListDef{Present: true, Name: "LAN"}
	rules := []ListRule{defconfDrop}
	for name, after := range map[string]ListDef{
		"delete":   {},
		"rename":   {Present: true, Name: "LAN-old"},
		"redefine": {Present: true, Name: "LAN", Exclude: "dynamic"},
	} {
		v := CheckListDefinition(ifCtx(), rules, "update", lan, after)
		if !v.Warned() || v.Code != "list-redefine" || v.Detail["change"] != name {
			t.Errorf("%s LAN: %+v", name, v)
		}
	}
	for name, c := range map[string]struct {
		before, after ListDef
		ctx           FWContext
	}{
		"comment only":         {lan, lan, ifCtx()},
		"a list no rule names": {ListDef{Present: true, Name: "IOT"}, ListDef{}, ifCtx()},
		"a new list":           {ListDef{}, ListDef{Present: true, Name: "LAN"}, ifCtx()},
		"path unresolved":      {lan, ListDef{}, FWContext{}},
	} {
		if v := CheckListDefinition(c.ctx, rules, "update", c.before, c.after); v.Level != "none" {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

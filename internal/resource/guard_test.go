package resource

import "testing"

// GuardTargets decides whether a write is even worth asking the guard about.
// Getting it wrong in one direction warns about harmless edits until people
// click through without reading; wrong in the other, it stays silent on the one
// that cuts the link.
func TestGuardTargetsOnlyForEditsThatCanCut(t *testing.T) {
	before := map[string]string{"name": "Home", "vlan-id": "5", "interface": "Bridge", "disabled": "false"}
	keep := map[string]string{"name": "Home", "vlanId": "5", "interface": "Bridge", "disabled": "no"}

	cases := []struct {
		what   string
		action string
		values map[string]string
		want   bool // are there targets, i.e. is the guard consulted
	}{
		{"a comment-only edit", "update",
			merge(keep, map[string]string{"comment": "note"}), false},
		{"an MTU change", "update",
			merge(keep, map[string]string{"mtu": "1400"}), false},
		{"renaming it", "update",
			merge(keep, map[string]string{"name": "Home2"}), true},
		{"disabling it", "update",
			merge(keep, map[string]string{"disabled": "yes"}), true},
		{"deleting it", "delete", nil, true},
		// A create cuts nothing that already exists.
		{"creating a new one", "update", keep, false},
	}
	for _, c := range cases {
		b := before
		if c.what == "creating a new one" {
			b = nil
		}
		got := len(Vlan.GuardTargets(c.action, c.values, b)) > 0
		if got != c.want {
			t.Errorf("%s: guard consulted = %v, want %v", c.what, got, c.want)
		}
	}
}

// The VLAN's guard names the VLAN ITSELF and deliberately not its parent: our
// address sitting on `Bridge` would otherwise make every VLAN riding that
// bridge warn, and a warning that fires on the innocent case is one people
// learn to click through.
func TestVlanGuardNamesTheVlanNotItsParent(t *testing.T) {
	before := map[string]string{"name": "Home", "interface": "Bridge", "disabled": "false"}
	targets := Vlan.GuardTargets("delete", nil, before)
	if !has(targets, "Home") {
		t.Errorf("targets %v do not name the VLAN itself", targets)
	}
	if has(targets, "Bridge") {
		t.Errorf("targets %v name the PARENT bridge; every VLAN on it would warn", targets)
	}
}

// bridgePort names both, because pulling a port out of a bridge cuts the link
// whichever of the two the operator was editing.
func TestBridgePortGuardNamesBoth(t *testing.T) {
	before := map[string]string{"interface": "ether2", "bridge": "Bridge", "disabled": "false"}
	targets := BridgePort.GuardTargets("delete", nil, before)
	if !has(targets, "ether2") || !has(targets, "Bridge") {
		t.Errorf("targets %v must name both the port and its bridge", targets)
	}
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func has(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

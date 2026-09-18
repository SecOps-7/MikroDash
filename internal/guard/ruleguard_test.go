package guard

import "testing"

func rule(dst, action, table string) RuleChange {
	return RuleChange{Present: true, Dst: dst, Action: action, Table: table}
}

// THE ORDINARY POLICY ROUTE is the dangerous one: "send the LAN out through
// ISP2" with no destination matches the router's own replies to MikroDash too.
func TestAPolicyRuleWithNoDestinationWarns(t *testing.T) {
	r := RuleChange{Present: true, Src: "10.0.0.0/24", Action: "lookup-only-in-table", Table: "isp2"}
	v := CheckRuleEdit(onSubnet, us, "create", RuleChange{}, r)
	if !v.Warned() || v.Code != "rule-cutoff" || v.Detail["address"] != "10.0.0.5" {
		t.Fatalf("a no-destination lookup rule in another table: %+v", v)
	}
	if v.Detail["table"] != "isp2" || v.Detail["destination"] != "any destination" {
		t.Errorf("detail %v does not name the table and the destination", v.Detail)
	}
}

func TestADropOnOurAddressWarnsAndOneElsewhereDoesNot(t *testing.T) {
	if v := CheckRuleEdit(offSubnet, us, "create", RuleChange{}, rule("203.0.113.0/24", "unreachable", "")); !v.Warned() {
		t.Errorf("unreachable on the management address did not warn: %+v", v)
	}
	// THE CONTROL: the same rule for a destination MikroDash is not at.
	if v := CheckRuleEdit(offSubnet, us, "create", RuleChange{}, rule("198.51.100.0/24", "unreachable", "")); v.Warned() {
		t.Errorf("unreachable on an unrelated destination warned: %+v", v)
	}
}

// A rule the router's replies cannot match: one that looks up main (what
// happens anyway), one needing a routing mark, one naming an incoming
// interface, and one that is disabled on both sides.
func TestRulesThatCannotMatchOurRepliesAreQuiet(t *testing.T) {
	quiet := []RuleChange{
		rule("", "lookup", "main"),
		{Present: true, Mark: "isp2", Action: "lookup-only-in-table", Table: "isp2"},
		{Present: true, Interface: "ether2", Action: "drop"},
	}
	for _, r := range quiet {
		if v := CheckRuleEdit(onSubnet, us, "create", RuleChange{}, r); v.Warned() {
			t.Errorf("%+v warned: %+v", r, v)
		}
	}
	off := rule("", "drop", "")
	off.Disabled = true
	on := off
	on.Src = "10.9.9.0/24"
	if v := CheckRuleEdit(onSubnet, us, "update", off, on); v.Warned() {
		t.Errorf("a rule disabled before and after warned: %+v", v)
	}
}

// Enabling, disabling and removing a steering rule all change the path; a
// comment-only edit does not.
func TestEnablingOrRemovingASteeringRuleWarns(t *testing.T) {
	on := rule("", "unreachable", "")
	off := on
	off.Disabled = true
	for name, c := range map[string][2]RuleChange{"enable": {off, on}, "disable": {on, off}, "delete": {on, {}}} {
		act := "update"
		if name == "delete" {
			act = "delete"
		}
		if v := CheckRuleEdit(onSubnet, us, act, c[0], c[1]); !v.Warned() {
			t.Errorf("%s of a steering rule did not warn", name)
		}
	}
	if v := CheckRuleEdit(onSubnet, us, "update", on, on); v.Warned() {
		t.Errorf("an unchanged rule warned: %+v", v)
	}
}

// Where MikroDash connects from cannot be read: a steering change warns that the
// guard cannot tell, rather than passing it as safe.
func TestARuleWarnsWhenOurAddressIsUnknown(t *testing.T) {
	v := CheckRuleEdit(nil, us, "create", RuleChange{}, rule("198.51.100.0/24", "drop", ""))
	if !v.Warned() || v.Code != "rule-cutoff-unknown" {
		t.Errorf("an unreadable management address: %+v", v)
	}
}

package guard

import "testing"

var liveTable = TableState{Present: true, FIB: true}

// The rules as the CHR printed them: one by name, one by the table's id (a rule
// over a table whose FIB was unset), one disabled, one on another table.
var tableRules = []TableRule{
	{ID: "*1", Table: "isp2"},
	{ID: "*2", Table: "*400"},
	{ID: "*3", Table: "isp2", Disabled: true},
	{ID: "*4", Table: "main"},
}

func TestTakingATableOutOfServiceWarnsWhenRulesUseIt(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		after  TableState
		change string
	}{
		{"remove", "delete", TableState{}, "remove"},
		{"disable", "update", TableState{Present: true, FIB: true, Disabled: true}, "disable"},
		{"unset fib", "update", TableState{Present: true}, "fib"},
	} {
		v := CheckTableChange(tc.action, "isp2", "*400", liveTable, tc.after, tableRules)
		if !v.Warned() || v.Code != "table-in-use" {
			t.Errorf("%s: verdict %+v, want a table-in-use warning", tc.name, v)
			continue
		}
		// *1 by name and *2 by id; the disabled rule and main's rule do not count.
		if v.Detail["rules"] != 2 || v.Detail["change"] != tc.change || v.Detail["table"] != "isp2" {
			t.Errorf("%s: detail %v, want 2 rules, change %s", tc.name, v.Detail, tc.change)
		}
		if v.Fingerprint == "" {
			t.Errorf("%s: a warning with no fingerprint cannot be acknowledged", tc.name)
		}
	}
}

// The controls: nothing breaks, so nothing warns.
func TestATableChangeThatBreaksNoRuleIsQuiet(t *testing.T) {
	cases := []struct {
		name   string
		action string
		table  string
		before TableState
		after  TableState
		rules  []TableRule
	}{
		// RouterOS carries the rule across a rename by id.
		{"rename", "update", "isp2", liveTable, liveTable, tableRules},
		{"enable", "update", "isp2", TableState{Present: true, FIB: true, Disabled: true}, liveTable, tableRules},
		{"no rule uses it", "delete", "isp3", liveTable, TableState{}, tableRules},
		{"only a disabled rule uses it", "delete", "isp2", liveTable, TableState{},
			[]TableRule{{ID: "*3", Table: "isp2", Disabled: true}}},
		{"already disabled, left disabled", "update", "isp2", TableState{Present: true, Disabled: true},
			TableState{Present: true, Disabled: true}, tableRules},
		{"a create", "create", "isp2", TableState{}, liveTable, tableRules},
	}
	for _, tc := range cases {
		if v := CheckTableChange(tc.action, tc.table, "", tc.before, tc.after, tc.rules); v.Level != "none" {
			t.Errorf("%s: verdict %+v, want none", tc.name, v)
		}
	}
}

// An acknowledgement answers the rules it was shown. A new rule on the table
// between the prompt and the answer is a different question.
func TestTheFingerprintBindsTheRulesItCounted(t *testing.T) {
	one := CheckTableChange("delete", "isp2", "*400", liveTable, TableState{}, tableRules[:1])
	two := CheckTableChange("delete", "isp2", "*400", liveTable, TableState{}, tableRules)
	if !one.Warned() || !two.Warned() || one.Fingerprint == two.Fingerprint {
		t.Errorf("a second rule on the table left the fingerprint unchanged: %q", one.Fingerprint)
	}
}

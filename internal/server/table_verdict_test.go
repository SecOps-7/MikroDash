package server

import (
	"testing"

	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
)

// tableDecision against the real RoutingTable resource, with the row shapes the
// CHR returned: `fib` is a presence flag (an empty value means set), and a rule
// names its table.
func TestTableDecisionReadsTheRowAndMergesAPartialEdit(t *testing.T) {
	before := map[string]string{".id": "*400", "name": "isp2", "fib": "", "disabled": "false"}
	rules := []routeros.Reply{
		{".id": "*1", "table": "isp2", "disabled": "false"},
		{".id": "*2", "table": "main", "disabled": "false"},
	}
	warn := []struct {
		name   string
		action string
		values map[string]string
	}{
		{"remove", "delete", nil},
		// Values as validation hands them over: flags are "yes" and "no".
		{"disable, sent alone", "update", map[string]string{"disabled": "yes"}},
		{"fib unset, sent alone", "update", map[string]string{"fib": "no"}},
		{"fib unset, whole form", "update", map[string]string{"name": "isp2", "fib": "no", "disabled": "no"}},
	}
	for _, tc := range warn {
		v := tableDecision(resource.RoutingTable, tc.action, tc.values, before, rules)
		if !v.Warned() || v.Detail["rules"] != 1 {
			t.Errorf("%s: verdict %+v, want a warning about one rule", tc.name, v)
		}
	}
	// THE CONTROLS. A partial edit that says nothing about fib or disabled must
	// read them from the row: treating an absent `fib` as unset would warn on
	// every comment change.
	quiet := []struct {
		name   string
		values map[string]string
	}{
		{"a comment, sent alone", map[string]string{"comment": "uplink 2"}},
		{"a rename", map[string]string{"name": "isp-two"}},
		// THE WHOLE FORM, as the browser saves it and validation normalises it.
		// This read "yes" as unset, and warned on every save from the page
		// (found live on the CHR, 2026-09-18).
		{"the whole form, a comment changed", map[string]string{"name": "isp2", "fib": "yes",
			"disabled": "no", "comment": "uplink 2"}},
	}
	for _, tc := range quiet {
		if v := tableDecision(resource.RoutingTable, "update", tc.values, before, rules); v.Level != "none" {
			t.Errorf("%s: verdict %+v, want none", tc.name, v)
		}
	}
}

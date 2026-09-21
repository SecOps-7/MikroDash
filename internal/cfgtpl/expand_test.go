package cfgtpl

import (
	"strings"
	"testing"
)

// A selector naming an interface list is sent once per interface; the list
// elsewhere on the line, and on other lines, stays its comma-separated text.
func TestAListSelectorIsSentOncePerInterface(t *testing.T) {
	tp := mustParse(t, "/interface bridge vlan\nadd bridge=bridge vlan-ids={{vlan}} untagged={{ports}}\n"+
		"/interface bridge port\nset [ find interface={{ports}} ] pvid={{vlan}}")
	defs := []VarDef{{Name: "vlan", Type: "vlan-id"}, {Name: "ports", Type: "iface-list"}}
	bound, vals, err := Bind(tp, defs, map[string]string{"vlan": "10", "ports": "ether2, 2.4GHz WiFi"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(bound, vals)
	if err != nil {
		t.Fatal(err)
	}
	// The list itself leaves as ONE literal, as every value does (QuoteROS);
	// RouterOS splits a list property on its commas.
	want := "/interface bridge vlan\nadd bridge=bridge vlan-ids=10 untagged=\"ether2,2.4GHz WiFi\"\n" +
		"/interface bridge port\nset [ find interface=ether2 ] pvid=10\nset [ find interface=\"2.4GHz WiFi\" ] pvid=10\n"
	if got != want {
		t.Errorf("rendered\n%s\nwant\n%s", got, want)
	}
	// What the analyser judges is the same expansion.
	filled, err := Fill(bound, vals)
	if err != nil || len(filled.Lines) != 3 {
		t.Errorf("the filled template has %d lines, want 3: %v", len(filled.Lines), err)
	}
}

// An optional list left empty selects nothing, so its lines are not sent; a
// required one cannot be left empty.
func TestAnEmptyListSendsNoSelectorLine(t *testing.T) {
	tp := mustParse(t, "/interface bridge port\nset [ find interface={{ports}} ] pvid=10\n/ip dns\nset allow-remote-requests=yes")
	opt := []VarDef{{Name: "ports", Type: "iface-list"}}
	bound, vals, err := Bind(tp, opt, map[string]string{"ports": ""}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := Render(bound, vals); strings.Contains(got, "pvid") || !strings.Contains(got, "allow-remote-requests") {
		t.Errorf("an empty list still sent its selector line, or took the rest with it:\n%s", got)
	}
	if _, _, err := Bind(tp, []VarDef{{Name: "ports", Type: "iface-list", Required: true}}, map[string]string{}, nil); err == nil {
		t.Error("a required list was accepted empty")
	}
}

// Each item is validated as its element type: a list cannot smuggle what one
// value of that type could not hold.
func TestListItemsAreValidatedOneByOne(t *testing.T) {
	for _, c := range []struct {
		typ, v string
		ok     bool
	}{
		{"iface-list", "ether2,ether3", true},
		{"iface-list", "ether2,bad\"name", false},
		{"port-list", "8123,1883", true},
		{"port-list", "8123,70000", false},
	} {
		_, err := Validate(VarDef{Type: c.typ}, c.v)
		if (err == nil) != c.ok {
			t.Errorf("%s %q: err %v", c.typ, c.v, err)
		}
	}
}

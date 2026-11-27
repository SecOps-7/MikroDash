package resource

import "testing"

// TestOptionSourcesAreDeduplicatedByMenu pins the property the caller depends
// on: /interface backs both the VLAN parent and the bridge port, and the server
// reads each distinct menu once per form open.
func TestOptionSourcesAreDeduplicatedByMenu(t *testing.T) {
	srcs := BridgePort.OptionSources()
	if len(srcs) != 2 {
		t.Fatalf("bridgePort has %d menu-backed pickers, want 2", len(srcs))
	}
	menus := map[string]bool{}
	for _, s := range srcs {
		if s.Menu == "" || s.Value == "" {
			t.Errorf("incomplete option source: %+v", s)
		}
		menus[s.Menu] = true
	}
	if len(menus) != 2 {
		t.Errorf("expected two distinct menus, got %v", menus)
	}
}

// TestStaticOptionsCopyTheSlice: a caller that sorted or appended to the
// returned list must not mutate the declaration every later form open reads.
func TestStaticOptionsCopyTheSlice(t *testing.T) {
	r := &Resource{Key: "t", Fields: []Field{
		{Name: "mode", OptionsFrom: &OptionsFrom{Values: []string{"b", "a"}}},
	}}
	got := r.StaticOptions()["mode"]
	got[0] = "MUTATED"
	if r.Fields[0].OptionsFrom.Values[0] != "b" {
		t.Error("StaticOptions handed out the declaration itself; a caller can now corrupt it")
	}
}

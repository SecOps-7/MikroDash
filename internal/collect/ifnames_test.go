package collect

import "testing"

// THE NAMES EVENT CARRIES EACH INTERFACE'S TYPE (issue #132).
//
// The Dashboard's Network Flow card counts wired ports from `ifstatus:names`,
// the one interface event every browser receives. Without the type it cannot
// tell an ethernet port from a bridge or a VLAN.
func TestTheInterfaceNamesCarryTheirType(t *testing.T) {
	got := NamesOf(&IfStatusPayload{Interfaces: []Interface{
		{Name: "ether1", Type: "ether", Running: true},
		{Name: "bridge", Type: "bridge", Running: true},
	}})
	if got == nil || len(got.Interfaces) != 2 {
		t.Fatalf("NamesOf returned %+v", got)
	}
	for i, want := range []string{"ether", "bridge"} {
		if got.Interfaces[i].Type != want {
			t.Errorf("interface %d carries type %q, want %q", i, got.Interfaces[i].Type, want)
		}
	}
}

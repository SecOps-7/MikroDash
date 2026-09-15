package collect

import (
	"strings"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

// AN INTERFACE CARRIES ITS ROUTEROS ID.
//
// The Interfaces page edits a row through the resource engine, which addresses
// it by `.id` and identifies it by name. Without the id in the payload a row
// cannot be opened at all, and nothing fails: the click simply does nothing.
//
// The recorded capture cannot show this, because its proplist predates the
// field, so this is the proof `addedSinceNode` points at.
func TestAnInterfaceCarriesItsRouterOSID(t *testing.T) {
	if !strings.Contains(ifStatusIfCmd.Args[0], ".id") {
		t.Fatalf("the interface read does not ask for .id: %q", ifStatusIfCmd.Args[0])
	}
	got, _, _ := BuildIfStatus(nil, IfStatusInput{
		Ifaces: []routeros.Reply{
			{".id": "*1", "name": "ether1", "type": "ether", "running": "true"},
			{".id": "*A", "name": "bridge", "type": "bridge", "disabled": "true"},
		},
		Now: time.Now(),
	})
	if len(got) != 2 {
		t.Fatalf("%d interfaces, want 2", len(got))
	}
	for i, want := range []string{"*1", "*A"} {
		if got[i].ID != want {
			t.Errorf("%s: id = %q, want %q", got[i].Name, got[i].ID, want)
		}
	}
}

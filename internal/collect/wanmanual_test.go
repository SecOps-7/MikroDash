package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// MANUAL WITH NOTHING TICKED IS STILL MANUAL.
//
// `sitedoc.WANUplinks.Manual()` answers nil in auto mode and the stored list in
// manual mode, so the empty list and "not manual" are different values with the
// same length. Reading `len(manual) > 0` collapsed them: unticking the last
// uplink and pressing Apply stored `{mode:"manual",names:[]}`, the payload came
// back saying `detect`, and the page put its own switch back to Auto and filled
// the table with the router's answer.
//
// The empty state has a sentence written for it on the page — "No interfaces are
// declared as uplinks" — which no router could ever reach while the two were
// collapsed.

func wanDetectRow(name string) routeros.Reply {
	return routeros.Reply{"name": name, "state": "internet"}
}

func TestManualWithNothingTickedStaysManual(t *testing.T) {
	detect := []routeros.Reply{wanDetectRow("ether1")}
	ifaces := []routeros.Reply{{"name": "ether1", "type": "ether", "running": "true"}}

	empty := BuildWanRows(detect, nil, nil, nil, ifaces, nil, []string{})
	if empty.UplinkSource != "manual" {
		t.Errorf("uplinkSource = %q, want manual — an empty declared list is a "+
			"declaration, not an absence", empty.UplinkSource)
	}
	if len(empty.Wans) != 0 {
		t.Errorf("%d uplinks, want 0 — the router's own answer was used anyway",
			len(empty.Wans))
	}
	if empty.ManualNames == nil {
		t.Error("manualNames is nil; Go must never send a null array")
	}

	// AND THE OTHER TWO STATES STILL READ AS THEMSELVES, or this test would pass
	// on an implementation that always says manual.
	auto := BuildWanRows(detect, nil, nil, nil, ifaces, nil, nil)
	if auto.UplinkSource != "detect" {
		t.Errorf("with no document: uplinkSource = %q, want detect", auto.UplinkSource)
	}
	if len(auto.Wans) != 1 {
		t.Errorf("with no document: %d uplinks, want the router's 1", len(auto.Wans))
	}

	named := BuildWanRows(detect, nil, nil, nil, ifaces, nil, []string{"ether1"})
	if named.UplinkSource != "manual" || len(named.Wans) != 1 {
		t.Errorf("with one name: source=%q rows=%d, want manual and 1",
			named.UplinkSource, len(named.Wans))
	}
	if !named.Wans[0].Manual {
		t.Error("a declared uplink is not marked manual, so the page cannot say so")
	}
}

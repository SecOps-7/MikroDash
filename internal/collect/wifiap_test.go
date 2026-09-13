package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// THE ACCESS POINT A NETWORK IS BROADCAST BY, on both CAPsMAN trees.
//
// ── WHY THIS TEST CARRIES A LEDGER ENTRY ────────────────────────────────────
//
// `addedSinceNode` in fixture_test.go names this test, because the golden corpus
// cannot reach the field: no capture in this repository has a CAP-provisioned
// `/interface/wifi` row, and on the AX3 that was captured the answer is
// correctly "" on every interface. So the golden proves the field exists and
// this proves it is filled. Deleting or renaming this fails that ledger.

func TestBuildWifiViewReadsTheAccessPointFromCap(t *testing.T) {
	// `cap` reads `identity@base-mac%id` and is present only on the MASTER
	// interface — which is the whole reason the chase exists.
	nets, radios := BuildWifiView(WifiViewInput{
		Ifaces: []routeros.Reply{
			{".id": "*1", "name": "cap24", "radio-mac": "AA:BB:CC:DD:EE:01",
				"cap":                "cap-hall@AA:BB:CC:DD:EE:00%1",
				"configuration.ssid": "service", "channel.band": "2ghz-ax", "running": "true"},
			{".id": "*2", "name": "cap24-guest", "master-interface": "cap24",
				"configuration.ssid": "guest", "running": "true"},
			{".id": "*3", "name": "cap5", "radio-mac": "AA:BB:CC:DD:EE:02",
				"cap":                "cap-hall@AA:BB:CC:DD:EE:00%2",
				"configuration.ssid": "service", "channel.band": "5ghz-ax", "running": "true"},
			// A LOCAL radio: no `cap` at all, which must read as "this router"
			// rather than as an access point called "".
			{".id": "*4", "name": "wifi1", "radio-mac": "AA:BB:CC:DD:EE:10",
				"configuration.ssid": "home", "channel.band": "5ghz-ax", "running": "true"},
		},
	})

	byName := map[string]WifiNetwork{}
	for _, n := range nets {
		byName[n.Name] = n
	}
	if got := byName["cap24"].AP; got != "cap-hall" {
		t.Errorf("master AP = %q, want cap-hall", got)
	}
	if got := byName["cap5"].AP; got != "cap-hall" {
		t.Errorf("the other band's AP = %q, want cap-hall — one box, one "+
			"pin on the map and one group on the page", got)
	}
	if got := byName["cap24-guest"].AP; got != "cap-hall" {
		t.Errorf("the virtual AP's AP = %q, want cap-hall — only the master "+
			"carries `cap`, so the chase did not run", got)
	}
	if got := byName["wifi1"].AP; got != "" {
		t.Errorf("a local radio reported AP %q, want empty", got)
	}

	// The RADIO list carries it too, because the map pins radios and the page's
	// AP view groups by it.
	found := 0
	for _, r := range radios {
		if r.Name == "wifi1" && r.AP != "" {
			t.Errorf("local radio %q reported AP %q", r.Name, r.AP)
		}
		if r.AP == "cap-hall" {
			found++
		}
	}
	if found != 2 {
		t.Errorf("%d radios attributed to the CAP, want 2", found)
	}
	// A virtual AP is not a radio.
	if len(radios) != 3 {
		t.Errorf("%d radios, want 3", len(radios))
	}
}

// The legacy tree answers the same question through `/caps-man/radio`, because a
// `/caps-man/interface` row carries no `cap` field at all.
func TestCapsLegacyAPsJoinsThroughTheRadio(t *testing.T) {
	// ── THE ROWS ARE SHAPED LIKE THE ROUTER'S, AND THAT IS THE POINT ────────
	//
	// A master answers `master-interface=none`, not an empty string, and a SLAVE
	// carries a `radio-mac` of its own — the master's with the
	// locally-administered bit set (`6E:` against `6C:`), which belongs to no
	// radio. Both were got wrong on the first pass, and between them they made
	// every legacy interface read as a virtual AP: no radios in the payload, an
	// empty access-point tray on the Wi-Fi map, and every slave grouped under
	// "This router". Written as the router writes them so that cannot recur.
	ifaces := []routeros.Reply{
		{"name": "cap-north-1", "master-interface": "none", "radio-mac": "02:00:00:00:0A:02"},
		{"name": "cap-north-1-1", "master-interface": "cap-north-1",
			"radio-mac": "02:00:00:00:0A:03"},
		{"name": "orphan-1", "master-interface": "none", "radio-mac": "00:00:00:00:00:99"},
	}
	radios := []routeros.Reply{
		{"radio-mac": "02:00:00:00:0A:02", "remote-cap-identity": "cap-north"},
	}
	got := CapsLegacyAPs(ifaces, radios)

	if got["cap-north-1"] != "cap-north" {
		t.Errorf("master = %q, want cap-north", got["cap-north-1"])
	}
	if got["cap-north-1-1"] != "cap-north" {
		t.Errorf("virtual AP = %q, want cap-north — the chase must follow "+
			"master-interface to the ROOT rather than stopping at the slave's own "+
			"radio-mac, which matches no radio", got["cap-north-1-1"])
	}

	// AND "none" IS NOT A MASTER. Reading it as an interface name would send the
	// chase looking for a device called "none" and find nothing.
	if capsV1Master(ifaces[0]) != "" {
		t.Error(`master-interface="none" read as a real master`)
	}
	if capsV1Master(ifaces[1]) != "cap-north-1" {
		t.Error("a real master-interface was dropped")
	}
	if _, ok := got["orphan-1"]; ok {
		t.Error("an interface whose radio no CAP reported was attributed anyway")
	}

	// AND THE OTHER SPELLING. MikroTik's own page prints the column as
	// REMOTE-AP-IDENT; the detail view says `remote-cap-identity`. Both are read,
	// because a wrong guess detaches every radio from its CAP rather than
	// costing a column.
	alt := CapsLegacyAPs(ifaces, []routeros.Reply{
		{"radio-mac": "02:00:00:00:0A:02", "remote-ap-ident": "cap-north"},
	})
	if alt["cap-north-1"] != "cap-north" {
		t.Errorf("the REMOTE-AP-IDENT spelling did not resolve: %q", alt["cap-north-1"])
	}
}

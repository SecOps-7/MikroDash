package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// THE FREQUENCY ANALYSER'S CATALOGUE LIVES ON THE WIRELESS COLLECTOR.
//
// ── WHY THESE TESTS MOVED HERE FROM `wifi_scancat_test.go` ────────────────
//
// They passed against `Wifi` for as long as the catalogue was there, and the
// feature was still unreachable: `faOpenBtn` is on the Wireless page, `ws.go`
// resumes the WIRELESS collector for that page, and the catalogue sat on the one
// resumed by `case "wifi"`. A session that went straight to the page the button
// is on found an empty catalogue and no button.
//
// So the property worth pinning is not "a catalogue is built" but "the collector
// the page runs is the one that has it", and a test on the wrong collector
// cannot say that. Live keeps it here too — `src/collectors/wireless.js:391`.
//
// It is deliberately NOT derived from the payload's SSIDs: that is the page's
// shape, joined and enriched for display, while the scan needs the raw
// relationships — which interface is a master, which virtual AP rides on which
// radio, and the `.id` the scan command is addressed by.
func TestTheScanCatalogueIsKeptOnTheModernStack(t *testing.T) {
	r := fakeReader{rows: map[string][]routeros.Reply{
		"/interface/wifi/print": {
			{".id": "*1", "name": "wifi1", "master": "true", "running": "true", "disabled": "false"},
			{".id": "*2", "name": "wifi1-guest", "master": "false",
				"master-interface": "wifi1", "running": "true", "disabled": "false"},
			{".id": "*3", "name": "capsman-ap", "master": "true", "running": "true",
				"configuration.manager": "capsman"},
		},
		"/interface/wifi/registration-table/print": {
			{"mac-address": "AA:01", "interface": "wifi1", "ssid": "Home"},
			{"mac-address": "AA:02", "interface": "wifi1-guest", "ssid": "Guest"},
			{"mac-address": "AA:03", "interface": "wifi1-guest", "ssid": "Guest"},
		},
	}}

	c := NewWireless(r, func(string, string, any) {}, nil, 30000)
	c.Tick()

	cat, clients := c.ScanCatalogue()
	if len(cat) != 3 {
		t.Fatalf("the catalogue holds %d entries, want 3: %+v", len(cat), cat)
	}
	byName := map[string]int{}
	for i, e := range cat {
		byName[e.Name] = i
	}
	if cat[byName["wifi1"]].ID != "*1" {
		t.Error("the RouterOS .id was lost — the scan command is addressed by it")
	}
	if !cat[byName["wifi1"]].Master {
		t.Error("a master radio parsed as not-a-master. Every radio then fails " +
			"ScannableInterfaces' filter and the dialog offers none — which is exactly " +
			"how the button stayed hidden with `permitted: true` in the payload")
	}
	if cat[byName["wifi1-guest"]].MasterInterface != "wifi1" {
		t.Error("the virtual AP's master was lost — its clients would not be counted")
	}
	if !cat[byName["capsman-ap"]].CapsmanManaged {
		t.Error("a CAPsMAN-managed radio was not marked as such")
	}

	// THREE clients, one per client rather than per interface: the dialog counts
	// them to say how many devices a scan will drop.
	if len(clients) != 3 {
		t.Fatalf("%d client placements, want 3: %v", len(clients), clients)
	}
	for _, cl := range clients {
		if cl == "" {
			t.Error("a client with no interface was kept")
		}
	}
}

// TestTheLegacyStackKeepsNoScanCatalogue.
//
// The refusal is enforced twice on purpose — `ParseCatalogue` rejects the
// endpoint, and a legacy read therefore never populates the field. Either alone
// would work; both together mean a future change to one cannot quietly start
// offering a scan on hardware whose command this port has never seen.
func TestTheLegacyStackKeepsNoScanCatalogue(t *testing.T) {
	r := fakeReader{rows: map[string][]routeros.Reply{
		// No modern menu at all, so the collector falls through to legacy.
		"/interface/wireless/print": {
			{".id": "*1", "name": "wlan1", "master": "true", "running": "true"},
		},
		"/interface/wireless/registration-table/print": {
			{"mac-address": "AA:01", "interface": "wlan1"},
		},
	}}

	c := NewWireless(r, func(string, string, any) {}, nil, 30000)
	c.Tick()

	cat, _ := c.ScanCatalogue()
	if len(cat) != 0 {
		t.Errorf("a legacy router offered %d scannable radios: %+v", len(cat), cat)
	}
}

// TestTheCatalogueIsCopiedOut: this collector keeps polling underneath the
// websocket goroutine that reads it, and a caller holding a slice into its state
// would see a scan's target list change under them mid-dialog.
func TestTheCatalogueIsCopiedOut(t *testing.T) {
	r := fakeReader{rows: map[string][]routeros.Reply{
		"/interface/wifi/print": {
			{".id": "*1", "name": "wifi1", "master": "true", "running": "true"},
		},
		"/interface/wifi/registration-table/print": {
			{"mac-address": "AA:01", "interface": "wifi1", "ssid": "Home", "signal": "-52"},
		},
	}}
	c := NewWireless(r, func(string, string, any) {}, nil, 30000)
	c.Tick()

	cat, clients := c.ScanCatalogue()
	if len(cat) == 0 || len(clients) == 0 {
		t.Fatal("nothing to test against")
	}
	cat[0].Name = "tampered"
	clients[0] = "tampered"

	again, againClients := c.ScanCatalogue()
	if again[0].Name == "tampered" {
		t.Error("the caller's slice aliases the collector's own catalogue")
	}
	if againClients[0] == "tampered" {
		t.Error("the caller's slice aliases the collector's own client list")
	}
}

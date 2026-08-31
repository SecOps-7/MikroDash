package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// The three behaviours this fleet's corpus cannot exercise.
//
// All three survived a mutation against the golden — not because the rules are
// weak but because the captured router happens not to distinguish them: every
// client reports an `ssid`, every registration row is a real client, and the
// CAPsMAN table is empty. Synthetic rows are the honest way to pin them.

// A build that reports no `ssid` on the client is the whole reason the SSID join
// is keyed on the INTERFACE first. Matching on the client's own ssid alone reads
// as zero everywhere, which is indistinguishable from an idle network.
func TestWithClientStatsMatchesByInterfaceFirst(t *testing.T) {
	ssids := []WirelessSSID{
		{SSID: "Home", Ifaces: []string{"wifi1", "wifi2"}},
		{SSID: "Guest", Ifaces: []string{"wifi3"}},
	}
	// Clients with NO ssid field, which is what the failing build sends.
	clients := []WirelessClient{
		{MAC: "AA:01", Iface: "wifi1", Band: "5GHz"},
		{MAC: "AA:02", Iface: "wifi2", Band: "2.4GHz"},
		{MAC: "AA:03", Iface: "wifi3", Band: "5GHz"},
	}
	out := withClientStats(ssids, clients)
	if out[0].Clients != 2 {
		t.Errorf("Home has %d clients, want 2 — the interface match did not fire", out[0].Clients)
	}
	if out[1].Clients != 1 {
		t.Errorf("Guest has %d clients, want 1", out[1].Clients)
	}
	// Bands are collected per SSID and sorted, so a dual-band network reads as one.
	if len(out[0].Bands) != 2 || out[0].Bands[0] != "2.4GHz" || out[0].Bands[1] != "5GHz" {
		t.Errorf("Home bands = %v, want [2.4GHz 5GHz]", out[0].Bands)
	}

	// The SSID fallback still works, for the legacy stack and for CAPsMAN rows
	// naming an interface this router does not own.
	byName := withClientStats(ssids, []WirelessClient{{MAC: "AA:04", Iface: "not-ours", SSID: "Guest"}})
	if byName[1].Clients != 1 {
		t.Error("the ssid fallback did not fire for an interface this router does not own")
	}

	// THE CASE THAT SEPARATES THE TWO ORDERS: a client whose reported ssid names
	// one network while its interface belongs to another. The association is
	// keyed on the interface, so the interface wins — and reversing the two
	// silently moves the client to the wrong network. A test where the client
	// carries no ssid at all cannot see the difference, which is exactly what
	// let the first version of this test pass against the mutation.
	conflict := withClientStats(ssids, []WirelessClient{{MAC: "AA:05", Iface: "wifi1", SSID: "Guest"}})
	if conflict[0].Clients != 1 {
		t.Errorf("Home has %d clients, want 1 — the client's INTERFACE must win", conflict[0].Clients)
	}
	if conflict[1].Clients != 0 {
		t.Errorf("Guest has %d clients, want 0 — its ssid field must not outrank the interface",
			conflict[1].Clients)
	}
}

// Some RouterOS builds answer the registration table with rows describing
// INTERFACES — Ethernet ones included. They are not clients, and counting them
// would inflate every SSID on the page.
func TestIsWirelessRowDropsInterfaceMetadata(t *testing.T) {
	client := routeros.Reply{"mac-address": "AA:01", "signal": "-52", "interface": "wifi1"}
	legacy := routeros.Reply{"mac-address": "AA:02", "signal-strength": "-60"}
	capsman := routeros.Reply{"mac-address": "AA:03", "rx-signal": "-44"}
	quiet := routeros.Reply{"mac-address": "AA:04", "ssid": "Home"} // associated, no signal yet
	meta := routeros.Reply{"name": "ether1", "type": "ether", "mac-address": "AA:05"}

	for _, r := range []routeros.Reply{client, legacy, capsman, quiet} {
		if !isWirelessRow(r) {
			t.Errorf("a real client row was dropped: %v", r)
		}
	}
	if isWirelessRow(meta) {
		t.Error("an interface metadata row was counted as a client")
	}
}

// fakeReader answers canned rows, so a collector can be driven without a router
// or a fixture.
type fakeReader struct{ rows map[string][]routeros.Reply }

func (f fakeReader) Connected() bool { return true }

func (f fakeReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	if r, ok := f.rows[cmd.Path]; ok {
		return r, nil
	}
	return nil, errNoSuchMenu
}

var errNoSuchMenu = errNoSuchMenuType{}

type errNoSuchMenuType struct{}

func (errNoSuchMenuType) Error() string { return "no such command prefix" }

// The filter has to be REACHED, not merely correct.
//
// Testing `isWirelessRow` alone passes whether or not the collector calls it —
// which is how a mutation that deleted the call survived. Driving a tick with a
// metadata row mixed into the registration table is what pins the call site.
func TestTickDropsInterfaceMetadataRows(t *testing.T) {
	ros := fakeReader{rows: map[string][]routeros.Reply{
		"/interface/wifi/registration-table/print": {
			{"mac-address": "AA:01", "signal": "-52", "interface": "wifi1", "ssid": "Home"},
			{"name": "ether1", "type": "ether", "mac-address": "AA:05"}, // metadata, not a client
		},
		"/interface/wifi/print": {
			{"name": "wifi1", "configuration.ssid": "Home", "disabled": "false", "running": "true"},
		},
	}}
	c := NewWireless(ros, func(string, string, any) {}, nil, 30000)
	c.Tick()
	got := c.Last()
	if got == nil {
		t.Fatal("no payload")
	}
	if len(got.Clients) != 1 {
		t.Fatalf("got %d clients, want 1 — the metadata row was counted", len(got.Clients))
	}
	if got.Clients[0].MAC != "AA:01" {
		t.Errorf("kept the wrong row: %+v", got.Clients[0])
	}
	if len(got.SSIDs) != 1 || got.SSIDs[0].Clients != 1 {
		t.Errorf("ssids = %+v, want one network with one client", got.SSIDs)
	}
	// And the first tick reports no CAPsMAN answer, matching the live app's
	// first emit — the probe is deferred to the second tick.
	if got.CapsmanAvailable {
		t.Error("the first payload claims CAPsMAN before the probe has run")
	}
}

// A CAPsMAN row carries no `band`, so the interface NAME is the only signal —
// which is why the fixture rules keep interface names un-anonymised.
func TestWirelessBandInference(t *testing.T) {
	cases := []struct {
		name    string
		row     routeros.Reply
		iface   string
		capsman bool
		want    string
	}{
		{"local row, band field wins", routeros.Reply{"band": "5ghz-ax"}, "wifi1", false, "5GHz"},
		{"local row, 2.4", routeros.Reply{"band": "2ghz-n"}, "wifi1", false, "2.4GHz"},
		{"local row, 6", routeros.Reply{"band": "6ghz-ax"}, "wifi1", false, "6GHz"},
		{"capsman row, name suffix", routeros.Reply{}, "cap-office-5g", true, "5GHz"},
		{"capsman row, name contains", routeros.Reply{}, "AP1-2ghz", true, "2.4GHz"},
		{"capsman row, 6GHz", routeros.Reply{}, "roof-6g", true, "6GHz"},
		{"capsman row, nothing to go on", routeros.Reply{}, "cap1", true, ""},
		{"capsman row WITH a band field uses it", routeros.Reply{"band": "5ghz-ac"}, "cap1", true, "5GHz"},
		{"local row with no band", routeros.Reply{}, "wifi1", false, ""},
	}
	for _, c := range cases {
		if got := wlBandOf(c.row, c.iface, c.capsman); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// An SSID is disabled only when EVERY interface carrying it is: one radio
// broadcasting the network is enough for the network to be up.
func TestParseWirelessSSIDsAggregates(t *testing.T) {
	rows := []routeros.Reply{
		{"name": "wifi1", "configuration.ssid": "Home", "disabled": "true", "running": "false"},
		{"name": "wifi2", "configuration.ssid": "Home", "disabled": "false", "running": "true"},
		{"name": "wifi3", "configuration.ssid": "Guest", "disabled": "true", "running": "false"},
		// A CAP takes its configuration from the manager, so it has no local
		// SSID to report — counted, not listed.
		{"name": "wifi4", "configuration.manager": "capsman-1"},
		{"name": "wifi5"}, // no ssid at all: skipped in silence
	}
	ssids, managed := parseWirelessSSIDs(rows)
	if managed != 1 {
		t.Errorf("managedElsewhere = %d, want 1", managed)
	}
	if len(ssids) != 2 {
		t.Fatalf("got %d ssids, want 2", len(ssids))
	}
	// Sorted by name, so Guest comes first.
	if ssids[0].SSID != "Guest" || !ssids[0].Disabled || ssids[0].Running {
		t.Errorf("Guest = %+v, want disabled and not running", ssids[0])
	}
	if ssids[1].SSID != "Home" || ssids[1].Disabled || !ssids[1].Running {
		t.Errorf("Home = %+v, want enabled and running — one live radio is enough", ssids[1])
	}
	if len(ssids[1].Ifaces) != 2 {
		t.Errorf("Home ifaces = %v, want both", ssids[1].Ifaces)
	}
}

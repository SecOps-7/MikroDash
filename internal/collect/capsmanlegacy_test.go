package collect

import (
	"testing"

	"mikrodash/internal/routeros"
)

// The `/caps-man` tree, driven by synthetic rows.
//
// ── WHY SYNTHETIC AND NOT A FIXTURE ─────────────────────────────────────────
//
// Every router captured into `testdata/fixtures/` runs the MODERN manager: the
// AX3's `/caps-man/registration-table` capture is zero rows, which is what a
// board with no legacy wireless package answers. So a fixture replay cannot
// reach any of this, and a golden that cannot reach a builder is not a gate over
// it — the same reasoning `wifiview.go`'s header records for the legacy stack.
//
// The rows below are shaped after a real RB5009 running both trees: twelve
// RB951s on `/caps-man` alongside three wifi-qcom CAPs, `name-format=identity`,
// and channel profiles reading `2ghz-onlyn` and `5ghz-onlyac`.

// capsV1Iface builds one `/caps-man/interface` row.
//
// `master` IS SPELLED "none" ON A MASTER, because that is what the router
// answers — see capsV1Master. Writing "" here would have made every test agree
// with a reading that cost the live page its radios.
func capsV1Iface(name, master, radioMac, cfg string) routeros.Reply {
	if master == "" {
		master = "none"
	}
	return routeros.Reply{
		"name": name, "master-interface": master, "radio-mac": radioMac,
		"configuration": cfg, "running": "true",
	}
}

// The band a v1 client is on is THREE HOPS from the registration row, and the
// point of the test is that every hop is taken.
func TestCapsLegacyBandsWalksTheManagersConfiguration(t *testing.T) {
	ifaces := []routeros.Reply{
		capsV1Iface("cap-north-1", "", "02:00:00:00:0A:02", "cfg-office"),
		// A slave interface: its own configuration names no channel, so the band
		// has to come from the master it rides.
		{"name": "cap-north-2", "master-interface": "cap-north-1", "configuration": "cfg-guest"},
		capsV1Iface("cap-south-1", "", "02:00:00:00:0B:02", "cfg-5g"),
		// An INLINE channel override on the interface, which RouterOS lets you
		// set and which must win over the configuration's.
		{"name": "odd-1", "configuration": "cfg-office",
			"configuration.channel": "channel5-a"},
	}
	configs := []routeros.Reply{
		{"name": "cfg-office", "channel": "channel24-a"},
		{"name": "cfg-guest"},
		{"name": "cfg-5g", "channel": "channel5-a"},
	}
	channels := []routeros.Reply{
		{"name": "channel24-a", "band": "2ghz-onlyn", "frequency": "2437",
			"control-channel-width": "20mhz"},
		{"name": "channel5-a", "band": "5ghz-onlyac"},
	}

	got := CapsLegacyBands(ifaces, configs, channels)

	if got["cap-north-1"].Raw != "2ghz-onlyn" {
		t.Errorf("master band = %q, want 2ghz-onlyn", got["cap-north-1"].Raw)
	}
	if got["cap-north-1"].Width != "20mhz" {
		t.Errorf("width = %q, want 20mhz — control-channel-width is v1's spelling",
			got["cap-north-1"].Width)
	}
	if got["cap-north-2"].Raw != "2ghz-onlyn" {
		t.Errorf("slave band = %q, want 2ghz-onlyn — the master chase did not run",
			got["cap-north-2"].Raw)
	}
	if got["cap-south-1"].Raw != "5ghz-onlyac" {
		t.Errorf("cap-south-1 band = %q, want 5ghz-onlyac", got["cap-south-1"].Raw)
	}
	if got["odd-1"].Raw != "5ghz-onlyac" {
		t.Errorf("odd-1 band = %q, want 5ghz-onlyac — the interface's own channel "+
			"override must beat the configuration's", got["odd-1"].Raw)
	}

	// AND THE TWO LABELS THE PAGES ACTUALLY RENDER. The band token alone proves
	// the walk; these prove the walk answers the question the columns ask.
	if b := BandLabel(got["cap-north-1"].Raw); b != "2.4GHz" {
		t.Errorf("band label = %q, want 2.4GHz", b)
	}
	if s := WifiStandard(got["cap-north-1"].Raw); s != "Wi-Fi 4" {
		t.Errorf("standard = %q, want Wi-Fi 4 — `only` is a prefix, not a token", s)
	}
	if s := WifiStandard(got["cap-south-1"].Raw); s != "Wi-Fi 5" {
		t.Errorf("standard = %q, want Wi-Fi 5", s)
	}
}

// The reason this join exists: without it the Wifi Clients table showed a dash
// in Band and Standard for every client on a v1 CAP, which is what was reported.
func TestParseWirelessClientTakesItsBandFromTheManager(t *testing.T) {
	row := routeros.Reply{
		"mac-address": "02:00:00:00:00:01", "interface": "cap-north-1",
		"ssid": "service", "rx-signal": "-55", "tx-rate": "72.2Mbps", "uptime": "1d",
	}
	band := CapsBand{Raw: "2ghz-onlyn", Frequency: "2437"}

	c := parseWirelessClient(row, true, "192.168.10.82", "kitchen-pi", band)
	if c.Band != "2.4GHz" {
		t.Errorf("band = %q, want 2.4GHz — a /caps-man row carries none of its own", c.Band)
	}
	if c.Standard != "Wi-Fi 4" {
		t.Errorf("standard = %q, want Wi-Fi 4", c.Standard)
	}
	if c.Source != "capsman" {
		t.Errorf("source = %q, want capsman", c.Source)
	}

	// WITH NOTHING TO SAY, IT SAYS NOTHING. An unresolved interface must leave
	// both columns empty rather than guess — the page renders a dash, and a
	// guessed generation is read as a fact about the client.
	blank := parseWirelessClient(row, true, "", "", CapsBand{})
	if blank.Band != "" || blank.Standard != "" {
		t.Errorf("band=%q standard=%q, want both empty when the manager said nothing",
			blank.Band, blank.Standard)
	}

	// AND A ROW THAT CARRIES ITS OWN BAND IS NOT OVERRIDDEN. The modern
	// registration table reports one per client — what it NEGOTIATED — and that
	// is a better answer than what the radio offers.
	own := routeros.Reply{"mac-address": "02:00:00:00:00:02", "interface": "wifi1",
		"band": "5ghz-ax", "signal": "-40"}
	got := parseWirelessClient(own, true, "", "", CapsBand{Raw: "2ghz-onlyn"})
	if got.Band != "5GHz" || got.Standard != "Wi-Fi 6" {
		t.Errorf("band=%q standard=%q, want 5GHz / Wi-Fi 6 — the row's own band wins",
			got.Band, got.Standard)
	}
}

func TestBuildCapsmanLegacyViewJoinsClientsThroughTheRadio(t *testing.T) {
	manager := routeros.Reply{"enabled": "true"}
	remote := []routeros.Reply{
		// v1 spells the model `board`, not `board-name`.
		{"identity": "cap-north", "address": "198.51.100.7", "board": "RB951G-2HnD",
			"serial": "SERIAL1", "version": "6.49.18", "base-mac": "02:00:00:00:0a:00",
			"state": "Run", "uptime": "1d10h"},
		{"identity": "cap-south", "board": "RB951Ui-2HnD", "serial": "SERIAL2",
			"base-mac": "02:00:00:00:0b:00", "state": "Run"},
	}
	radios := []routeros.Reply{
		{"radio-mac": "02:00:00:00:0A:02", "interface": "cap-north-1",
			"remote-cap-identity": "cap-north"},
		{"radio-mac": "02:00:00:00:0B:02", "interface": "cap-south-1",
			"remote-cap-identity": "cap-south"},
	}
	ifaces := []routeros.Reply{
		capsV1Iface("cap-north-1", "", "02:00:00:00:0A:02", "cfg-a"),
		{"name": "cap-north-2", "master-interface": "cap-north-1", "configuration": "cfg-b"},
		capsV1Iface("cap-south-1", "", "02:00:00:00:0B:02", "cfg-a"),
	}
	reg := []routeros.Reply{
		{"mac-address": "02:00:00:00:00:01", "interface": "cap-north-1",
			"ssid": "service", "rx-signal": "-31", "uptime": "1d2h"},
		// On the VIRTUAL AP, which carries no radio-mac of its own: without the
		// master chase this client is attributed to nobody.
		{"mac-address": "02:00:00:00:00:02", "interface": "cap-north-2",
			"ssid": "guest", "rx-signal": "-70"},
		{"mac-address": "02:00:00:00:00:03", "interface": "cap-south-1",
			"ssid": "service", "rx-signal": "-55"},
		// An interface no CAP owns — a stale row, or a radio that has gone.
		{"mac-address": "02:00:00:00:00:04", "interface": "ghost-1"},
	}

	v := BuildCapsmanLegacyView(manager, remote, radios, ifaces, reg)

	if !v.ManagerEnabled {
		t.Error("the manager reads as disabled")
	}
	if len(v.Caps) != 2 {
		t.Fatalf("%d CAPs, want 2", len(v.Caps))
	}
	byIdent := map[string]Cap{}
	for _, c := range v.Caps {
		byIdent[c.Identity] = c
		if !c.Legacy {
			t.Errorf("%s is not marked legacy, so the page cannot tell the trees apart",
				c.Identity)
		}
	}
	if got := byIdent["cap-north"].BoardName; got != "RB951G-2HnD" {
		t.Errorf("board = %q, want RB951G-2HnD — v1 calls the field `board`", got)
	}
	if got := byIdent["cap-north"].ConnectedTime; got != "1d10h" {
		t.Errorf("connected = %q, want 1d10h — v1 reports it as `uptime`", got)
	}
	if got := byIdent["cap-north"].ClientCount; got != 2 {
		t.Errorf("cap-north has %d clients, want 2 — the virtual AP's client is "+
			"reached only by chasing master-interface", got)
	}
	if got := byIdent["cap-south"].ClientCount; got != 1 {
		t.Errorf("cap-south has %d clients, want 1", got)
	}
	if v.ClientsOnCaps != 3 || v.ClientsLocal != 1 {
		t.Errorf("onCaps=%d local=%d, want 3 and 1 — the ghost row belongs to nobody",
			v.ClientsOnCaps, v.ClientsLocal)
	}
	// Strongest signal first, as the modern builder does.
	cl := byIdent["cap-north"].Clients
	if len(cl) != 2 || cl[0].Mac != "02:00:00:00:00:01" {
		t.Errorf("clients are not strongest-first: %+v", cl)
	}
	if cl[0].Signal == nil || *cl[0].Signal != -31 {
		t.Error("rx-signal did not reach the client — v1 has no `signal` field")
	}

	// `Run` IS HEALTHY. v2 says `ok` and v1 says `Run`; counting only the first
	// reported a working fleet as entirely down.
	if !capStateOk("Run") || !capStateOk("ok") || capStateOk("L2 Run") {
		t.Error("capStateOk does not agree with both trees' vocabularies")
	}
}

func TestBuildCapsLegacyNetworksAreReadOnlyAndCarryTheirBand(t *testing.T) {
	in := CapsLegacyViewInput{
		Ifaces: []routeros.Reply{
			capsV1Iface("cap-north-1", "", "02:00:00:00:0A:02", "cfg-office"),
			{"name": "cap-north-2", "master-interface": "cap-north-1",
				"configuration": "cfg-guest", "running": "true"},
		},
		Configs: []routeros.Reply{
			{"name": "cfg-office", "ssid": "service", "channel": "channel24",
				"security": "security3", "datapath": "datapath2"},
			{"name": "cfg-guest", "ssid": "guest", "security": "security1",
				"datapath": "datapath3"},
		},
		Security: []routeros.Reply{
			{"name": "security3", "authentication-types": "wpa-psk,wpa2-psk"},
			{"name": "security1", "authentication-types": "wpa2-psk"},
		},
		Channels:  []routeros.Reply{{"name": "channel24", "band": "2ghz-onlyn", "frequency": "2437"}},
		Datapaths: []routeros.Reply{{"name": "datapath2", "bridge": "bridge-home", "vlan-id": "20"}},
		Reg: []routeros.Reply{
			{"interface": "cap-north-1"}, {"interface": "cap-north-1"},
			{"interface": "cap-north-2"},
		},
	}

	nets, radios := BuildCapsLegacyNetworks(in)
	if len(nets) != 2 {
		t.Fatalf("%d networks, want 2", len(nets))
	}
	master, slave := nets[0], nets[1]

	if master.SSID != "service" || slave.SSID != "guest" {
		t.Errorf("ssids = %q / %q, want service / guest", master.SSID, slave.SSID)
	}
	if master.Band != "2.4GHz" {
		t.Errorf("band = %q, want 2.4GHz", master.Band)
	}
	if slave.Band != "2.4GHz" {
		t.Errorf("the virtual AP's band = %q, want 2.4GHz — it rides the master radio",
			slave.Band)
	}
	if master.Security != "WPA2/WPA" {
		t.Errorf("security = %q, want WPA2/WPA", master.Security)
	}
	if master.VlanID != "20" || master.Bridge != "bridge-home" {
		t.Errorf("datapath did not resolve: vlan=%q bridge=%q", master.VlanID, master.Bridge)
	}
	if master.Clients != 2 || slave.Clients != 1 {
		t.Errorf("clients = %d / %d, want 2 / 1", master.Clients, slave.Clients)
	}
	if !slave.IsVirtual || master.IsVirtual {
		t.Error("the master/virtual split is wrong")
	}

	// ── THE READ-ONLY PROPERTY, WHICH IS THE ONE THAT MATTERS ─────────────
	//
	// An empty id is what stops the resource dialog offering an edit it has no
	// menu for: `resRow` renders no `data-id`, and the engine opens a row only
	// when it has one. A row that gained an id here would open a form that
	// writes to `/interface/wifi` and fails against a `/caps-man` interface.
	for _, n := range nets {
		if n.ID != "" || n.Editable || n.Removable || n.Resource != "" {
			t.Errorf("%s is offered for editing: id=%q editable=%v removable=%v res=%q",
				n.Name, n.ID, n.Editable, n.Removable, n.Resource)
		}
		if n.ReadOnlyReason != "capsv1" {
			t.Errorf("%s says %q, want capsv1 — the badge keys off this", n.Name, n.ReadOnlyReason)
		}
	}

	// ONE RADIO, NOT TWO: a virtual AP is not a radio.
	if len(radios) != 1 || radios[0].Name != "cap-north-1" {
		t.Fatalf("radios = %+v, want just the master", radios)
	}
	if radios[0].Frequency != "2437" || radios[0].Band != "2.4GHz" {
		t.Errorf("radio band/frequency = %q / %q", radios[0].Band, radios[0].Frequency)
	}
}

// The local row wins when a manager sees its own radios twice — once in its
// local stack as dynamic interfaces and once in `/caps-man/interface`. The local
// one carries an `.id`, so it is the one that can be edited.
func TestWithLegacyCapsKeepsTheLocalRow(t *testing.T) {
	local := wifiView{
		networks: []WifiNetwork{{ID: "*1", Name: "wlan1", Resource: "wlNet"}},
		radios:   []WifiRadio{{Name: "wlan1"}},
	}
	out := withLegacyCaps(local,
		[]WifiNetwork{
			{Name: "wlan1", ReadOnlyReason: "capsv1"},
			{Name: "cap-north-1", ReadOnlyReason: "capsv1"},
		},
		[]WifiRadio{{Name: "wlan1"}, {Name: "cap-north-1"}})

	if len(out.networks) != 2 {
		t.Fatalf("%d networks, want 2 — wlan1 was counted twice", len(out.networks))
	}
	if out.networks[0].ID != "*1" {
		t.Error("the local wlan1 row was replaced by the read-only CAPsMAN one")
	}
	if len(out.radios) != 2 {
		t.Errorf("%d radios, want 2", len(out.radios))
	}
}

package collect

import (
	"errors"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// Issue #164: which registration tables the Wifi Clients collector reads, on
// each kind of router there is. The rows follow MikroTik's documented field
// names; no router here runs the legacy `wireless` package, so there is no
// capture to replay.
//
// THE PREMISE, measured on CHR Test (RouterOS 7.24.1, no wifi driver package):
// `/interface/wifi/print` and `/interface/wifi/registration-table/print` answer
// with no rows, and `/interface/wireless/...` and `/caps-man/...` answer "no such
// command prefix". MikroTik's Packages page: since 7.13, WiFi CAPsMAN "comes with
// the system package". So an empty modern menu is the normal state of a legacy
// board, and every router below that runs 7.13+ is given one.

// countingReader is fakeReader plus a count of what was asked, and a failure
// that is not an absent menu (a timeout) for the paths in `fail`.
type countingReader struct {
	fakeReader
	asked map[string]int
	fail  map[string]bool
}

func newCounting(rows map[string][]routeros.Reply) *countingReader {
	return &countingReader{fakeReader: fakeReader{rows: rows}, asked: map[string]int{}, fail: map[string]bool{}}
}

func (c *countingReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	c.asked[cmd.Path]++
	if c.fail[cmd.Path] {
		return nil, errTimeout
	}
	return c.fakeReader.Do(cmd)
}

var errTimeout = errors.New("i/o timeout")

var (
	// What every RouterOS 7.13+ router without a wifi driver answers.
	emptyModernMenus = map[string][]routeros.Reply{
		wlRegWifiCmd.Path:   {},
		wlIfaceWifiCmd.Path: {},
	}
	legacyAPIfaces = []routeros.Reply{
		{"name": "wlan1", "ssid": "Home", "mode": "ap-bridge", "band": "2ghz-b/g/n",
			"disabled": "false", "running": "true"},
	}
	legacyAPClients = []routeros.Reply{
		{".id": "*1", "interface": "wlan1", "mac-address": "02:00:00:00:01:01", "ap": "false",
			"signal-strength": "-78dBm@1Mbps", "tx-rate": "36Mbps", "rx-rate": "24Mbps",
			"uptime": "4m35s", "band": "2ghz-n"},
		{".id": "*2", "interface": "wlan1", "mac-address": "02:00:00:00:01:02", "ap": "false",
			"signal-strength": "-60@1Mbps", "tx-rate": "11Mbps", "rx-rate": "11Mbps",
			"uptime": "4m31s"},
	}
)

func merged(parts ...map[string][]routeros.Reply) map[string][]routeros.Reply {
	out := map[string][]routeros.Reply{}
	for _, p := range parts {
		for k, v := range p {
			out[k] = v
		}
	}
	return out
}

func clientByMAC(p *WirelessPayload, mac string) *WirelessClient {
	for i := range p.Clients {
		if p.Clients[i].MAC == mac {
			return &p.Clients[i]
		}
	}
	return nil
}

// TestAStandaloneLegacyAPListsItsClients is the reporter's router: legacy
// `wireless`, no CAPsMAN of either kind, on RouterOS 7.24. It latched "wifi" on
// the empty modern table and listed nobody.
func TestAStandaloneLegacyAPListsItsClients(t *testing.T) {
	ros := newCounting(merged(emptyModernMenus, map[string][]routeros.Reply{
		wlRegLegacyCmd.Path: legacyAPClients,
		wlIfaceLegacy.Path:  legacyAPIfaces,
	}))
	c := NewWireless(ros, hub.Relay{}, nil, 30000)
	for tick := 1; tick <= 3; tick++ {
		c.Tick()
		got := c.Last()
		if len(got.Clients) != 2 {
			t.Fatalf("tick %d: %d clients, want 2 - the legacy table was not read", tick, len(got.Clients))
		}
		if got.Mode != "wireless" {
			t.Errorf("tick %d: mode %q, want wireless", tick, got.Mode)
		}
	}
	got := c.Last()
	a := clientByMAC(got, "02:00:00:00:01:01")
	if a == nil {
		t.Fatal("the first client is missing")
	}
	if a.Iface != "wlan1" || a.Signal != -78 || a.TxRate != "36Mbps" || a.RxRate != "24Mbps" ||
		a.Uptime != "4m35s" || a.Band != "2.4GHz" || a.Standard != "Wi-Fi 4" {
		t.Errorf("client fields: %+v", *a)
	}
	if b := clientByMAC(got, "02:00:00:00:01:02"); b == nil || b.Signal != -60 {
		t.Errorf("the second client, whose signal carries no unit: %+v", b)
	}
	// The SSID list on the same page had the same first-answer bug.
	if len(got.SSIDs) != 1 || got.SSIDs[0].SSID != "Home" || got.SSIDs[0].Clients != 2 {
		t.Errorf("ssids = %+v, want Home with 2 clients", got.SSIDs)
	}
	if got.CapsmanAvailable {
		t.Error("CAPsMAN claimed on a router with no /caps-man")
	}
	// The legacy table is read every tick: it exists, so it is never latched.
	if n := ros.asked[wlRegLegacyCmd.Path]; n != 3 {
		t.Errorf("the legacy table was read %d times over 3 ticks, want 3", n)
	}

	// One stack's interface menu timing out keeps the list it had, rather than
	// publishing the other stack's half of it, and is not latched as absent.
	ros.fail[wlIfaceLegacy.Path] = true
	c.Tick()
	if got := c.Last(); len(got.SSIDs) != 1 || got.Mode != "wireless" {
		t.Errorf("after a timeout: ssids %+v, mode %q", got.SSIDs, got.Mode)
	}
	delete(ros.fail, wlIfaceLegacy.Path)
	before := ros.asked[wlIfaceLegacy.Path]
	c.Tick()
	if ros.asked[wlIfaceLegacy.Path] != before+1 {
		t.Error("a timeout was latched as an absent menu")
	}
}

// TestAWifiDriverBoardAsksForTheLegacyMenusOnce: the hAP AX3 and AC2 case. Its
// clients and mode are as before, and the legacy menus it does not have cost one
// refusal each per connection, not one per tick.
func TestAWifiDriverBoardAsksForTheLegacyMenusOnce(t *testing.T) {
	ros := newCounting(map[string][]routeros.Reply{
		wlRegWifiCmd.Path: {{"mac-address": "02:00:00:00:02:01", "interface": "wifi1",
			"ssid": "Home", "signal": "-52", "band": "5ghz-ax", "tx-rate": "1.2Gbps", "uptime": "1h"}},
		wlIfaceWifiCmd.Path: {{"name": "wifi1", "configuration.ssid": "Home",
			"disabled": "false", "running": "true"}},
	})
	c := NewWireless(ros, hub.Relay{}, nil, 30000)
	for i := 0; i < 3; i++ {
		c.Tick()
	}
	got := c.Last()
	if got.Mode != "wifi" || len(got.Clients) != 1 || got.Clients[0].Standard != "Wi-Fi 6" {
		t.Fatalf("mode %q, clients %+v", got.Mode, got.Clients)
	}
	if len(got.SSIDs) != 1 || got.SSIDs[0].Clients != 1 {
		t.Errorf("ssids = %+v", got.SSIDs)
	}
	for _, p := range []string{wlRegLegacyCmd.Path, wlIfaceLegacy.Path} {
		if n := ros.asked[p]; n != 1 {
			t.Errorf("%s asked %d times over 3 ticks, want 1", p, n)
		}
	}
	// A reconnect may follow a package change, so it asks again.
	c.Reconnected()
	if n := ros.asked[wlRegLegacyCmd.Path]; n != 2 {
		t.Errorf("after a reconnect the legacy table was asked %d times in all, want 2", n)
	}
}

// TestALegacyCAPsMANManagerListsItsOwnAndItsCAPsClients: a legacy board that is
// also a legacy (`/caps-man`) manager.
func TestALegacyCAPsMANManagerListsItsOwnAndItsCAPsClients(t *testing.T) {
	ros := newCounting(merged(emptyModernMenus, map[string][]routeros.Reply{
		wlRegLegacyCmd.Path: legacyAPClients[:1],
		wlIfaceLegacy.Path:  legacyAPIfaces,
		capsV1RegCmd.Path: {{"interface": "cap-north-1", "mac-address": "02:00:00:00:03:01",
			"rx-signal": "-41", "tx-rate": "65Mbps", "uptime": "2m"}},
		capsV1IfaceCmd.Path:   {capsV1Iface("cap-north-1", "", "02:00:00:00:0A:02", "cfg-office")},
		capsV1ConfigCmd.Path:  {{"name": "cfg-office", "ssid": "office", "channel": "ch5"}},
		capsV1ChannelCmd.Path: {{"name": "ch5", "band": "5ghz-a/n/ac", "frequency": "5180"}},
	}))
	c := NewWireless(ros, hub.Relay{}, nil, 30000)
	c.Tick()
	c.Tick() // CAPsMAN is first asked on the second tick
	got := c.Last()
	if !got.CapsmanAvailable || got.Mode != "wireless" {
		t.Errorf("capsman %v, mode %q", got.CapsmanAvailable, got.Mode)
	}
	own, onCap := clientByMAC(got, "02:00:00:00:01:01"), clientByMAC(got, "02:00:00:00:03:01")
	if own == nil || own.Source != "" {
		t.Errorf("the manager's own legacy client: %+v", own)
	}
	if onCap == nil || onCap.Source != "capsman" || onCap.Band != "5GHz" || onCap.Signal != -41 {
		t.Errorf("the CAP's client: %+v", onCap)
	}
}

// TestAWifiCAPsMANManagerListsItsCAPsClients: a modern manager, whose CAPs'
// clients are in its own `/interface/wifi/registration-table`, and a legacy
// board that is ALSO a WiFi CAPsMAN manager, where both tables hold clients.
func TestAWifiCAPsMANManagerListsItsCAPsClients(t *testing.T) {
	capClient := routeros.Reply{"mac-address": "02:00:00:00:04:01", "interface": "cap-wifi1",
		"ssid": "Office", "signal": "-48", "band": "5ghz-ax", "uptime": "9m"}
	capIface := routeros.Reply{"name": "cap-wifi1", "configuration.ssid": "Office",
		"disabled": "false", "running": "true"}

	t.Run("modern manager", func(t *testing.T) {
		ros := newCounting(map[string][]routeros.Reply{
			wlRegWifiCmd.Path:   {capClient},
			wlIfaceWifiCmd.Path: {capIface},
		})
		c := NewWireless(ros, hub.Relay{}, nil, 30000)
		c.Tick()
		got := c.Last()
		if got.Mode != "wifi" || clientByMAC(got, "02:00:00:00:04:01") == nil {
			t.Errorf("mode %q, clients %+v", got.Mode, got.Clients)
		}
	})

	t.Run("legacy board as manager", func(t *testing.T) {
		ros := newCounting(map[string][]routeros.Reply{
			wlRegWifiCmd.Path:   {capClient},
			wlIfaceWifiCmd.Path: {capIface},
			wlRegLegacyCmd.Path: legacyAPClients,
			wlIfaceLegacy.Path:  legacyAPIfaces,
		})
		c := NewWireless(ros, hub.Relay{}, nil, 30000)
		c.Tick()
		got := c.Last()
		if len(got.Clients) != 3 || clientByMAC(got, "02:00:00:00:04:01") == nil {
			t.Fatalf("%d clients, want both stacks' 3: %+v", len(got.Clients), got.Clients)
		}
		// Its own radios are legacy, so that is its mode.
		if got.Mode != "wireless" {
			t.Errorf("mode %q, want wireless", got.Mode)
		}
		if len(got.SSIDs) != 2 {
			t.Errorf("ssids = %+v, want Home and Office", got.SSIDs)
		}
	})
}

// TestARouterWithNoWirelessIsNone: RouterOS before 7.13 with no wireless
// package refuses both, and says so once.
func TestARouterWithNoWirelessIsNone(t *testing.T) {
	ros := newCounting(map[string][]routeros.Reply{})
	c := NewWireless(ros, hub.Relay{}, nil, 30000)
	c.Tick()
	c.Tick()
	if got := c.Last(); got.Mode != "none" || len(got.Clients) != 0 {
		t.Errorf("mode %q, clients %+v", got.Mode, got.Clients)
	}
	if n := ros.asked[wlRegWifiCmd.Path] + ros.asked[wlRegLegacyCmd.Path]; n != 2 {
		t.Errorf("the two refused tables were asked %d times over 2 ticks, want 2", n)
	}
}

// TestTopologyPlacesALegacyAPsClients: the topology map had the same rule
// ("legacy only when the modern menu is absent"), so a legacy AP's clients were
// never attributed to it.
func TestTopologyPlacesALegacyAPsClients(t *testing.T) {
	ros := newCounting(map[string][]routeros.Reply{
		topoWifiCmd.Path: {},
		topoRegCmd.Path:  {},
		topoWlCmd.Path:   {{"name": "wlan1", "mac-address": "02:00:00:00:0F:01"}},
		topoWlRegCmd.Path: {{"mac-address": "02:00:00:00:01:01", "interface": "wlan1",
			"signal-strength": "-78@1Mbps", "uptime": "4m35s"}},
	})
	topo := NewTopology(ros, hub.Relay{}, nil, "r1", "lab", 30000)
	radios, _, assoc := topo.readWifi()
	if radios["wlan1"] != "02:00:00:00:0F:01" {
		t.Errorf("radios = %v", radios)
	}
	a, ok := assoc["02:00:00:00:01:01"]
	if !ok || a.Iface != "wlan1" || a.Signal != "-78@1Mbps" {
		t.Errorf("assoc = %+v", assoc)
	}

	// A wifi-driver board that is also a WiFi CAPsMAN manager: its remote CAPs
	// are still read, and the legacy menus are refused once and not asked again
	// until a reconnect.
	ax := newCounting(map[string][]routeros.Reply{
		topoWifiCmd.Path: {{"name": "wifi1", "radio-mac": "02:00:00:00:0E:01"}},
		topoRegCmd.Path:  {{"mac-address": "02:00:00:00:02:01", "interface": "wifi1", "signal": "-50"}},
		topoCapsCmd.Path: {{"identity": "cap-a", "address": "02:00:00:00:0D:00%bridge", "state": "ok"}},
	})
	topo = NewTopology(ax, hub.Relay{}, nil, "r2", "ax", 30000)
	topo.readWifi()
	_, caps, assoc := topo.readWifi()
	if assoc["02:00:00:00:02:01"].Iface != "wifi1" {
		t.Errorf("the wifi-driver client was lost: %+v", assoc)
	}
	if len(caps) != 1 {
		t.Errorf("the manager's remote CAPs were not read: %v", caps)
	}
	if n := ax.asked[topoWlCmd.Path]; n != 1 {
		t.Errorf("the absent legacy menu was asked %d times over 2 reads, want 1", n)
	}
	topo.Reconnected()
	topo.readWifi()
	if n := ax.asked[topoWlCmd.Path]; n < 2 {
		t.Errorf("after a reconnect the legacy menu was asked %d times in all, want it asked again", n)
	}
}

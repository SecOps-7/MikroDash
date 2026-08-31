package collect

// Replay tools/wifiview-cases.js's recorded answers through the port.
//
// THE LEGACY HALF HAS NO OTHER COVERAGE AND CANNOT HAVE ANY. No router in this
// fleet runs `/interface/wireless` — checked, not assumed: the AC2 answers zero
// rows and the AX3 and cAP AX do not have the menu — so `BuildWirelessView` can
// never be reached by a fixture, and a golden that cannot reach it is not a gate
// over it. Without this, roughly half the collector would ship unverified behind
// a green suite.

import (
	"encoding/json"
	"os"
	"testing"

	"mikrodash/internal/routeros"
)

type wifiViewCases struct {
	BandLabel []struct {
		Raw  string `json:"raw"`
		Want string `json:"want"`
	} `json:"bandLabel"`
	SecurityLabel []struct {
		Raw  string `json:"raw"`
		Want string `json:"want"`
	} `json:"securityLabel"`
	BandFromFrequency []struct {
		Raw  string `json:"raw"`
		Want string `json:"want"`
	} `json:"bandFromFrequency"`
	BandFromName []struct {
		Raw  string `json:"raw"`
		Want string `json:"want"`
	} `json:"bandFromName"`

	Wifi []struct {
		Name  string `json:"name"`
		Input struct {
			Ifaces   []routeros.Reply `json:"ifaces"`
			Configs  []routeros.Reply `json:"configs"`
			Security []routeros.Reply `json:"security"`
			Channels []routeros.Reply `json:"channels"`
			Reg      []routeros.Reply `json:"reg"`
		} `json:"input"`
		Want struct {
			Networks []WifiNetwork `json:"networks"`
			Radios   []WifiRadio   `json:"radios"`
		} `json:"want"`
	} `json:"wifi"`

	Wireless []struct {
		Name  string `json:"name"`
		Input struct {
			Ifaces   []routeros.Reply `json:"ifaces"`
			Profiles []routeros.Reply `json:"profiles"`
			Reg      []routeros.Reply `json:"reg"`
		} `json:"input"`
		Want struct {
			Networks    []WifiNetwork    `json:"networks"`
			Radios      []WifiRadio      `json:"radios"`
			SecProfiles []WifiSecProfile `json:"secProfiles"`
		} `json:"want"`
	} `json:"wireless"`
}

func loadWifiViewCases(t *testing.T) wifiViewCases {
	t.Helper()
	b, err := os.ReadFile("../../testdata/wifiview-cases.json")
	if err != nil {
		t.Fatalf("read cases: %v — run: node tools/wifiview-cases.js", err)
	}
	var c wifiViewCases
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse cases: %v", err)
	}
	return c
}

func TestWifiHelpersMatchTheOriginal(t *testing.T) {
	c := loadWifiViewCases(t)
	for _, tc := range c.BandLabel {
		if got := BandLabel(tc.Raw); got != tc.Want {
			t.Errorf("BandLabel(%q) = %q, original = %q", tc.Raw, got, tc.Want)
		}
	}
	for _, tc := range c.SecurityLabel {
		if got := SecurityLabel(tc.Raw); got != tc.Want {
			t.Errorf("SecurityLabel(%q) = %q, original = %q", tc.Raw, got, tc.Want)
		}
	}
	for _, tc := range c.BandFromFrequency {
		if got := BandFromFrequency(tc.Raw); got != tc.Want {
			t.Errorf("BandFromFrequency(%q) = %q, original = %q", tc.Raw, got, tc.Want)
		}
	}
	for _, tc := range c.BandFromName {
		if got := BandFromName(tc.Raw); got != tc.Want {
			t.Errorf("BandFromName(%q) = %q, original = %q", tc.Raw, got, tc.Want)
		}
	}
}

// compare routes through the existing diffJSON in fixture_test.go, which
// reports differences BY PATH — "not deeply equal" on a payload this size is a
// fact without a location.
func compare(t *testing.T, label string, got, want any) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) == string(w) {
		return
	}
	var gv, wv any
	_ = json.Unmarshal(g, &gv)
	_ = json.Unmarshal(w, &wv)
	if d := diffJSON(gv, wv, ""); d != "" {
		t.Errorf("%s differs from the original:\n%s", label, d)
	}
}

func TestBuildWifiViewMatchesTheOriginal(t *testing.T) {
	c := loadWifiViewCases(t)
	if len(c.Wifi) == 0 {
		t.Fatal("no wifi scenarios")
	}
	for _, tc := range c.Wifi {
		nets, radios := BuildWifiView(WifiViewInput{
			Ifaces: tc.Input.Ifaces, Configs: tc.Input.Configs,
			Security: tc.Input.Security, Channels: tc.Input.Channels, Reg: tc.Input.Reg,
		})
		compare(t, tc.Name+" networks", SortNetworks(nets), tc.Want.Networks)
		compare(t, tc.Name+" radios", radios, tc.Want.Radios)
	}
}

func TestBuildWirelessViewMatchesTheOriginal(t *testing.T) {
	c := loadWifiViewCases(t)
	if len(c.Wireless) == 0 {
		t.Fatal("no wireless scenarios — this is the half no fixture can reach")
	}
	var rows int
	for _, tc := range c.Wireless {
		nets, radios, secs := BuildWirelessView(WirelessViewInput{
			Ifaces: tc.Input.Ifaces, Profiles: tc.Input.Profiles, Reg: tc.Input.Reg,
		})
		rows += len(nets)
		compare(t, tc.Name+" networks", SortNetworks(nets), tc.Want.Networks)
		compare(t, tc.Name+" radios", radios, tc.Want.Radios)
		compare(t, tc.Name+" secProfiles", secs, tc.Want.SecProfiles)
	}
	// A corpus that stopped producing rows would pass every assertion above
	// while covering nothing — and this is the ONLY coverage this half has.
	if rows < 4 {
		t.Errorf("only %d legacy rows exercised; this is the half with no golden", rows)
	}
}

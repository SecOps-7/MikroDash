package collect

import (
	"encoding/json"
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// TestAClientCarriesItsMFPAndEAPIdentity. RouterOS 7.25 added `mfp` and
// `eap-identity` to the Wi-Fi registration table; both names are in the 7.25.1
// field list on CHR Test. A router that does not report `mfp` must send null,
// not false: "not reported" is not "unprotected".
func TestAClientCarriesItsMFPAndEAPIdentity(t *testing.T) {
	row := routeros.Reply{"mac-address": "02:00:00:00:00:01", "interface": "wifi1",
		"mfp": "true", "eap-identity": " alice@example.org "}
	c := parseWirelessClient(row, false, "", "", CapsBand{})
	if c.MFP == nil || !*c.MFP {
		t.Errorf("mfp=true read as %v", c.MFP)
	}
	if c.EAPIdentity != "alice@example.org" {
		t.Errorf("eap-identity read as %q", c.EAPIdentity)
	}

	row["mfp"] = "false"
	if c := parseWirelessClient(row, false, "", "", CapsBand{}); c.MFP == nil || *c.MFP {
		t.Errorf("mfp=false read as %v", c.MFP)
	}

	// A 7.24 router: neither field. CAPsMAN rows take the same path.
	old := routeros.Reply{"mac-address": "02:00:00:00:00:02", "interface": "wifi1"}
	for _, capsman := range []bool{false, true} {
		c := parseWirelessClient(old, capsman, "", "", CapsBand{})
		if c.MFP != nil || c.EAPIdentity != "" {
			t.Errorf("capsman=%v: an older router's client reads mfp %v, identity %q", capsman, c.MFP, c.EAPIdentity)
		}
		b, _ := json.Marshal(c)
		if !strings.Contains(string(b), `"mfp":null`) {
			t.Errorf("capsman=%v: not reported must reach the page as null: %s", capsman, b)
		}
	}
}

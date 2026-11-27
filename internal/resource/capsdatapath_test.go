package resource

import (
	"slices"
	"strings"
	"testing"
)

// A CAPsMAN DATAPATH SAVE NAMES ONLY WHAT /interface/wifi/datapath HAS.
//
// The form carried `local-forwarding`, the legacy /caps-man/datapath's
// property, which the wifi menu has never had (RouterOS 7.25.2 CHR,
// /console/inspect; rosetta 7.9 to 7.26beta4). As a checkbox it went out as
// `=local-forwarding=no` on every add and every edit, so the router refused
// every datapath write. Its Traffic Processing picker offered that same word as
// a value, and lacked two of the three the router has, so a profile set to
// `on-cap` could not be saved at all.
func TestADatapathSaveNamesOnlyWhatTheWifiMenuHas(t *testing.T) {
	// The arguments /interface/wifi/datapath add accepts, recorded off the CHR.
	accepted := []string{"bridge", "bridge-cost", "bridge-horizon", "client-isolation", "comment",
		"copy-from", "disabled", "interface-list", "name", "traffic-processing", "vlan-id"}
	for _, editing := range []bool{false, true} {
		v, errs := CapsDatapath.Validate(map[string]string{"name": "dp1"}, editing)
		if len(errs) != 0 {
			t.Fatalf("editing=%v: %v", editing, errs)
		}
		for _, a := range CapsDatapath.BuildArgs(v) {
			name := strings.TrimLeft(strings.SplitN(strings.TrimPrefix(a, "="), "=", 2)[0], "!")
			if !slices.Contains(accepted, name) {
				t.Errorf("editing=%v: the save sends %q, which /interface/wifi/datapath refuses", editing, a)
			}
		}
	}

	// Every value the router has is offered and saves; the legacy word does not.
	for _, tp := range []string{"on-cap", "on-capsman", "on-capsman-secure"} {
		if _, errs := CapsDatapath.Validate(map[string]string{"name": "dp1", "trafficProcessing": tp}, false); len(errs) != 0 {
			t.Errorf("traffic-processing=%s is refused: %v", tp, errs)
		}
	}
	if _, errs := CapsDatapath.Validate(map[string]string{"name": "dp1", "trafficProcessing": "local-forwarding"}, false); len(errs) == 0 {
		t.Error("traffic-processing=local-forwarding is accepted, and the router has no such value")
	}
}

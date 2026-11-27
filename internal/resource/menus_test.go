package resource

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// THE WRITE FORMS ARE HELD TO THE ROUTER, NOT TO A COPY OF AN OLDER APP.
//
// Fifteen forms were held to the Node app's own declarations until 2026-11-27.
// That was the weaker reference: it pinned a property the menu has never had
// (the wifi datapath's local-forwarding, so every datapath save was refused)
// and lists the router had outgrown (a CAPsMAN configuration in
// station-pseudobridge could not be saved). They are held here instead, as the
// firewall forms are, to what the router accepts.
//
// THE REFERENCE IS COMMITTED, AND NO TEST HERE TALKS TO A ROUTER. Thirteen menus
// are recorded off the lab CHR by cmd/menurecord (read-only) into
// testdata/menu-properties.json; the two legacy wireless menus, which the CHR
// cannot show without a write, are MikroTik's command tree via rosetta in
// testdata/wireless-properties.json. Refreshing either is a deliberate act, and
// its diff is reviewed like any other.
//
// Subsets are allowed, unlike the firewall's: these forms carry the properties
// worth editing, not every one a menu has. What fails:
//
//   - a property the form writes that the menu does not accept, which fails
//     every save that sends it;
//   - a hard select that lacks a value the router has, which makes a row
//     holding that value unsavable, or offers one it does not, which the router
//     refuses;
//   - a suggestion list offering an item the router does not know, or missing
//     one it does (a comma list is checked item by item);
//   - a fixed list with no recorded values, or recorded values for a field
//     with no fixed list: re-run the recorder.

type menuRecording struct {
	Measured string `json:"_measured"`
	How      string `json:"_how"`
	Version  string `json:"_routeros"`
	Menus    map[string]struct {
		Verb   string              `json:"verb"`
		Props  []string            `json:"props"`
		Values map[string][]string `json:"values"`
	} `json:"menus"`
}

// routerHeld are the forms each recording holds, so the recordings and this
// list must agree in both directions.
var routerHeld = map[string][]*Resource{
	"testdata/menu-properties.json": {Bridge, BridgePort, Vlan, Route, Route6, DHCPLease, WgPeer, WifiNet,
		CapsProvisioningRes, CapsConfig, CapsSecurity, CapsChannel, CapsDatapath},
	"testdata/wireless-properties.json": {WlNet, WlSecProfile},
}

func readMenuRecording(t *testing.T, path string) menuRecording {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec menuRecording
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if rec.Version == "" || rec.How == "" || rec.Measured == "" {
		t.Fatalf("%s does not say what it was recorded from (_measured, _how, _routeros)", path)
	}
	return rec
}

// fixedList is the static list a field offers: a hard select's options or a
// text field's suggestions. A list read from a router menu when the form opens
// is not one, and is not compared.
func fixedList(f Field) []string {
	if f.OptionsFrom != nil {
		return f.OptionsFrom.Values
	}
	return f.Options
}

func TestFormsMatchTheirMenus(t *testing.T) {
	for path, forms := range routerHeld {
		rec := readMenuRecording(t, path)
		held := map[string]bool{}
		for _, res := range forms {
			held[res.Menu] = true
		}
		for m := range rec.Menus {
			if !held[m] {
				t.Errorf("%s records %s, which no form here is held to", path, m)
			}
		}
		for _, res := range forms {
			t.Run(res.Key, func(t *testing.T) {
				want, ok := rec.Menus[res.Menu]
				if !ok {
					t.Fatalf("%s has no recording of %s: re-run the recorder", path, res.Menu)
				}
				if verb := map[bool]string{false: "add", true: "set"}[res.NoCreate]; want.Verb != verb {
					t.Errorf("%s was recorded for %s, and the form uses %s", res.Menu, want.Verb, verb)
				}
				withList := map[string]bool{}
				for _, f := range res.Fields {
					if f.Display || f.ROS == "" {
						continue
					}
					if !slices.Contains(want.Props, f.ROS) {
						t.Errorf("the form writes %s, which %s %s refuses", f.ROS, res.Menu, want.Verb)
					}
					list := nonEmpty(fixedList(f))
					if len(list) == 0 {
						continue
					}
					withList[f.ROS] = true
					router, ok := want.Values[f.ROS]
					if !ok {
						t.Errorf("%s offers a fixed list and the recording has no values for it: re-run the recorder", f.ROS)
						continue
					}
					if f.Type == TypeSelect {
						if a, b := sorted(list), sorted(router); strings.Join(a, ",") != strings.Join(b, ",") {
							t.Errorf("%s offers %v, the router %v", f.ROS, a, b)
						}
						continue
					}
					items := map[string]bool{}
					for _, v := range list {
						for _, it := range strings.Split(v, ",") {
							items[it] = true
							if !slices.Contains(router, it) {
								t.Errorf("%s suggests %q, which the router does not know", f.ROS, it)
							}
						}
					}
					for _, v := range router {
						if !items[v] {
							t.Errorf("%s does not suggest %q, which the router has", f.ROS, v)
						}
					}
				}
				for p := range want.Values {
					if !withList[p] {
						t.Errorf("the recording has values for %s, which the form gives no fixed list", p)
					}
				}
			})
		}
	}
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func sorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

package resource

import (
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

type fwRecording struct {
	Menus map[string]struct {
		Props     []string            `json:"props"`
		Negatable []string            `json:"negatable"`
		Values    map[string][]string `json:"values"`
	} `json:"menus"`
}

func readFWRecording(t *testing.T) fwRecording {
	t.Helper()
	b, err := os.ReadFile("testdata/firewall-properties.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec fwRecording
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.Menus) != 8 {
		t.Fatalf("the recording holds %d menus, want the 8 firewall tables", len(rec.Menus))
	}
	return rec
}

var fwResources = []*Resource{FWFilter, FWNat, FWMangle, FWRaw, FWFilter6, FWNat6, FWMangle6, FWRaw6}

// fwTyped are properties the router completes from a vocabulary but which take
// a COMMA LIST of it, some with a `!` per item, so the form gives a text box
// with the vocabulary in its help rather than a picker that holds one value.
var fwTyped = map[string]bool{
	"connection-state": true, "connection-nat-state": true, "tcp-flags": true,
	"hotspot": true, "headers": true,
	// A duration or one of two words: a picker would refuse `1d`.
	"address-list-timeout": true,
	// A number OR a keyword, so a picker would refuse the number; the keywords
	// are in the help. The marks complete from the router's own marks.
	"new-mss": true, "new-dscp": true, "new-priority": true,
	"new-connection-mark": true, "new-packet-mark": true,
	// Checkboxes: the vocabulary is yes/no.
	"log": true, "disabled": true, "passthrough": true,
}

// fwPerItem negate item by item (`syn,!ack`), so they carry no whole-value
// toggle although the router's completion offers a `!`.
var fwPerItem = map[string]bool{"tcp-flags": true, "hotspot": true}

// TestFirewallFormsMatchTheRouter holds each firewall form to what its menu
// accepts, recorded off RouterOS 7.24.1, in BOTH directions: a property the
// form offers and the menu refuses fails every save; one the menu has and the
// form lacks is invisible, which is what the operator reported (2026-10-03).
func TestFirewallFormsMatchTheRouter(t *testing.T) {
	rec := readFWRecording(t)
	for _, res := range fwResources {
		t.Run(res.Key, func(t *testing.T) {
			want, ok := rec.Menus[res.Menu]
			if !ok {
				t.Fatalf("no recording for %s", res.Menu)
			}
			got := map[string]Field{}
			for _, f := range res.Fields {
				if !f.Display {
					got[f.ROS] = f
				}
			}
			for _, p := range want.Props {
				if _, ok := got[p]; !ok {
					t.Errorf("%s accepts %s and the form does not offer it", res.Menu, p)
				}
			}
			for p := range got {
				if !slices.Contains(want.Props, p) {
					t.Errorf("the form offers %s, which %s refuses", p, res.Menu)
				}
			}

			for p, f := range got {
				neg := slices.Contains(want.Negatable, p) && !fwPerItem[p]
				if f.Negatable != neg {
					t.Errorf("%s negatable=%v, the router says %v", p, f.Negatable, neg)
				}
			}

			for p, vocab := range want.Values {
				f, ok := got[p]
				if !ok || fwTyped[p] {
					continue
				}
				if f.OptionsFrom == nil {
					t.Errorf("%s has a vocabulary on the router and no picker here", p)
					continue
				}
				if f.OptionsFrom.Menu != "" {
					continue // the router's own list, read when the form opens
				}
				a := append([]string{}, f.OptionsFrom.Values...)
				b := append([]string{}, vocab...)
				sort.Strings(a)
				sort.Strings(b)
				if strings.Join(a, ",") != strings.Join(b, ",") {
					t.Errorf("%s offers %v, the router %v", p, a, b)
				}
			}
		})
	}
}

// TestFirewallFormsAreTabbedAndComplete: every field is on one of WinBox's
// five tabs, and the statistics are shown, never sent.
func TestFirewallFormsAreTabbedAndComplete(t *testing.T) {
	tabs := map[string]bool{fwTabGeneral: true, fwTabAdvanced: true, fwTabExtra: true,
		fwTabAction: true, fwTabStats: true}
	for _, res := range fwResources {
		for _, f := range res.Fields {
			if !tabs[f.Tab] {
				t.Errorf("%s.%s is on tab %q", res.Key, f.Name, f.Tab)
			}
			if (f.Tab == fwTabStats) != f.Display {
				t.Errorf("%s.%s: only the statistics are display-only", res.Key, f.Name)
			}
		}
	}
	// The wire names the form had before stay put: history and the guard read
	// them.
	for _, n := range []string{"chain", "action", "srcAddress", "dstAddress", "protocol", "srcPort",
		"dstPort", "inInterface", "outInterface", "connectionState", "rejectWith", "log",
		"logPrefix", "comment", "disabled"} {
		if FWFilter.FieldByName(n) == nil {
			t.Errorf("fwFilter lost the field %s", n)
		}
	}
}

// TestAFirewallEditClearsOnlyWhatTheRowHolds. Every optional match clears with
// `=!prop=`, since the router refuses an empty one; the fields before this were
// not clearable at all, so emptying Source Address left it on the router. With
// the stored row, an edit removes only what the row has.
func TestAFirewallEditClearsOnlyWhatTheRowHolds(t *testing.T) {
	stored := map[string]string{".id": "*1", "chain": "input", "action": "drop",
		"src-address": "10.0.0.0/8", "src-address-list": "blocked", "comment": "x"}
	values := map[string]string{"chain": "input", "action": "drop", "comment": "y",
		"srcAddressList": "blocked"} // srcAddress emptied
	v, errs := FWFilter.Validate(values, true)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	v.Stored = stored
	args := strings.Join(FWFilter.BuildArgs(v), " ")
	if !strings.Contains(args, "=!src-address=") {
		t.Errorf("an emptied source address is not removed: %s", args)
	}
	if !strings.Contains(args, "=src-address-list=blocked") {
		t.Errorf("a kept match is not sent: %s", args)
	}
	if n := strings.Count(args, "=!"); n != 1 {
		t.Errorf("%d removals, want 1: an absent property needs none: %s", n, args)
	}

	// Without the row (an undo), every empty match is removed, which is longer
	// and as correct.
	v.Stored = nil
	if n := strings.Count(strings.Join(FWFilter.BuildArgs(v), " "), "=!"); n < 40 {
		t.Errorf("without the row only %d removals", n)
	}
}

// TestANegatedMatchIsWrittenAsTheRouterSpellsIt: the form's "not" toggle is a
// leading `!` on the value, and the write path passes it through.
func TestANegatedMatchIsWrittenAsTheRouterSpellsIt(t *testing.T) {
	v, errs := FWFilter.Validate(map[string]string{"chain": "input", "action": "drop",
		"srcAddress": "!10.0.0.0/8", "protocol": "!udp", "inInterfaceList": "!LAN"}, false)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	args := strings.Join(FWFilter.BuildArgs(v), " ")
	for _, w := range []string{"=src-address=!10.0.0.0/8", "=protocol=!udp", "=in-interface-list=!LAN"} {
		if !strings.Contains(args, w) {
			t.Errorf("missing %s in %s", w, args)
		}
	}
}

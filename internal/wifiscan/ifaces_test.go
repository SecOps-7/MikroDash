package wifiscan

import (
	"encoding/json"
	"os"
	"testing"
)

type ifacesCorpus struct {
	Cases []struct {
		Name    string      `json:"name"`
		All     []Catalogue `json:"all"`
		Clients []string    `json:"clients"`
		Out     []Scannable `json:"out"`
	} `json:"cases"`
}

func TestScannableInterfacesMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/wifiscan-ifaces-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c ifacesCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}

	sawRollup := false
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := ScannableInterfaces(tc.All, tc.Clients)
			if len(got) != len(tc.Out) {
				t.Fatalf("offered %d radios, live offered %d (%v vs %v)",
					len(got), len(tc.Out), got, tc.Out)
			}
			for i := range got {
				if got[i] != tc.Out[i] {
					t.Errorf("radio %d is %+v, live %+v", i, got[i], tc.Out[i])
				}
			}
		})
		// A case where a radio reports more clients than are directly on it is
		// what proves the roll-up happens at all.
		for _, s := range tc.Out {
			direct := 0
			for _, name := range tc.Clients {
				if name == s.Name {
					direct++
				}
			}
			if s.Clients > direct {
				sawRollup = true
			}
		}
	}
	if !sawRollup {
		t.Error("no corpus case reports more clients than sit directly on the radio, " +
			"so nothing here proves the virtual APs are rolled up")
	}
}

// TestTheWarningNeverUndercountsWhatAScanWouldDrop states the property the
// dialog's warning rests on, independently of the corpus.
//
// An operator decides whether to disrupt a radio from this number. Undercounting
// is the dangerous direction: it makes a costly scan look cheap.
func TestTheWarningNeverUndercountsWhatAScanWouldDrop(t *testing.T) {
	all := []Catalogue{
		{Name: "wifi1", Master: true, Running: true},
		{Name: "wifi1-guest", MasterInterface: "wifi1"},
		{Name: "wifi1-iot", MasterInterface: "wifi1"},
		{Name: "wifi2", Master: true, Running: true},
	}
	clients := []string{"wifi1", "wifi1-guest", "wifi1-guest", "wifi1-iot", "wifi2"}

	got := ScannableInterfaces(all, clients)
	byName := map[string]int{}
	for _, s := range got {
		byName[s.Name] = s.Clients
	}
	if byName["wifi1"] != 4 {
		t.Errorf("wifi1 reports %d clients; a scan of it drops 4", byName["wifi1"])
	}
	if byName["wifi2"] != 1 {
		t.Errorf("wifi2 reports %d clients, want 1 -- another radio's clients leaked in",
			byName["wifi2"])
	}
	if len(got) != 2 {
		t.Errorf("%d radios offered, want 2 -- the virtual APs are being offered", len(got))
	}
}

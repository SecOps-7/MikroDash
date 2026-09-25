package wifiscan

import (
	"encoding/json"
	"os"
	"testing"
)

type catCorpus struct {
	WifiEndpoint string `json:"wifiEndpoint"`
	Cases        []struct {
		Name     string           `json:"name"`
		Endpoint *string          `json:"endpoint"`
		Rows     []map[string]any `json:"rows"`
		Out      []struct {
			Name            string  `json:"name"`
			ID              *string `json:"id"`
			Master          bool    `json:"master"`
			MasterInterface *string `json:"masterInterface"`
			CapsmanManaged  bool    `json:"capsmanManaged"`
			Disabled        bool    `json:"disabled"`
			Running         bool    `json:"running"`
		} `json:"out"`
	} `json:"cases"`
}

func TestParseCatalogueMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/wifiscan-catalogue-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c catCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- this test would pass against nothing")
	}
	// The endpoint is read out of the LIVE collector by the generator. If it
	// moves, this fails rather than this port quietly scanning a stack whose
	// command it has never seen.
	if c.WifiEndpoint != WifiEndpoint {
		t.Fatalf("the live wifi endpoint is %q, this port has %q", c.WifiEndpoint, WifiEndpoint)
	}

	sawRefusal := false
	for _, tc := range c.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			ep := ""
			if tc.Endpoint != nil {
				ep = *tc.Endpoint
			}
			got := ParseCatalogue(tc.Rows, ep)
			if len(got) != len(tc.Out) {
				t.Fatalf("parsed %d rows, live parsed %d", len(got), len(tc.Out))
			}
			if len(tc.Out) == 0 && len(tc.Rows) > 0 {
				sawRefusal = true
			}
			for i, want := range tc.Out {
				g := got[i]
				wantID, wantMI := "", ""
				if want.ID != nil {
					wantID = *want.ID
				}
				if want.MasterInterface != nil {
					wantMI = *want.MasterInterface
				}
				if g.Name != want.Name || g.ID != wantID || g.MasterInterface != wantMI {
					t.Errorf("row %d names %q/%q/%q, live %q/%q/%q",
						i, g.Name, g.ID, g.MasterInterface, want.Name, wantID, wantMI)
				}
				if g.Master != want.Master || g.CapsmanManaged != want.CapsmanManaged ||
					g.Disabled != want.Disabled || g.Running != want.Running {
					t.Errorf("row %q flags master=%v capsman=%v disabled=%v running=%v, "+
						"live master=%v capsman=%v disabled=%v running=%v",
						g.Name, g.Master, g.CapsmanManaged, g.Disabled, g.Running,
						want.Master, want.CapsmanManaged, want.Disabled, want.Running)
				}
			}
		})
	}
	if !sawRefusal {
		t.Error("no corpus case offers rows and gets nothing back, so the legacy-stack " +
			"refusal is untested")
	}
}

// TestTheLegacyStackIsRefusedOutright, stated directly.
//
// Offering a picker on a stack whose scan command differs would fail AFTER the
// radio was already off the air, which is the worst place for it to fail.
func TestTheLegacyStackIsRefusedOutright(t *testing.T) {
	rows := []map[string]any{{".id": "*1", "name": "wlan1", "master": "true"}}
	for _, ep := range []string{"/interface/wireless", "", "/interface/wifi", "anything else"} {
		if got := ParseCatalogue(rows, ep); len(got) != 0 {
			t.Errorf("endpoint %q produced %d scannable radios", ep, len(got))
		}
	}
	if got := ParseCatalogue(rows, WifiEndpoint); len(got) != 1 {
		t.Errorf("the supported endpoint produced %d radios, want 1 -- the refusal is "+
			"refusing everything", len(got))
	}
}

// TestAStringFalseIsNotTrue is the coercion a port gets wrong.
func TestAStringFalseIsNotTrue(t *testing.T) {
	rows := []map[string]any{
		{".id": "*1", "name": "a", "master": "false", "disabled": "false", "running": "false"},
		{".id": "*2", "name": "b", "master": "true", "disabled": "true", "running": "true"},
		{".id": "*3", "name": "c", "master": true, "disabled": false, "running": true},
		{".id": "*4", "name": "d"},
	}
	got := ParseCatalogue(rows, WifiEndpoint)
	if got[0].Master || got[0].Disabled || got[0].Running {
		t.Errorf(`the string "false" read as true: %+v -- every radio would look disabled `+
			`and the dialog would offer none`, got[0])
	}
	if !got[1].Master || !got[1].Disabled || !got[1].Running {
		t.Errorf(`the string "true" did not read as true: %+v`, got[1])
	}
	if !got[2].Master || got[2].Disabled || !got[2].Running {
		t.Errorf("a real boolean was misread: %+v", got[2])
	}
	if got[3].Master || got[3].Disabled || got[3].Running {
		t.Errorf("absent flags did not default to false: %+v", got[3])
	}
}

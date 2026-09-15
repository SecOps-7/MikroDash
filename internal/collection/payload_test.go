package collection

// Payload against what the LIVE `_collectionPayload` produced.
//
// The collection-payload corpus slices the function out of `src/index.js`
// and feeds it a resolution taken from the live `resolveCollection`, so both
// halves are the originals. The corpus records the payload and, separately, the
// ORDER of `eff.enabled`'s keys.

import (
	"encoding/json"
	"os"
	"testing"
)

type payloadCase struct {
	Why      string          `json:"why"`
	Settings map[string]any  `json:"settings"`
	Router   json.RawMessage `json:"router"`
	Payload  struct {
		RouterID string          `json:"routerId"`
		Mode     string          `json:"mode"`
		Enabled  map[string]bool `json:"enabled"`
	} `json:"payload"`
	EnabledOrder []string `json:"enabledOrder"`
}

func loadPayloadCases(t *testing.T) []payloadCase {
	t.Helper()
	b, err := os.ReadFile("../../testdata/collection-payload-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/collection-payload-cases.js", err)
	}
	var doc struct {
		Cases []payloadCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("corpus is empty")
	}
	return doc.Cases
}

// payloadRouter reuses collection_test.go's `routerFor` rather than decoding the
// record a second time.
//
// That function handles `overrides` arriving as `any` on purpose — the
// resolve corpus carries a case where it is a STRING, because a hand-edited
// routers.json can hold either. A second decoder here would be a second
// implementation of that decision, and the two would drift.
func payloadRouter(raw json.RawMessage) *Router {
	var rc resolveCase
	if len(raw) > 0 {
		// The field name differs between the two corpora — `router` here,
		// `record` there — so the bytes are decoded into Record directly.
		if err := json.Unmarshal(raw, &rc.Record); err != nil {
			panic(err)
		}
	}
	return routerFor(rc)
}

func TestPayloadMatchesLive(t *testing.T) {
	for _, c := range loadPayloadCases(t) {
		c := c
		t.Run(c.Why, func(t *testing.T) {
			got := Payload("r-under-test", Resolve(c.Settings, payloadRouter(c.Router)))

			if got["routerId"] != c.Payload.RouterID {
				t.Errorf("routerId = %v, live sent %q", got["routerId"], c.Payload.RouterID)
			}
			if got["mode"] != c.Payload.Mode {
				t.Errorf("mode = %v, live sent %q", got["mode"], c.Payload.Mode)
			}

			enabled, _ := got["enabled"].(map[string]bool)
			for k, want := range c.Payload.Enabled {
				if enabled[k] != want {
					t.Errorf("enabled[%s] = %v, live sent %v", k, enabled[k], want)
				}
			}
			// AND NO EXTRA KEYS: a collector this side knows and the live registry
			// does not would be offered a checkbox nothing serves.
			for k := range enabled {
				if _, ok := c.Payload.Enabled[k]; !ok {
					t.Errorf("enabled carries %s, which the live payload does not", k)
				}
			}
		})
	}
}

// TestThePayloadHasNoOffList — `off` left the payload with per-router switching
// (2026-09-15). Checked as ABSENT rather than empty, so a browser cannot go on
// reading a list that is always empty and take it for a real answer.
func TestThePayloadHasNoOffList(t *testing.T) {
	got := Payload("r1", Resolve(map[string]any{}, &Router{}))
	if _, ok := got["off"]; ok {
		t.Error("the collection:config payload still carries off")
	}
}

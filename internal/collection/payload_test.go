package collection

// Payload against what the LIVE `_collectionPayload` produced.
//
// The collection-payload corpus slices the function out of `src/index.js`
// and feeds it a resolution taken from the live `resolveCollection`, so both
// halves are the originals. The corpus records the payload and, separately, the
// ORDER of `eff.enabled`'s keys — which is what `off` is ordered by.

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
		Off      []string        `json:"off"`
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
// That function handles `off` and `overrides` arriving as `any` on purpose — the
// resolve corpus carries a case where each is a STRING, because a hand-edited
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

			// THE ORDER OF `off` IS THE POINT. Compared as a sequence, not a set:
			// registry order is neither alphabetical nor the operator's own list,
			// and a sorted implementation would pass a set comparison.
			off, _ := got["off"].([]string)
			if len(off) != len(c.Payload.Off) {
				t.Fatalf("off = %v, live sent %v", off, c.Payload.Off)
			}
			for i := range off {
				if off[i] != c.Payload.Off[i] {
					t.Fatalf("off = %v, live sent %v — the ORDER differs, and it is "+
						"registry order rather than sorted", off, c.Payload.Off)
				}
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

// TestOffIsRegistryOrderNotSorted — the assertion the corpus exists to allow.
//
// A set comparison would pass on a sorted implementation. The corpus deliberately
// carries two lists that are NOT alphabetical (`wifi, logs` and the cascade's
// `conns, bandwidth`), so this can fail one.
func TestOffIsRegistryOrderNotSorted(t *testing.T) {
	discriminating := 0
	for _, c := range loadPayloadCases(t) {
		if len(c.Payload.Off) < 2 {
			continue
		}
		sorted := true
		for i := 1; i < len(c.Payload.Off); i++ {
			if c.Payload.Off[i] < c.Payload.Off[i-1] {
				sorted = false
			}
		}
		if !sorted {
			discriminating++
		}
	}
	if discriminating == 0 {
		t.Error("no case in the corpus has an `off` list that is out of alphabetical order, " +
			"so nothing here can tell registry order from a sorted implementation. Add a case " +
			"whose disabled collectors are not alphabetical.")
	}
}

// TestOffIsNeverNil — `[]` and `null` are different on the wire, and the live
// payload always carries an array.
func TestOffIsNeverNil(t *testing.T) {
	got := Payload("r1", Resolve(map[string]any{}, &Router{}))
	off, ok := got["off"].([]string)
	if !ok {
		t.Fatalf("off is %T", got["off"])
	}
	if off == nil {
		t.Error("off is nil, which marshals as null; the live payload sends []")
	}
	b, _ := json.Marshal(got["off"])
	if string(b) != "[]" {
		t.Errorf("an unconfigured router sends off = %s, want []", b)
	}
}

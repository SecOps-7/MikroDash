package principals

// `ParseName` against the LIVE `_parseName`, lifted and run by
// The name corpus.

import (
	"encoding/json"
	"os"
	"testing"
)

type nameCorpus struct {
	Cases map[string]struct {
		Body    map[string]any `json:"body"`
		Partial bool           `json:"partial"`
		Error   *string        `json:"error"`
		Value   map[string]any `json:"value"`
	} `json:"cases"`
}

func TestParseNameMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/name-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c nameCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty -- this test would pass against nothing")
	}

	// Believability: both outcomes appear, and at least one case has a key
	// ABSENT rather than null — without the third, the trap this file is written
	// around goes unexercised.
	var refused, accepted, hasAbsent bool
	for _, tc := range c.Cases {
		if tc.Error != nil {
			refused = true
			continue
		}
		accepted = true
		if _, sent := tc.Body["description"]; !sent {
			if _, written := tc.Value["description"]; !written {
				hasAbsent = true
			}
		}
	}
	if !refused || !accepted || !hasAbsent {
		t.Fatalf("the corpus is not discriminating (refused=%v accepted=%v absent=%v)",
			refused, accepted, hasAbsent)
	}

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseName(tc.Body, tc.Partial)

			if tc.Error != nil {
				if err == nil {
					t.Fatalf("accepted a body the live function refused with %q", *tc.Error)
				}
				if err.Error() != *tc.Error {
					t.Errorf("error %q, live %q", err.Error(), *tc.Error)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused with %q; the live function accepted it", err)
			}

			mine := got.Columns()
			if len(mine) != len(tc.Value) {
				t.Fatalf("wrote %s\n        live %s", mustJSON(mine), mustJSON(tc.Value))
			}
			for k, wv := range tc.Value {
				mv, ok := mine[k]
				if !ok {
					t.Errorf("%s is ABSENT here and present in the live value (%v) -- "+
						"absent and null write different rows", k, wv)
					continue
				}
				if mustJSON(mv) != mustJSON(wv) {
					t.Errorf("%s = %v, live %v", k, mv, wv)
				}
			}
		})
	}
}

// TestAnAbsentDescriptionIsNotAClearedOne.
//
// Stated on its own because it is the failure with consequences: renaming a
// group must not blank its description, and Go's `m["description"]` is nil for a
// missing key exactly as it is for a JSON null.
func TestAnAbsentDescriptionIsNotAClearedOne(t *testing.T) {
	absent, err := ParseName(map[string]any{"name": "Ops"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if absent.HasDescription {
		t.Error("a rename claimed to write the description column")
	}

	cleared, err := ParseName(map[string]any{"name": "Ops", "description": nil}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.HasDescription || cleared.Description != nil {
		t.Errorf("an explicit null did not clear (Has=%v Description=%v)",
			cleared.HasDescription, cleared.Description)
	}

	// ...and an EMPTY STRING clears it too: the live `d || null` collapses them.
	empty, err := ParseName(map[string]any{"name": "Ops", "description": "  "}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !empty.HasDescription || empty.Description != nil {
		t.Error("an empty description did not write NULL")
	}
}

// TestAnAbsentNameIsOnlyAllowedOnAnEdit.
//
// A create with no name is refused; an edit with no name simply does not write
// the column. A port that wrote `name: ""` on an edit would blank the group.
func TestAnAbsentNameIsOnlyAllowedOnAnEdit(t *testing.T) {
	if _, err := ParseName(map[string]any{}, false); err == nil {
		t.Error("a create with no name was accepted")
	}
	f, err := ParseName(map[string]any{"description": "x"}, true)
	if err != nil {
		t.Fatalf("an edit with no name was refused: %v", err)
	}
	if f.HasName {
		t.Error("an absent name was written on an edit")
	}
	if _, ok := f.Columns()["name"]; ok {
		t.Error("the column map carries a name that was never sent")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

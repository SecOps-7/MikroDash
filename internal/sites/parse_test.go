package sites

// `ParseSiteBody` against the LIVE `_parseSiteBody`, lifted and run by
// The site-body corpus.

import (
	"encoding/json"
	"os"
	"testing"
)

type bodyCorpus struct {
	Cases map[string]struct {
		Body    map[string]any `json:"body"`
		Partial bool           `json:"partial"`
		Error   *string        `json:"error"`
		Value   map[string]any `json:"value"`
	} `json:"cases"`
}

// asWritten renders a Patch the way the live function's `value` object looks, so
// the two can be compared key for key. AN ABSENT KEY AND A NULL ONE ARE
// DIFFERENT ENTRIES, which is the entire point of the corpus.
func asWritten(p Patch) map[string]any {
	out := map[string]any{}
	if p.HasName {
		out["name"] = p.Name
	}
	if p.HasDescription {
		if p.Description == nil {
			out["description"] = nil
		} else {
			out["description"] = *p.Description
		}
	}
	if p.HasPlace {
		if p.Place == nil {
			for _, k := range []string{"lat", "lon", "place_name", "place_region", "place_cc"} {
				out[k] = nil
			}
		} else {
			out["lat"] = p.Place.Lat
			out["lon"] = p.Place.Lon
			out["place_name"] = p.Place.Name
			out["place_region"] = p.Place.Region
			out["place_cc"] = p.Place.CC
		}
	}
	return out
}

func TestParseSiteBodyMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/site-body-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c bodyCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty, so this test would pass against nothing")
	}

	// Believability: the corpus must contain BOTH refusals and acceptances, and
	// at least one case where a key is absent rather than null. Without the
	// third, "absent stays absent" is untested and the trap this file is written
	// around goes unexercised.
	var refused, accepted, hasAbsent bool
	for _, tc := range c.Cases {
		if tc.Error != nil {
			refused = true
			continue
		}
		accepted = true
		if _, ok := tc.Value["place_name"]; !ok {
			if _, sent := tc.Body["place"]; !sent {
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
			got, err := ParseSiteBody(tc.Body, tc.Partial)

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

			want := tc.Value
			mine := asWritten(got)
			if len(mine) != len(want) {
				t.Fatalf("wrote %s\n        live %s", mustJSON(mine), mustJSON(want))
			}
			for k, wv := range want {
				mv, ok := mine[k]
				if !ok {
					t.Errorf("%s is ABSENT here and present in the live value (%v) -- "+
						"absent and null write different rows", k, wv)
					continue
				}
				if !sameJSON(mv, wv) {
					t.Errorf("%s = %v, live %v", k, mv, wv)
				}
			}
		})
	}
}

// TestAnAbsentPlaceIsNotAClearedOne.
//
// Stated on its own because it is the failure with real consequences: an edit
// that only renames a site must not blank its map pin, and Go's `m["place"]` is
// nil for a missing key exactly as it is for a JSON null.
func TestAnAbsentPlaceIsNotAClearedOne(t *testing.T) {
	absent, err := ParseSiteBody(map[string]any{"name": "Depot"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if absent.HasPlace {
		t.Error("a rename claimed to write the location columns -- the site's map pin " +
			"would be blanked by an edit that never mentioned it")
	}

	cleared, err := ParseSiteBody(map[string]any{"name": "Depot", "place": nil}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.HasPlace || cleared.Place != nil {
		t.Errorf("an explicit null did not clear the location (HasPlace=%v Place=%v)",
			cleared.HasPlace, cleared.Place)
	}
}

// TestAnAbsentDescriptionIsNotAClearedOne. Same trap, cheaper consequence.
func TestAnAbsentDescriptionIsNotAClearedOne(t *testing.T) {
	absent, _ := ParseSiteBody(map[string]any{"name": "Depot"}, true)
	if absent.HasDescription {
		t.Error("a rename would clear the description")
	}
	cleared, _ := ParseSiteBody(map[string]any{"name": "D", "description": nil}, true)
	if !cleared.HasDescription || cleared.Description != nil {
		t.Error("an explicit null did not clear the description")
	}
	// ...and an EMPTY STRING clears it too: the live `d || null` collapses them.
	empty, _ := ParseSiteBody(map[string]any{"name": "D", "description": "  "}, true)
	if !empty.HasDescription || empty.Description != nil {
		t.Error("an empty description did not write NULL")
	}
}

// TestTheFiveLocationColumnsMoveTogether.
//
// A location is a PICKED PLACE, not typed coordinates (#96). If any subset could
// be written on its own, a site could sit on the map at coordinates no gazetteer
// ever produced -- or carry a place name with no pin.
func TestTheFiveLocationColumnsMoveTogether(t *testing.T) {
	// Top-level coordinates are ignored outright.
	p, err := ParseSiteBody(map[string]any{"name": "D", "lat": 51.5, "lon": -0.1}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.HasPlace {
		t.Error("top-level lat/lon reached the location columns")
	}

	// A place carrying coordinates but no name is refused, not half-written.
	if _, err := ParseSiteBody(
		map[string]any{"name": "D", "place": map[string]any{"lat": 1.0, "lon": 2.0}},
		false); err == nil {
		t.Error("coordinates with no place name were accepted")
	}
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

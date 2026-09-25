package geoplace

// The differential gate: every case in testdata/geoplace-cases.json was produced
// by RUNNING the live src/geoPlace.js, so a disagreement here is a port defect
// rather than a difference of opinion about the comments.
//
// The comparison is on MARSHALLED JSON, not on Go values, because the key set is
// part of the contract — the auto tier carries `accuracyKm` and `wanIp` and the
// other two tiers do not, and a struct comparison would not notice a tier that
// started emitting them.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type placeCases struct {
	NameMax int   `json:"nameMax"`
	Now     int64 `json:"now"`
	Cases   struct {
		NormalizePlace []struct {
			Note  string          `json:"note"`
			Input any             `json:"input"`
			Want  json.RawMessage `json:"want"`
		} `json:"normalizePlace"`
		FormatPlace []struct {
			Note  string `json:"note"`
			Input any    `json:"input"`
			Want  string `json:"want"`
		} `json:"formatPlace"`
		ResolveLocation []struct {
			Note   string          `json:"note"`
			Router map[string]any  `json:"router"`
			Site   map[string]any  `json:"site"`
			Want   json.RawMessage `json:"want"`
		} `json:"resolveLocation"`
		AutoGeoAction []struct {
			Note   string          `json:"note"`
			WanIP  any             `json:"wanIp"`
			Lookup map[string]any  `json:"lookup"`
			Want   json.RawMessage `json:"want"`
		} `json:"autoGeoAction"`
	} `json:"cases"`
}

func load(t *testing.T) placeCases {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "geoplace-cases.json"))
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}
	var f placeCases
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parsing the corpus: %v", err)
	}
	return f
}

// same compares against the corpus by re-marshalling both sides, so key
// presence and null-versus-absent are both in scope.
func same(t *testing.T, note string, got any, want json.RawMessage) {
	t.Helper()
	gb, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("%s: marshalling: %v", note, err)
	}
	var g, w any
	if err := json.Unmarshal(gb, &g); err != nil {
		t.Fatalf("%s: %v", note, err)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("%s: %v", note, err)
	}
	gn, _ := json.Marshal(g)
	wn, _ := json.Marshal(w)
	if string(gn) != string(wn) {
		t.Errorf("%s\n  got  %s\n  want %s", note, gn, wn)
	}
}

func TestNameMaxMatchesTheLiveConstant(t *testing.T) {
	if f := load(t); f.NameMax != NameMax {
		t.Errorf("NameMax = %d, the live module says %d", NameMax, f.NameMax)
	}
}

func TestNormalizePlaceAgainstTheLiveModule(t *testing.T) {
	f := load(t)
	if len(f.Cases.NormalizePlace) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}
	for _, c := range f.Cases.NormalizePlace {
		same(t, c.Note, NormalizePlace(c.Input), c.Want)
	}
	t.Logf("%d normalizePlace cases", len(f.Cases.NormalizePlace))
}

func TestFormatPlaceAgainstTheLiveModule(t *testing.T) {
	f := load(t)
	if len(f.Cases.FormatPlace) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}
	for _, c := range f.Cases.FormatPlace {
		// The corpus feeds formatPlace raw input, exactly as the live module is
		// called: it is NOT re-normalised first, so a numeric region reaches it.
		var p *Place
		if m, ok := c.Input.(map[string]any); ok {
			p = &Place{
				Name: strOf(m, "name"), Region: strOf(m, "region"), CC: strOf(m, "cc"),
			}
		}
		if got := FormatPlace(p); got != c.Want {
			t.Errorf("%s: got %q, want %q", c.Note, got, c.Want)
		}
	}
}

func strOf(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func TestResolveLocationAgainstTheLiveModule(t *testing.T) {
	f := load(t)
	if len(f.Cases.ResolveLocation) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}
	for _, c := range f.Cases.ResolveLocation {
		var geo map[string]any
		if c.Router != nil {
			geo, _ = c.Router["geo"].(map[string]any)
		}
		var site *SiteRow
		if c.Site != nil {
			site = &SiteRow{
				Name:        strOf(c.Site, "name"),
				Lat:         c.Site["lat"],
				Lon:         c.Site["lon"],
				PlaceName:   strOf(c.Site, "place_name"),
				PlaceRegion: strOf(c.Site, "place_region"),
				PlaceCC:     strOf(c.Site, "place_cc"),
			}
		}
		got := ResolveLocation(geo, site)
		if got == nil {
			same(t, c.Note, nil, c.Want)
			continue
		}
		same(t, c.Note, got, c.Want)
	}
	t.Logf("%d resolveLocation cases", len(f.Cases.ResolveLocation))
}

func TestAutoGeoActionAgainstTheLiveModule(t *testing.T) {
	f := load(t)
	if len(f.Cases.AutoGeoAction) == 0 {
		t.Fatal("no cases; the corpus is not being read")
	}
	for _, c := range f.Cases.AutoGeoAction {
		ip, _ := c.WanIP.(string)
		var g *Lookup
		if c.Lookup != nil {
			g = &Lookup{
				City: strOf(c.Lookup, "city"), Region: strOf(c.Lookup, "region"),
				Country: strOf(c.Lookup, "country"), Area: c.Lookup["area"],
			}
			if ll, ok := c.Lookup["ll"].([]any); ok {
				g.LL = ll
			}
		}
		d := AutoGeoAction(ip, g, f.Now)

		// The live shape is `{action}` or `{action, auto:{…}}` — no `auto` key at
		// all for keep and clear, which is why this is built by hand rather than
		// tagged onto the struct.
		out := map[string]any{"action": d.Action}
		if d.Auto != nil {
			out["auto"] = d.Auto
		}
		same(t, c.Note, out, c.Want)
	}
	t.Logf("%d autoGeoAction cases", len(f.Cases.AutoGeoAction))
}

// ── the invariants, stated separately from the corpus ────────────────────────

// TestAMissingCoordinateIsNotTheEquator is the one an operator would see: a
// router placed at 0,0 sits in the Gulf of Guinea, which looks like a real fix.
func TestAMissingCoordinateIsNotTheEquator(t *testing.T) {
	for _, absent := range []any{nil, "", map[string]any{}["nope"]} {
		if p := NormalizePlace(map[string]any{
			"name": "X", "cc": "DE", "lat": absent, "lon": 13.4,
		}); p != nil {
			t.Errorf("an absent latitude (%#v) was accepted as %v", absent, p.Lat)
		}
	}
	// AND A REAL ZERO STILL PASSES, which is what stops the guard being a blunt
	// "reject falsy" that loses Null Island and the prime meridian.
	if p := NormalizePlace(map[string]any{
		"name": "X", "cc": "DE", "lat": 0.0, "lon": 0.0,
	}); p == nil {
		t.Error("a genuine 0,0 was rejected")
	}
}

// TestTheAutoTierAloneCarriesAccuracyAndWanIp — the key set is the contract.
func TestTheAutoTierAloneCarriesAccuracyAndWanIp(t *testing.T) {
	auto := map[string]any{
		"name": "Hamburg", "region": "HH", "cc": "DE", "lat": 53.55, "lon": 10.0,
		"ip": "198.51.100.7", "accuracyKm": 5.0,
	}
	manual := map[string]any{"name": "Berlin", "region": "BE", "cc": "DE", "lat": 52.52, "lon": 13.4}

	for _, tc := range []struct {
		name string
		geo  map[string]any
		want bool // should the extra keys be present?
	}{
		{"auto", map[string]any{"auto": auto}, true},
		{"manual", map[string]any{"place": manual}, false},
	} {
		b, err := json.Marshal(ResolveLocation(tc.geo, nil))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		_, hasAcc := m["accuracyKm"]
		_, hasIP := m["wanIp"]
		if hasAcc != tc.want || hasIP != tc.want {
			t.Errorf("%s tier: accuracyKm=%v wanIp=%v, want both %v (%s)",
				tc.name, hasAcc, hasIP, tc.want, b)
		}
	}
}

// TestAnOfflineRouterKeepsItsLastKnownPosition is the failure the live module
// records as having taken a browser to notice: folding "no address" into "cannot
// place it" empties the map of every offline router.
func TestAnOfflineRouterKeepsItsLastKnownPosition(t *testing.T) {
	if d := AutoGeoAction("", &Lookup{LL: []any{53.55, 10.0}}, 1); d.Action != ActionKeep {
		t.Errorf("no WAN address gave %q, want keep — every offline router would "+
			"lose its position from the map", d.Action)
	}
	if d := AutoGeoAction("198.51.100.7", nil, 1); d.Action != ActionClear {
		t.Errorf("an unplaceable address gave %q, want clear — a fix from the "+
			"previous address is now a lie", d.Action)
	}
}

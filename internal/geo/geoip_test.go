package geo

// The geo differential gate.
//
// tools/geo-cases.js asks geoip-lite for 164 addresses and records what it
// said; this reads the SAME data files and must agree on every one. Where a
// fixture proves a collector's transform, this proves a binary format reader —
// and the failure modes are the kind that produce a plausible wrong answer
// rather than an error, which is why it is a gate and not a smoke test.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

type geoCase struct {
	IP    string `json:"ip"`
	Found bool   `json:"found"`
	// Country can be EMPTY on a record that was found: some ranges resolve to a
	// location with a timezone and coordinates but no country code. That is why
	// `found` is recorded separately — see the generator's comment.
	Country string `json:"country"`
	City    string `json:"city"`
	Region  string `json:"region"`
	// LL is geoip-lite's `[lat, lon]`, kept VERBATIM including its nulls. A
	// range can be a hit with no location record, and the pair is then
	// `[null, null]` — which is not `[0, 0]`, a real place in the Gulf of
	// Guinea. Pointers on both sides preserve the distinction; two float64s
	// would quietly erase it and put those routers in the sea off west Africa.
	LL   []*float64 `json:"ll"`
	Area *uint32    `json:"area"`
}

// dataDir is where geoip-lite keeps its files, inside the live repo's
// node_modules. Read-only, and skipped rather than failed when absent: this is
// the one gate in the port that depends on a 76 MB file nobody committed.
func dataDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("MIKRODASH_SRC")
	if root == "" {
		root = filepath.Join("..", "..", "..", "MikroDash")
	}
	dir := filepath.Join(root, "node_modules", "geoip-lite", "data")
	if _, err := os.Stat(filepath.Join(dir, "geoip-city.dat")); err != nil {
		t.Skipf("geoip-lite data not present at %s — set MIKRODASH_SRC to run the geo gate", dir)
	}
	return dir
}

func TestLookupMatchesGeoipLite(t *testing.T) {
	dir := dataDir(t)
	db, err := Load(dir)
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}

	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "geo-cases.json"))
	if err != nil {
		t.Fatalf("no cases — run: node tools/geo-cases.js: %v", err)
	}
	var cases struct {
		Total    int       `json:"total"`
		Answered int       `json:"answered"`
		Found    int       `json:"found"`
		Cases    []geoCase `json:"cases"`
	}
	if err := json.Unmarshal(body, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.Cases) < 100 {
		t.Fatalf("only %d cases — the generator produced too few to mean anything", len(cases.Cases))
	}

	located, coords := 0, 0
	for _, c := range cases.Cases {
		got, ok := db.Lookup(c.IP)
		if ok != c.Found {
			t.Errorf("%s: found=%v, geoip-lite says %v", c.IP, ok, c.Found)
			continue
		}
		if !ok {
			continue
		}
		located++
		if got.Country != c.Country {
			t.Errorf("%s: country %q, geoip-lite says %q", c.IP, got.Country, c.Country)
		}
		if got.City != c.City {
			t.Errorf("%s: city %q, geoip-lite says %q", c.IP, got.City, c.City)
		}
		if got.Region != c.Region {
			t.Errorf("%s: region %q, geoip-lite says %q", c.IP, got.Region, c.Region)
		}

		// ── THE COORDINATES, INCLUDING THEIR ABSENCE ───────────────────────
		//
		// `wantLat` is nil for a range that was found but carries no location
		// record. Comparing only the numbers would let a reader that returned
		// 0,0 for those pass every case in this corpus, and 0,0 renders as a
		// confident fix in the Atlantic rather than as "unknown".
		var wantLat, wantLon *float64
		if len(c.LL) == 2 {
			wantLat, wantLon = c.LL[0], c.LL[1]
		}
		if !sameCoord(got.Lat, wantLat) || !sameCoord(got.Lon, wantLon) {
			t.Errorf("%s: ll %s,%s — geoip-lite says %s,%s",
				c.IP, showCoord(got.Lat), showCoord(got.Lon),
				showCoord(wantLat), showCoord(wantLon))
		}
		if got.Lat != nil {
			coords++
		}

		// `area` is absent in the corpus exactly when there is no location
		// record; the reader reports 0 there, which is what every consumer's
		// `Number(g.area) || 0` already collapses absent and zero to.
		var wantArea uint32
		if c.Area != nil {
			wantArea = *c.Area
		}
		if got.Area != wantArea {
			t.Errorf("%s: area %d, geoip-lite says %d", c.IP, got.Area, wantArea)
		}
	}

	// A FLOOR ON THE LOCATED COUNT TOO. Without it a reader that returned nil
	// coordinates for everything would agree with every no-location case and be
	// silent about the thousands that do carry a fix.
	if coords < 50 {
		t.Errorf("only %d addresses carried coordinates — the reader is agreeing "+
			"by reporting every fix as unknown", coords)
	}

	// A floor rather than an exact count: geoip-lite refreshes its data and the
	// answers move, so asserting a number would make this gate fail on a data
	// update rather than on a code change. What is asserted is AGREEMENT.
	if located < 50 {
		t.Errorf("only %d addresses resolved — the reader is agreeing by refusing everything", located)
	}
}

// The private ranges are refused BEFORE the search, and that is not an
// optimisation: these addresses fall inside the index, so a reader that searched
// anyway would return whatever public range surrounds them — a LAN host placed
// confidently in another country.
func TestPrivateRangesAreRefused(t *testing.T) {
	db, err := Load(dataDir(t))
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}
	for _, ip := range []string{
		"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255",
		"192.168.0.1", "192.168.255.255",
	} {
		if _, ok := db.Lookup(ip); ok {
			t.Errorf("%s was located — a private address must never resolve", ip)
		}
	}
	// And an address just OUTSIDE a private range must still work, so the
	// refusal is a range test rather than a prefix guess.
	if _, ok := db.Lookup("172.32.0.1"); !ok {
		t.Log("172.32.0.1 did not resolve; not a failure, but the boundary is untested here")
	}
}

// A nil database answers rather than panicking: "no geo" is a state this app
// runs in, on any deployment without the data files.
func TestNilDBIsSafe(t *testing.T) {
	var db *DB
	if _, ok := db.Lookup("1.1.1.1"); ok {
		t.Error("a nil database claimed to locate an address")
	}
}

// sameCoord compares two possibly-absent coordinates. Absence is equal only to
// absence: that is the distinction the whole pointer type exists for.
//
// The values are compared EXACTLY rather than within a tolerance. Both sides
// come from the same int32 divided by the same 10000, so any difference is a
// decoding difference and not a rounding one — a tolerance here would hide
// exactly the bug this gate is for.
func sameCoord(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func showCoord(v *float64) string {
	if v == nil {
		return "<absent>"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

package geoplace_test

// Do the two halves actually fit?
//
// `AutoGeoAction` takes its lookup as a parameter, which keeps it pure and
// testable — but it also means nothing had checked that what `internal/geo`
// RETURNS is what `AutoGeoAction` can READ. That gap is where a port loses a
// day: both sides pass their own tests, and the seam between them is discovered
// at the call site much later.
//
// So this drives the real reader into the real decision. It lives in
// `geoplace_test` (the external test package) and imports `internal/geo` in TEST
// SCOPE ONLY, so the pure package keeps no production dependency on the reader —
// which mirrors the live app, where index.js calls `geo.lookup(ip)` and hands
// the result to `GeoPlace.autoGeoAction`.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/geo"
	"mikrodash/internal/geoplace"
)

const bridgeNow = int64(1773567000000)

// lookupFrom is the adapter the eventual caller will need: geoip-lite's record
// as AutoGeoAction reads it. Written here first, on purpose — if the two shapes
// did not line up this is where it would show, rather than in a page handler.
func lookupFrom(l geo.Location) *geoplace.Lookup {
	g := &geoplace.Lookup{
		City: l.City, Region: l.Region, Country: l.Country, Area: float64(l.Area),
	}
	// THE NILS ARE CARRIED THROUGH, not flattened. A range can be a hit with no
	// location record, and `AutoGeoAction` must see that as "cannot place it"
	// rather than as a fix at 0,0.
	if l.Lat != nil && l.Lon != nil {
		g.LL = []any{*l.Lat, *l.Lon}
	}
	return g
}

func dataDir(t *testing.T) string {
	t.Helper()
	root := os.Getenv("MIKRODASH_SRC")
	if root == "" {
		root = filepath.Join("..", "..", "..", "MikroDash")
	}
	dir := filepath.Join(root, "node_modules", "geoip-lite", "data")
	if _, err := os.Stat(filepath.Join(dir, "geoip-city.dat")); err != nil {
		t.Skipf("geoip-lite data not present at %s — set MIKRODASH_SRC to run this gate", dir)
	}
	return dir
}

// TestARealLookupProducesAUsableFix walks an address the database can place all
// the way to a `set` decision, and checks the fix carries the coordinates the
// reader found rather than zeros.
func TestARealLookupProducesAUsableFix(t *testing.T) {
	db, err := geo.Load(dataDir(t))
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}

	// A public resolver, chosen because it is stable and not from any capture.
	// The ASSERTIONS do not name a city or country: geoip-lite refreshes its
	// data, and a gate that pinned "Mountain View" would fail on a data update
	// rather than on a code change. What is asserted is that a placeable address
	// produces a placeable fix.
	const addr = "8.8.8.8"
	loc, ok := db.Lookup(addr)
	if !ok {
		t.Skipf("%s is not in this copy of the database", addr)
	}
	if loc.Lat == nil || loc.Lon == nil {
		t.Skipf("%s resolved to a range with no location record", addr)
	}

	d := geoplace.AutoGeoAction(addr, lookupFrom(loc), bridgeNow)
	if d.Action != geoplace.ActionSet {
		t.Fatalf("action = %q, want set — a placeable address produced no fix", d.Action)
	}
	if d.Auto["lat"] != *loc.Lat || d.Auto["lon"] != *loc.Lon {
		t.Errorf("fix at %v,%v but the reader found %v,%v",
			d.Auto["lat"], d.Auto["lon"], *loc.Lat, *loc.Lon)
	}
	if d.Auto["ip"] != addr {
		t.Errorf("the fix records ip %v, want %q", d.Auto["ip"], addr)
	}
	if d.Auto["ts"] != bridgeNow {
		t.Errorf("the fix records ts %v, want %d", d.Auto["ts"], bridgeNow)
	}

	// AND IT MUST SURVIVE THE ROUND TRIP. The fix is stored and later read back
	// by ResolveLocation, so a decision that cannot be resolved is a router that
	// vanishes from the map one restart after it was placed.
	got := geoplace.ResolveLocation(map[string]any{"auto": d.Auto}, nil)
	if got == nil {
		t.Fatal("the fix this very lookup produced does not resolve to a location")
	}
	if got.Source != geoplace.SourceAuto {
		t.Errorf("source = %q, want auto", got.Source)
	}
	if got.Lat != *loc.Lat || got.Lon != *loc.Lon {
		t.Errorf("round trip moved the router: %v,%v -> %v,%v",
			*loc.Lat, *loc.Lon, got.Lat, got.Lon)
	}
	if got.WanIP != addr {
		t.Errorf("round trip lost the WAN address: %q", got.WanIP)
	}
}

// TestAPrivateAddressClearsRatherThanPlaces — the reader refuses RFC1918 before
// it searches, so the lookup misses, and a MISS with an address in hand means
// the router has moved somewhere unresolvable. Clearing is right; keeping would
// leave a fix from the previous address standing as a lie.
func TestAPrivateAddressClearsRatherThanPlaces(t *testing.T) {
	db, err := geo.Load(dataDir(t))
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}
	for _, addr := range []string{"192.168.1.1", "10.0.0.2", "172.16.5.4"} {
		loc, ok := db.Lookup(addr)
		if ok {
			t.Errorf("%s was placed; private ranges are refused before the search", addr)
			continue
		}
		// A miss gives the zero Location, whose Lat is nil — so the adapter
		// produces a lookup with no LL and the decision is `clear`.
		if d := geoplace.AutoGeoAction(addr, lookupFrom(loc), bridgeNow); d.Action != geoplace.ActionClear {
			t.Errorf("%s: action = %q, want clear", addr, d.Action)
		}
	}
}

// TestAHitWithNoLocationDoesNotBecomeTheGulfOfGuinea is the seam this whole
// file exists for. Such a range IS in the index — the reader reports found —
// and its coordinates are absent rather than zero. If either side flattened
// that, the router would be placed confidently in the Atlantic instead of left
// unplaced.
//
// ── IT DRIVES OFF THE CORPUS, AND THAT IS THE POINT ────────────────────────
//
// The first version of this test picked four addresses by hand and SKIPPED when
// none of them was found-but-unlocated. Mutating the reader to return 0,0 for
// exactly that case made the condition disappear, so the test skipped and
// `go test` printed ok — the gate went silent precisely when the bug it names
// was present. Measured, not theorised.
//
// `testdata/geo-cases.json` records which addresses geoip-lite answers with
// `ll: [null, null]`, so the corpus decides what to look for and the READER is
// what is on trial. A skip is now honest: it means the corpus has no such case,
// not that the reader stopped producing one.
func TestAHitWithNoLocationDoesNotBecomeTheGulfOfGuinea(t *testing.T) {
	dir := dataDir(t)
	db, err := geo.Load(dir)
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}

	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "geo-cases.json"))
	if err != nil {
		t.Fatalf("no cases — run: node tools/geo-cases.js: %v", err)
	}
	var corpus struct {
		Cases []struct {
			IP    string     `json:"ip"`
			Found bool       `json:"found"`
			LL    []*float64 `json:"ll"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(body, &corpus); err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, c := range corpus.Cases {
		// The condition, as geoip-lite itself reports it: in the index, no
		// coordinates.
		if !c.Found || (len(c.LL) == 2 && c.LL[0] != nil) {
			continue
		}
		l, ok := db.Lookup(c.IP)
		if !ok {
			t.Errorf("%s: the reader missed a range geoip-lite found", c.IP)
			continue
		}
		if l.Lat != nil || l.Lon != nil {
			t.Errorf("%s: the reader reported %v,%v for a range with NO location "+
				"record — that places the router in the Gulf of Guinea rather than "+
				"leaving it unplaced", c.IP, *l.Lat, *l.Lon)
			continue
		}
		if d := geoplace.AutoGeoAction(c.IP, lookupFrom(l), bridgeNow); d.Action != geoplace.ActionClear {
			t.Errorf("%s: action = %q, want clear", c.IP, d.Action)
		}
		checked++
		if checked >= 50 {
			break // enough to prove the rule; the corpus holds hundreds
		}
	}

	if checked == 0 {
		t.Skip("the corpus holds no found-but-unlocated range — nothing to check")
	}
	t.Logf("%d found-but-unlocated ranges stayed unplaced", checked)
}

package geo

import (
	"os"
	"path/filepath"
	"testing"
)

// THE MMDB BACKEND, AGAINST A REAL DB-IP FILE.
//
// ── WHY THE ASSERTIONS ARE MOSTLY STRUCTURAL ──────────────────────────────
//
// DB-IP City Lite is republished monthly, and that is the entire reason it was
// chosen. A test pinning "8.8.8.8 is in Mountain View" would therefore be a test
// that breaks on a data refresh for no defect — the exact shape of a gate people
// learn to ignore and then delete.
//
// So this asserts what must ALWAYS be true of a City database, whatever month it
// is: private space is unplaced, public space is placed, a country code is two
// letters, v6 resolves, and the string-parsing rules match the ones this package
// already committed to. The one value-level assertion is the country of a
// well-known anycast address, because a database that stopped placing 8.8.8.8 in
// the US would be telling us something worth hearing.
//
// SKIPS WITHOUT THE FILE, following `localcc_api_test.go`'s convention. A skip is
// reported as a skip; it is not a pass.
func geoDirOrSkip(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("MIKRODASH_GEO_DIR")
	if dir == "" {
		t.Skip("MIKRODASH_GEO_DIR is not set; the mmdb is downloaded at image build")
	}
	if _, err := os.Stat(filepath.Join(dir, mmdbName)); err != nil {
		t.Skipf("no %s in MIKRODASH_GEO_DIR=%s", mmdbName, dir)
	}
	return dir
}

func TestTheMMDBBackendIsPreferredAndAnswers(t *testing.T) {
	dir := geoDirOrSkip(t)
	db, err := Load(dir)
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	if db.mmdb == nil {
		t.Fatal("a directory holding an .mmdb loaded the LEGACY reader. Load must " +
			"prefer the mmdb, or an install that downloaded fresh data would go on " +
			"answering from the stale .dat files beside it")
	}

	t.Run("public addresses are placed", func(t *testing.T) {
		for _, ip := range []string{"8.8.8.8", "1.1.1.1", "140.82.121.3", "2606:4700::1111"} {
			loc, ok := db.Lookup(ip)
			if !ok {
				t.Errorf("%s was not placed; a City database should place a "+
					"well-known public address", ip)
				continue
			}
			if len(loc.Country) != 2 {
				t.Errorf("%s gave country %q, want a 2-letter ISO code", ip, loc.Country)
			}
			if loc.City == "" {
				t.Errorf("%s gave no city. A Country database opens cleanly and "+
					"answers exactly like this, which is why openMMDB checks the type", ip)
			}
		}
	})

	t.Run("a well-known anycast address keeps its country", func(t *testing.T) {
		loc, ok := db.Lookup("8.8.8.8")
		if !ok || loc.Country != "US" {
			t.Errorf("8.8.8.8 -> %+v (ok=%v), want country US", loc, ok)
		}
	})

	t.Run("unroutable space is unplaced, not an error", func(t *testing.T) {
		// The majority of what a LAN dashboard asks about. These must return
		// false rather than a zero Location that renders as a blank flag.
		for _, ip := range []string{"192.168.1.1", "10.0.0.1", "127.0.0.1", "::1", "fe80::1"} {
			if loc, ok := db.Lookup(ip); ok {
				t.Errorf("%s was placed as %+v; private and reserved space has no location", ip, loc)
			}
		}
	})

	t.Run("the string rules match the legacy reader", func(t *testing.T) {
		// NO TrimSpace: geoip-lite returns null for a padded address, and this
		// package committed to answering where the live app answers. Swapping the
		// backend must not quietly widen what counts as an address.
		if _, ok := db.Lookup(" 8.8.8.8 "); ok {
			t.Error("a padded address was accepted; net.isIP(' 8.8.8.8 ') is 0, so " +
				"the legacy reader refuses it and so must this one")
		}
		if _, ok := db.Lookup("not-an-ip"); ok {
			t.Error("a non-address was accepted")
		}
		// A ZONE IS IGNORED, NOT REFUSED — the legacy reader's documented rule.
		if _, ok := db.Lookup("2606:4700::1111%eth0"); !ok {
			t.Error("a zone-suffixed v6 address was refused; the legacy reader " +
				"ignores the zone and places the address")
		}
	})
}

// THE DATABASE NAMES ITSELF, which is what openMMDB's type check depends on.
//
// A Country database opens cleanly and then answers "" for every city and no
// coordinates — indistinguishable from correct behaviour on a fleet that talks
// mostly to unplaced space. Load is the only point where that difference is
// visible, and it can only see it if the metadata carries a type at all.
func TestTheDatabaseReportsItsType(t *testing.T) {
	dir := geoDirOrSkip(t)
	db, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := db.mmdb.Metadata.DatabaseType
	if got == "" {
		t.Fatal("the database reports no type, so openMMDB's check cannot work")
	}
	t.Logf("database type: %s (build epoch %d)", got, db.mmdb.Metadata.BuildEpoch)
}

// THE GENERATED GAZETTEER FEEDS THE PICKER, AND RANKS LIKE THE OLD ONE.
//
// `cmd/geogen` counts networks per city into `w`, because that is the quantity
// `buildFromDat` counts into `weight` and ranks on. If that had been dropped the
// picker would still work and would silently get worse — "london" offering
// Londonderry first is not a failure any test would notice unless it asks.
func TestTheGeneratedGazetteerRanksByProminence(t *testing.T) {
	dir := os.Getenv("MIKRODASH_GEO_DIR")
	if dir == "" {
		t.Skip("MIKRODASH_GEO_DIR is not set")
	}
	if _, err := os.Stat(filepath.Join(dir, gazetteerName)); err != nil {
		t.Skipf("no %s in MIKRODASH_GEO_DIR=%s", gazetteerName, dir)
	}
	idx, err := BuildCityIndex(dir)
	if err != nil {
		t.Fatalf("BuildCityIndex: %v", err)
	}
	if n := len(idx.places); n < minRows {
		t.Fatalf("only %d places; the floor is %d", n, minRows)
	}
	t.Logf("gazetteer: %d places", len(idx.places))

	for _, c := range []struct{ query, wantFirst, wantCC string }{
		{"london", "London", "GB"},
		{"tokyo", "Tokyo", "JP"},
		{"sydney", "Sydney", "AU"},
	} {
		hits := idx.Search(c.query, "5")
		if len(hits) == 0 {
			t.Errorf("%q returned nothing", c.query)
			continue
		}
		if hits[0].Name != c.wantFirst || hits[0].CC != c.wantCC {
			t.Errorf("%q -> %s (%s) first, want %s (%s). The weight is how the "+
				"index breaks a tie between same-named places; a gazetteer built "+
				"without it ranks alphabetically and looks subtly wrong.",
				c.query, hits[0].Name, hits[0].CC, c.wantFirst, c.wantCC)
		}
		if hits[0].Lat == 0 && hits[0].Lon == 0 {
			t.Errorf("%q gave no coordinates; the map places a router by them", c.query)
		}
	}

	// One letter is never a real intent — the legacy rule, preserved.
	if got := idx.Search("l", "5"); len(got) != 0 {
		t.Errorf("a one-letter query returned %d hits, want 0", len(got))
	}
}

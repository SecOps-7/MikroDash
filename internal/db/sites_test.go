package db

// Sites and the open-alert counts: the two reads the Routers page needs.
//
// The DDL is the LIVE schema, copied — `sites.name` is `TEXT NOT NULL UNIQUE
// COLLATE NOCASE` and the coordinates are nullable REAL. Both matter to what is
// asserted below, so a tidied-up test schema would be testing something else.

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"
)

const sitesDDL = `
CREATE TABLE sites (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT,
  lat         REAL,
  lon         REAL,
  place_name   TEXT,
  place_region TEXT,
  place_cc     TEXT,
  created_at  INTEGER NOT NULL
);
CREATE TABLE alert_events (
  id              INTEGER PRIMARY KEY,
  router_id       TEXT    NOT NULL,
  alert_type      TEXT    NOT NULL,
  subject         TEXT,
  detail          TEXT,
  fired_at        INTEGER NOT NULL,
  resolved_at     INTEGER,
  acknowledged_at INTEGER,
  acknowledged_by TEXT
);
`

func sitesDB(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	if _, err := h.Exec(sitesDDL); err != nil {
		t.Fatal(err)
	}
	return openTest(t, dir), h
}

// TestAnUnsetCoordinateStaysUnset is the one that matters. The live schema's own
// comment gives the reason: "an unset location must not read as coordinates 0,0
// in the Gulf of Guinea."
func TestAnUnsetCoordinateStaysUnset(t *testing.T) {
	d, h := sitesDB(t)
	if _, err := h.Exec(`INSERT INTO sites (id, name, lat, lon, created_at)
	                     VALUES ('s1', 'Never located', NULL, NULL, 1)`); err != nil {
		t.Fatal(err)
	}
	// A site AT the equator and prime meridian, to prove the guard is not a
	// blunt "reject zero".
	if _, err := h.Exec(`INSERT INTO sites (id, name, lat, lon, created_at)
	                     VALUES ('s2', 'Null Island', 0, 0, 2)`); err != nil {
		t.Fatal(err)
	}

	got, err := d.ListSites()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d sites, want 2", len(got))
	}
	byID := map[string]Site{}
	for _, s := range got {
		byID[s.ID] = s
	}
	// The message must not DEREFERENCE what it just found might be nil: a first
	// version did, and the very mutation this test exists to catch (dropping the
	// Valid check on lat only) made one pointer non-nil and the other nil, so the
	// test PANICKED instead of reporting. A panicking test still fails, but it
	// fails without saying why, and the reason it exists is the message.
	if s := byID["s1"]; s.Lat != nil || s.Lon != nil {
		t.Errorf("an unlocated site came back at %s,%s — that is the Gulf of Guinea",
			showF(s.Lat), showF(s.Lon))
	}
	if s := byID["s2"]; s.Lat == nil || s.Lon == nil {
		t.Fatal("a site genuinely at 0,0 came back as unlocated")
	} else if *s.Lat != 0 || *s.Lon != 0 {
		t.Errorf("Null Island moved to %v,%v", *s.Lat, *s.Lon)
	}
}

// TestCoordProducesATrueNil pins the Go trap Coord exists for: a nil *float64
// assigned into an `any` is NOT a nil interface, and the consumer's `case nil:`
// would not match it.
func TestCoordProducesATrueNil(t *testing.T) {
	var absent *float64
	if v := Coord(absent); v != nil {
		t.Errorf("Coord(nil) produced a non-nil any (%#v) — a typed nil pointer "+
			"in an interface does not match `case nil`, so the coordinate would "+
			"be rejected rather than read as absent", v)
	}
	// And the naive assignment, stated so the difference is visible rather than
	// folklore: this is what Coord is preventing.
	var naive any = absent
	if naive == nil {
		t.Fatal("precondition failed: a typed nil pointer in an interface should " +
			"NOT compare equal to nil — if it does, Coord is unnecessary")
	}

	v := 52.52
	if got := Coord(&v); got != any(52.52) {
		t.Errorf("Coord(&52.52) = %#v", got)
	}
}

// TestSitesAreOrderedCaseInsensitively — the column is declared NOCASE, and an
// ORDER BY that disagreed with the collation would list them differently from
// the Node app for the same data.
func TestSitesAreOrderedCaseInsensitively(t *testing.T) {
	d, h := sitesDB(t)
	for i, name := range []string{"zurich", "Berlin", "amsterdam", "Copenhagen"} {
		if _, err := h.Exec(`INSERT INTO sites (id, name, created_at) VALUES (?,?,?)`,
			"s"+string(rune('1'+i)), name, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.ListSites()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	want := []string{"amsterdam", "Berlin", "Copenhagen", "zurich"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order = %v, want %v (a case-SENSITIVE sort puts every "+
				"capitalised name first)", names, want)
		}
	}
}

// TestNullTextColumnsReadAsEmpty — NULL and empty are the same thing to
// NormalizePlace, which trims and then tests, so nothing distinguishes them
// downstream and a pointer would be ceremony.
func TestNullTextColumnsReadAsEmpty(t *testing.T) {
	d, h := sitesDB(t)
	if _, err := h.Exec(`INSERT INTO sites (id, name, created_at) VALUES ('s1','HQ',7)`); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListSites()
	if err != nil {
		t.Fatal(err)
	}
	s := got[0]
	// NULL, not "". These four are `*string` since 2026-08-28 because the live
	// payload sends `null` for an unset column and this port was sending `""` —
	// found by diffing /api/sites between the two servers, which is the only
	// thing that could have found it.
	if s.Description != nil || s.PlaceName != nil || s.PlaceRegion != nil || s.PlaceCC != nil {
		t.Errorf("NULL text came back as %+v", s)
	}
	if s.CreatedAt != 7 {
		t.Errorf("created_at = %d, want 7", s.CreatedAt)
	}
}

// ── the alert counts ─────────────────────────────────────────────────────────

// TestOnlyUnresolvedAlertsAreCounted, and a router with nothing open is ABSENT
// rather than zero — faithful to the original, which builds its object only from
// returned rows. Absence still reads as 0 through a Go map, which is the point:
// the caller gets its zero without this pretending to have counted one.
func TestOnlyUnresolvedAlertsAreCounted(t *testing.T) {
	d, h := sitesDB(t)
	rows := []struct {
		router   string
		resolved any
	}{
		{"r-A", nil}, {"r-A", nil}, {"r-A", int64(500)}, // two open, one resolved
		{"r-B", nil},        // one open
		{"r-C", int64(900)}, // all resolved
	}
	for i, r := range rows {
		if _, err := h.Exec(`INSERT INTO alert_events
		    (router_id, alert_type, fired_at, resolved_at) VALUES (?,?,?,?)`,
			r.router, "cpu", int64(i), r.resolved); err != nil {
			t.Fatal(err)
		}
	}

	got, err := d.CountOpenAlertsByRouter()
	if err != nil {
		t.Fatal(err)
	}
	if got["r-A"] != 2 {
		t.Errorf("r-A = %d, want 2 (the resolved row must not be counted)", got["r-A"])
	}
	if got["r-B"] != 1 {
		t.Errorf("r-B = %d, want 1", got["r-B"])
	}
	if _, present := got["r-C"]; present {
		t.Errorf("r-C is present (%d); a router with nothing open must be ABSENT, "+
			"as the original leaves it", got["r-C"])
	}
	if got["r-never-seen"] != 0 {
		t.Error("a missing key did not read as zero")
	}
}

func TestCountingWithNoAlertsAtAll(t *testing.T) {
	d, _ := sitesDB(t)
	got, err := d.CountOpenAlertsByRouter()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want an empty map", got)
	}
}

// showF renders a possibly-absent coordinate without dereferencing it.
func showF(v *float64) string {
	if v == nil {
		return "<absent>"
	}
	return strconv.FormatFloat(*v, 'g', -1, 64)
}

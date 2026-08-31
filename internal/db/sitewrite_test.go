package db

// The site writes: create, partial update, delete.
//
// The VALIDATION that decides what reaches these lives in `internal/sites` and
// is pinned against the live `_parseSiteBody` there. What is pinned here is that
// a partial update stays partial, that a NULL column round-trips, and that the
// UNIQUE index — not a pre-check — is what refuses a duplicate name.

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// siteWriteDDL is the LIVE schema for this table: six of the nine columns are
// nullable, and a fixture that cannot produce a NULL cannot produce the row that
// breaks a scan. `internal/server`'s first sites DDL declared them NOT NULL and
// hid exactly that bug in `GetSite`.
const siteWriteDDL = `
CREATE TABLE sites (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT, lat REAL, lon REAL,
  place_name TEXT, place_region TEXT, place_cc TEXT,
  created_at INTEGER NOT NULL);
`

func siteDB(t *testing.T) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(siteWriteDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	return openTest(t, dir)
}

func TestCreateSiteRoundTrips(t *testing.T) {
	d := siteDB(t)

	got, err := d.CreateSite(map[string]any{
		"name": "Depot", "description": "main",
		"lat": 12.5, "lon": -3.25,
		"place_name": "Northtown", "place_region": "NR", "place_cc": "ZZ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Depot" || sstr(got.Description) != "main" || sstr(got.PlaceName) != "Northtown" {
		t.Errorf("stored %+v", got)
	}
	if got.Lat == nil || *got.Lat != 12.5 {
		t.Errorf("lat = %v", got.Lat)
	}
	// The id and the timestamp are MINTED HERE, never taken from the caller.
	if len(got.ID) != 36 || !strings.Contains(got.ID, "-") {
		t.Errorf("id = %q, want a v4 uuid", got.ID)
	}
	if got.CreatedAt == 0 {
		t.Error("created_at was not stamped")
	}
}

// TestACreateNeverTakesItsIdOrTimestampFromTheCaller.
//
// Both are minted inside `CreateSite`, as the live function mints them. It
// matters because the id is what every grant, audit row and membership list
// names: a caller choosing it could collide with an existing site, or reuse the
// id of one just deleted and silently inherit its grants. `ParseSiteBody` already
// drops unknown keys, so this is the second of two defences -- and the reason to
// have two is that the first is a different package with its own callers.
func TestACreateNeverTakesItsIdOrTimestampFromTheCaller(t *testing.T) {
	d := siteDB(t)

	first, err := d.CreateSite(map[string]any{"name": "Depot"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.CreateSite(map[string]any{
		"name": "Annexe", "id": "forged-id", "created_at": int64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "forged-id" {
		t.Error("the caller chose the site id")
	}
	if got.CreatedAt == 1 {
		t.Error("the caller chose created_at")
	}
	// Believability: the id really is fresh each time, so "not forged-id" is not
	// holding because the function returns a constant.
	if got.ID == first.ID {
		t.Error("two creates produced the same id")
	}
	// And the forged id names nothing.
	if s, _ := d.GetSite("forged-id"); s != nil {
		t.Errorf("a site was stored under the caller's id: %+v", s)
	}
}

// TestASiteWithNoLocationHasNoCoordinates.
//
// THE GULF OF GUINEA GUARD, at the storage layer. An unset location must read
// back as nil, not as 0,0 — two float64s would place every site that has never
// been located in the Atlantic and the map would draw it confidently.
func TestASiteWithNoLocationHasNoCoordinates(t *testing.T) {
	d := siteDB(t)

	got, err := d.CreateSite(map[string]any{"name": "Bare"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Lat != nil || got.Lon != nil {
		t.Errorf("an unset location read back as %v,%v", got.Lat, got.Lon)
	}
	// NIL, not "". These are `*string` since 2026-08-28: the wire distinguishes
	// a NULL column from an empty one, and this port was collapsing both to "".
	if got.Description != nil || got.PlaceName != nil {
		t.Errorf("NULL text columns read back as %v / %v", got.Description, got.PlaceName)
	}
	// Believability: a REAL zero coordinate is still a coordinate, and must not
	// be confused with the absence above.
	zero, err := d.CreateSite(map[string]any{"name": "Null Island", "lat": 0.0, "lon": 0.0})
	if err != nil {
		t.Fatal(err)
	}
	if zero.Lat == nil || *zero.Lat != 0 {
		t.Errorf("a real 0 coordinate was lost: %v", zero.Lat)
	}
}

// TestAnUpdateWritesOnlyWhatItWasGiven.
//
// The whole reason `Patch` carries `Has*` flags. A rename must not blank the
// location or the description it never mentioned.
func TestAnUpdateWritesOnlyWhatItWasGiven(t *testing.T) {
	d := siteDB(t)
	made, err := d.CreateSite(map[string]any{
		"name": "Depot", "description": "main",
		"lat": 12.5, "lon": -3.25, "place_name": "Northtown",
		"place_region": "NR", "place_cc": "ZZ",
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := d.UpdateSite(made.ID, map[string]any{"name": "Depot 2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Depot 2" {
		t.Errorf("name = %q", got.Name)
	}
	if sstr(got.Description) != "main" {
		t.Errorf("a rename blanked the description (%q)", sstr(got.Description))
	}
	if got.Lat == nil || sstr(got.PlaceName) != "Northtown" {
		t.Errorf("a rename blanked the location (%v / %q)", got.Lat, sstr(got.PlaceName))
	}

	// ...and an EXPLICIT nil does clear.
	cleared, err := d.UpdateSite(made.ID, map[string]any{
		"lat": nil, "lon": nil, "place_name": nil, "place_region": nil, "place_cc": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Lat != nil || cleared.PlaceName != nil {
		t.Errorf("an explicit clear left %v / %v", cleared.Lat, cleared.PlaceName)
	}
	if sstr(cleared.Description) != "main" {
		t.Error("clearing the location took the description with it")
	}
}

// TestAnEmptyUpdateReturnsTheSiteUnchanged. A body whose every field was absent
// is a legitimate request that has nothing to say — not an error, and not a nil.
func TestAnEmptyUpdateReturnsTheSiteUnchanged(t *testing.T) {
	d := siteDB(t)
	made, _ := d.CreateSite(map[string]any{"name": "Depot", "description": "main"})

	got, err := d.UpdateSite(made.ID, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "Depot" || sstr(got.Description) != "main" {
		t.Errorf("an empty update returned %+v", got)
	}
}

// TestOnlyWhitelistedColumnsAreWritten.
//
// The column name goes into SQL TEXT — an identifier cannot be parameterised — so
// this list is the injection boundary, not a tidiness rule.
func TestOnlyWhitelistedColumnsAreWritten(t *testing.T) {
	d := siteDB(t)
	made, _ := d.CreateSite(map[string]any{"name": "Depot"})

	got, err := d.UpdateSite(made.ID, map[string]any{
		"name": "Renamed",
		"id":   "forged",
		// If an unknown key reached the SQL text, this would be a syntax error
		// at best and a second statement at worst.
		"created_at":          999,
		"name = 'x' --":       "y",
		"description) VALUES": "z",
	})
	if err != nil {
		t.Fatalf("an unknown column reached the statement: %v", err)
	}
	if got.ID != made.ID {
		t.Errorf("the id was overwritten: %q", got.ID)
	}
	if got.CreatedAt != made.CreatedAt {
		t.Errorf("created_at was overwritten: %d", got.CreatedAt)
	}
	if got.Name != "Renamed" {
		t.Errorf("the legitimate column was not written: %q", got.Name)
	}
}

// TestADuplicateNameIsRefusedByTheIndex.
//
// CASE-INSENSITIVELY, because the column is `UNIQUE COLLATE NOCASE`: these are
// human labels picked from a list, and two differing only in case are a mistake
// rather than a distinction. The index is the enforcement — a pre-check would
// race and one of two simultaneous creates still has to lose.
func TestADuplicateNameIsRefusedByTheIndex(t *testing.T) {
	d := siteDB(t)
	if _, err := d.CreateSite(map[string]any{"name": "Depot"}); err != nil {
		t.Fatal(err)
	}

	_, err := d.CreateSite(map[string]any{"name": "dEpOt"})
	if err == nil {
		t.Fatal("a name differing only in case was accepted")
	}
	if !IsDuplicateSiteName(err) {
		t.Errorf("the duplicate was not recognised as one: %v", err)
	}
	// Believability: an unrelated error must NOT be reported as a duplicate, or
	// every failure would answer 409.
	if IsDuplicateSiteName(errNotUnique) {
		t.Error("an unrelated error is being read as a duplicate name")
	}
	// ...and a genuinely different name still works.
	if _, err := d.CreateSite(map[string]any{"name": "Annexe"}); err != nil {
		t.Errorf("a distinct name was refused: %v", err)
	}
}

var errNotUnique = sql.ErrConnDone

// TestDeleteSiteReportsWhetherARowWent.
func TestDeleteSiteReportsWhetherARowWent(t *testing.T) {
	d := siteDB(t)
	made, _ := d.CreateSite(map[string]any{"name": "Depot"})

	gone, err := d.DeleteSite(made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gone {
		t.Error("removing an existing site reported nothing went")
	}
	if again, _ := d.DeleteSite(made.ID); again {
		t.Error("removing it twice reported a second row")
	}
	if got, _ := d.GetSite(made.ID); got != nil {
		t.Errorf("the site survived: %+v", got)
	}
}

// TestGetSiteDistinguishesMissingFromBroken.
//
// Collapsing the two tells an operator their site id is wrong when the database
// is unreadable, and they go and check the id.
func TestGetSiteDistinguishesMissingFromBroken(t *testing.T) {
	d := siteDB(t)
	got, err := d.GetSite("nope")
	if err != nil || got != nil {
		t.Errorf("a missing site gave (%v, %v), want (nil, nil)", got, err)
	}
	_ = d.Close()
	if _, err := d.GetSite("nope"); err == nil {
		t.Error("a closed database reported no error")
	}
}

// sstr reads a nullable text column for comparison, mapping NULL to "".
//
// The four text columns on a site became `*string` when live verification showed
// this port sending `""` where the live app sends `null`. A test asserting a
// VALUE does not care which it was; the two tests above that assert ABSENCE
// compare against nil directly, because that is the distinction.
func sstr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

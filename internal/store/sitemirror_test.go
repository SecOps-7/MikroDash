package store

// The DOWNGRADE MIRROR: `siteId` must always be `siteIds[0]` or null.
//
// ── WHY A STALE MIRROR IS NOT COSMETIC ──────────────────────────────────────
//
// Since #117 a device holds a LIST, and `siteId` survives as a scalar mirror of
// its first entry. Every reader on both sides prefers the list, so a stale
// scalar changes nothing anyone can see today — which is exactly why it would
// have gone unnoticed. It matters on a DOWNGRADE: a pre-#117 build reads the
// scalar and nothing else, so a device whose membership changed under the new
// build would show up in a site it had left.
//
// The live `Routers.update` recomputes it on every write
// (`siteId: _updSiteIds(data, existing)[0] || null`) and `loadAll` normalises it
// on every read. This port's `UpdateRouter` merged the patch and left the mirror
// alone — so `PUT /api/sites/:id/routers`, which writes `siteIds` and nothing
// else, produced a file Node would never have written.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func mirrorStore(t *testing.T, routersJSON string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"routers.json": routersJSON, "settings.json": `{}`, ".secret": "test-secret",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// rawRecord reads one record back as it is ON DISK, not through any normaliser —
// the whole point is what the file says.
func rawRecord(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var all []map[string]any
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		if r["id"] == id {
			return r
		}
	}
	t.Fatalf("router %s is not in the file", id)
	return nil
}

const mirrorFixture = `[
  {"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":"",
   "siteIds":["s1","s2"],"siteId":"s1"}
]`

// TestWritingSiteIdsKeepsTheMirrorTrue.
func TestWritingSiteIdsKeepsTheMirrorTrue(t *testing.T) {
	s, dir := mirrorStore(t, mirrorFixture)

	// Believability: the fixture starts consistent, so a failure below is the
	// write's doing and not the fixture's.
	if got := rawRecord(t, dir, "r1")["siteId"]; got != "s1" {
		t.Fatalf("the fixture does not start with a true mirror: %v", got)
	}

	// The membership route's write: `siteIds` and nothing else.
	if err := s.UpdateRouter("r1", map[string]any{"siteIds": []string{"s2", "s3"}}); err != nil {
		t.Fatal(err)
	}
	rec := rawRecord(t, dir, "r1")
	if got := rec["siteId"]; got != "s2" {
		t.Errorf("siteId = %v, want s2 -- a pre-#117 build reading this file would "+
			"still place the device in the site it just left", got)
	}
}

// TestEmptyingTheListNullsTheMirror.
//
// NULL, not the empty string and not the field left behind: `kept.length ?
// kept[0] : null`. A device detached from every site must not read as belonging
// to one.
func TestEmptyingTheListNullsTheMirror(t *testing.T) {
	s, dir := mirrorStore(t, mirrorFixture)

	if err := s.UpdateRouter("r1", map[string]any{"siteIds": []string{}}); err != nil {
		t.Fatal(err)
	}
	rec := rawRecord(t, dir, "r1")
	v, present := rec["siteId"]
	if !present {
		t.Fatal("siteId was removed from the record rather than set to null")
	}
	if v != nil {
		t.Errorf("siteId = %v, want null", v)
	}
}

// TestAScalarWriteStillFillsTheList.
//
// The other direction: an older client sends `siteId` alone, and the LIST is
// what every current reader consults. Without this the device would look
// site-less to the page that just assigned it.
func TestAScalarWriteStillFillsTheList(t *testing.T) {
	s, dir := mirrorStore(t,
		`[{"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":""}]`)

	if err := s.UpdateRouter("r1", map[string]any{"siteId": "s9"}); err != nil {
		t.Fatal(err)
	}
	rec := rawRecord(t, dir, "r1")
	ids, _ := rec["siteIds"].([]any)
	if len(ids) != 1 || ids[0] != "s9" {
		t.Errorf("siteIds = %v, want [s9]", rec["siteIds"])
	}
	if rec["siteId"] != "s9" {
		t.Errorf("siteId = %v", rec["siteId"])
	}
}

// TestAnUnrelatedPatchLeavesMembershipAlone.
//
// The recompute must not become a WRITE. An edit that never mentions a site has
// to leave both fields exactly as they were — including a record that carries
// neither, which must not gain an empty list it never had.
func TestAnUnrelatedPatchLeavesMembershipAlone(t *testing.T) {
	s, dir := mirrorStore(t, mirrorFixture)
	if err := s.UpdateRouter("r1", map[string]any{"label": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	rec := rawRecord(t, dir, "r1")
	ids, _ := rec["siteIds"].([]any)
	if len(ids) != 2 || ids[0] != "s1" || ids[1] != "s2" {
		t.Errorf("a rename changed siteIds to %v", rec["siteIds"])
	}
	if rec["siteId"] != "s1" {
		t.Errorf("a rename changed siteId to %v", rec["siteId"])
	}

	s2, dir2 := mirrorStore(t,
		`[{"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":""}]`)
	if err := s2.UpdateRouter("r1", map[string]any{"label": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	rec2 := rawRecord(t, dir2, "r1")
	if _, has := rec2["siteIds"]; has {
		t.Errorf("a rename gave a site-less device a siteIds field: %v", rec2["siteIds"])
	}
	if _, has := rec2["siteId"]; has {
		t.Errorf("a rename gave a site-less device a siteId field: %v", rec2["siteId"])
	}
}

// TestDuplicateSiteIdsAreCollapsed.
//
// `_cleanSiteIds` dedupes (`out.indexOf(id) === -1`). A duplicate makes the
// device count twice in the Sites card's per-site column, which is the one place
// the number is visible.
func TestDuplicateSiteIdsAreCollapsed(t *testing.T) {
	s, dir := mirrorStore(t, mirrorFixture)
	if err := s.UpdateRouter("r1",
		map[string]any{"siteIds": []string{"s1", "s2", "s1", "", "  "}}); err != nil {
		t.Fatal(err)
	}
	ids, _ := rawRecord(t, dir, "r1")["siteIds"].([]any)
	if len(ids) != 2 || ids[0] != "s1" || ids[1] != "s2" {
		t.Errorf("siteIds = %v, want [s1 s2] -- duplicates and blanks are dropped, "+
			"and the FIRST occurrence keeps its position", ids)
	}
}

package store

// `ClearSite` — the cascade a deleted site needs, because sites live in SQLite
// and devices in `routers.json` and there is no foreign key between them.

import (
	"os"
	"path/filepath"
	"testing"
)

const clearFixture = `[
  {"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":"",
   "siteIds":["s1","s2"],"siteId":"s1"},
  {"id":"r2","label":"Two","host":"198.51.100.2","port":8728,"username":"u","password":"",
   "siteIds":["s1"],"siteId":"s1"},
  {"id":"r3","label":"Three","host":"198.51.100.3","port":8728,"username":"u","password":"",
   "siteIds":["s2"],"siteId":"s2"},
  {"id":"r4","label":"Four","host":"198.51.100.4","port":8728,"username":"u","password":""}
]`

// TestClearSiteRemovesOnlyThatSite.
//
// r1 is in TWO sites and must keep the other. That is the #117 rule, and it is
// the reason this is a filter rather than a null: before it, detaching a device
// from a deleted site took every membership it had.
func TestClearSiteRemovesOnlyThatSite(t *testing.T) {
	s, dir := mirrorStore(t, clearFixture)

	n, err := s.ClearSite("s1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("changed %d devices, want 2 (r1 and r2)", n)
	}

	r1 := rawRecord(t, dir, "r1")
	ids, _ := r1["siteIds"].([]any)
	if len(ids) != 1 || ids[0] != "s2" {
		t.Errorf("r1 = %v, want [s2] -- a device in two sites lost the one it keeps", ids)
	}
	if r1["siteId"] != "s2" {
		t.Errorf("r1 mirror = %v, want s2", r1["siteId"])
	}

	// r2's ONLY site went, so it is detached entirely and the mirror is null.
	r2 := rawRecord(t, dir, "r2")
	if ids2, _ := r2["siteIds"].([]any); len(ids2) != 0 {
		t.Errorf("r2 = %v, want empty", ids2)
	}
	if v, present := r2["siteId"]; !present || v != nil {
		t.Errorf("r2 mirror = %v (present=%v), want null", v, present)
	}

	// r3 was never in s1 and r4 has no membership fields at all. NEITHER may be
	// touched -- and r4 must not GAIN the fields.
	r3 := rawRecord(t, dir, "r3")
	if ids3, _ := r3["siteIds"].([]any); len(ids3) != 1 || ids3[0] != "s2" {
		t.Errorf("r3 = %v, want [s2]", ids3)
	}
	r4 := rawRecord(t, dir, "r4")
	if _, has := r4["siteIds"]; has {
		t.Errorf("a device with no membership gained siteIds: %v", r4["siteIds"])
	}
	if _, has := r4["siteId"]; has {
		t.Errorf("a device with no membership gained siteId: %v", r4["siteId"])
	}
}

// TestAPre117ScalarIsDetachedToo.
//
// A record written before multi-site carries `siteId` and no list. If the
// cascade only read the list, that device would keep pointing at a site that no
// longer exists -- a blank chip, and unreachable to a site-scoped grant.
func TestAPre117ScalarIsDetachedToo(t *testing.T) {
	s, dir := mirrorStore(t,
		`[{"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u",
		   "password":"","siteId":"s1"}]`)

	n, err := s.ClearSite("s1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("changed %d, want 1 -- a scalar-only record was not seen", n)
	}
	rec := rawRecord(t, dir, "r1")
	if ids, _ := rec["siteIds"].([]any); len(ids) != 0 {
		t.Errorf("siteIds = %v", ids)
	}
	if rec["siteId"] != nil {
		t.Errorf("siteId = %v, want null", rec["siteId"])
	}
}

// TestAnEmptyArrayBeatsAStaleScalar.
//
// The array wins outright when present, EVEN WHEN EMPTY. A device whose
// membership was just cleared carries `siteIds: []` beside a scalar that has not
// caught up; reading the scalar there would re-attach it to a site it left and
// then "detach" it, reporting a change that never needed making.
func TestAnEmptyArrayBeatsAStaleScalar(t *testing.T) {
	s, _ := mirrorStore(t,
		`[{"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u",
		   "password":"","siteIds":[],"siteId":"s1"}]`)

	n, err := s.ClearSite("s1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("changed %d, want 0 -- the stale scalar was read over the empty array", n)
	}
}

// TestClearingASiteNobodyIsInWritesNothing.
//
// The live guard is `if (changed) { … _writeFile(routers); }`. During
// coexistence the Node process holds the same file, so a rewrite that changes
// nothing is a chance for the two to interleave for no gain.
func TestClearingASiteNobodyIsInWritesNothing(t *testing.T) {
	s, dir := mirrorStore(t, clearFixture)
	path := filepath.Join(dir, "routers.json")

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.ClearSite("s-nobody")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("changed %d for a site nobody is in", n)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("the file was rewritten for a no-op cascade")
	}

	// Believability: a REAL cascade does rewrite it, so the comparison above is
	// not holding because ClearSite never writes.
	if _, err := s.ClearSite("s1"); err != nil {
		t.Fatal(err)
	}
	real, _ := os.ReadFile(path)
	if string(real) == string(before) {
		t.Fatal("a real cascade left the file identical, so this test proves nothing")
	}
}

// TestAnEmptySiteIdIsRefusedOutright.
//
// It matches no membership, so a walk could only cost a read -- or, if the
// filter were ever loosened, detach the entire fleet.
func TestAnEmptySiteIdIsRefusedOutright(t *testing.T) {
	s, dir := mirrorStore(t, clearFixture)
	n, err := s.ClearSite("")
	if err != nil || n != 0 {
		t.Errorf("ClearSite(\"\") = (%d, %v), want (0, nil)", n, err)
	}
	if ids, _ := rawRecord(t, dir, "r1")["siteIds"].([]any); len(ids) != 2 {
		t.Errorf("an empty site id changed a membership: %v", ids)
	}

	// THE GUARD IS BEFORE THE READ, which is where the live one is
	// (`if (!siteId) return 0;` precedes `loadAll()`). Asserting only the
	// membership above cannot tell that from a guard placed after it: an empty id
	// matches no membership either way, since `cleanSiteIDs` drops blanks, so the
	// walk would reach the same answer having done the work. Removing the file
	// separates them -- a walk fails, and the guard still answers (0, nil).
	if err := os.Remove(filepath.Join(dir, "routers.json")); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ClearSite(""); err != nil || n != 0 {
		t.Errorf("with no routers.json, ClearSite(\"\") = (%d, %v), want (0, nil) -- the "+
			"guard is running after the read", n, err)
	}
	// Believability: a REAL id does surface the missing file, so the line above
	// is about the guard and not about ClearSite swallowing every error.
	if _, err := s.ClearSite("s1"); err == nil {
		t.Error("a missing routers.json was not reported for a real site id")
	}
}

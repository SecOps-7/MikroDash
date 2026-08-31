package db

// The group writes: create, partial update, delete-with-grants, membership.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func groupDB(t *testing.T) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	// The same DDL the global-admin tests use: the live shape of the four
	// principal tables, checked by tools/schema-audit.js.
	if _, err := h.Exec(adminDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	return openTest(t, dir)
}

func TestCreateGroupRoundTrips(t *testing.T) {
	d := groupDB(t)

	got, err := d.CreateGroup(map[string]any{"name": "Ops", "description": "the ops team"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ops" || got.Description == nil || *got.Description != "the ops team" {
		t.Errorf("stored %+v", got)
	}
	if len(got.ID) != 36 {
		t.Errorf("id = %q, want a v4 uuid", got.ID)
	}
	if got.CreatedAt == 0 {
		t.Error("created_at was not stamped")
	}

	// A group with NO description reads back empty rather than erroring — the
	// column is NULLABLE, which is the shape that broke GetSite for three ticks.
	bare, err := d.CreateGroup(map[string]any{"name": "Bare"})
	if err != nil {
		t.Fatal(err)
	}
	// NIL, not "". `Group.Description` is a pointer so the two stay
	// distinguishable — a group whose description was explicitly cleared and one
	// that never had it are the same row, but the type does not pretend
	// otherwise on the way out.
	if bare.Description != nil {
		t.Errorf("a NULL description read back as %q", *bare.Description)
	}
}

// TestAGroupCreateNeverTakesItsIdFromTheCaller.
//
// The id is what every grant and membership row names, so a caller choosing it
// could reuse the id of a group just deleted and inherit its grants.
func TestAGroupCreateNeverTakesItsIdFromTheCaller(t *testing.T) {
	d := groupDB(t)

	first, err := d.CreateGroup(map[string]any{"name": "One"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.CreateGroup(map[string]any{
		"name": "Two", "id": "forged-id", "created_at": int64(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "forged-id" {
		t.Error("the caller chose the group id")
	}
	if got.CreatedAt == 1 {
		t.Error("the caller chose created_at")
	}
	// Believability: the ids really are fresh, so "not forged-id" is not holding
	// because the function returns a constant.
	if got.ID == first.ID {
		t.Error("two creates produced the same id")
	}
	if g, _ := d.GetGroup("forged-id"); g != nil {
		t.Errorf("a group was stored under the caller's id: %+v", g)
	}
}

// TestAGroupUpdateWritesOnlyWhatItWasGiven.
func TestAGroupUpdateWritesOnlyWhatItWasGiven(t *testing.T) {
	d := groupDB(t)
	made, err := d.CreateGroup(map[string]any{"name": "Ops", "description": "the ops team"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := d.UpdateGroup(made.ID, map[string]any{"name": "Ops 2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ops 2" {
		t.Errorf("name = %q", got.Name)
	}
	if got.Description == nil || *got.Description != "the ops team" {
		t.Errorf("a rename blanked the description (%v)", got.Description)
	}

	// ...and an EXPLICIT nil does clear it.
	cleared, err := d.UpdateGroup(made.ID, map[string]any{"description": nil})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Description != nil {
		t.Errorf("an explicit clear left %q", *cleared.Description)
	}
	if cleared.Name != "Ops 2" {
		t.Error("clearing the description took the name with it")
	}
}

// TestAnEmptyGroupUpdateReturnsItUnchanged.
func TestAnEmptyGroupUpdateReturnsItUnchanged(t *testing.T) {
	d := groupDB(t)
	made, _ := d.CreateGroup(map[string]any{"name": "Ops", "description": "x"})

	got, err := d.UpdateGroup(made.ID, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Name != "Ops" || got.Description == nil || *got.Description != "x" {
		t.Errorf("an empty update returned %+v", got)
	}
}

// TestOnlyWhitelistedGroupColumnsAreWritten.
//
// A column name goes into SQL TEXT — an identifier cannot be parameterised — so
// this list is the injection boundary, not a tidiness rule.
func TestOnlyWhitelistedGroupColumnsAreWritten(t *testing.T) {
	d := groupDB(t)
	made, _ := d.CreateGroup(map[string]any{"name": "Ops"})

	got, err := d.UpdateGroup(made.ID, map[string]any{
		"name":                "Renamed",
		"id":                  "forged",
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

// TestDeletingAGroupTakesItsGrants.
//
// Memberships cascade through a foreign key; the grants CANNOT, because
// `principal_id` is polymorphic and no key can point at two tables. A grant whose
// principal does not exist is invisible in every card and still consulted by the
// resolver.
func TestDeletingAGroupTakesItsGrants(t *testing.T) {
	d := groupDB(t)
	made, _ := d.CreateGroup(map[string]any{"name": "Ops"})
	other, _ := d.CreateGroup(map[string]any{"name": "Other"})

	seedGrant(t, d, "group", made.ID)
	seedGrant(t, d, "group", other.ID)
	// A USER grant that happens to share the group's id. Ids are opaque, and a
	// delete keyed on the id alone would revoke a person's access with a group's.
	seedGrant(t, d, "user", made.ID)

	// Believability: all three exist first.
	if n := countGrants(t, d, ""); n != 3 {
		t.Fatalf("seeded %d grants, want 3", n)
	}

	gone, err := d.DeleteGroup(made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gone {
		t.Error("removing an existing group reported nothing went")
	}
	if g, _ := d.GetGroup(made.ID); g != nil {
		t.Errorf("the group survived: %+v", g)
	}
	if n := countGrants(t, d, "group"); n != 1 {
		t.Errorf("%d group grants left, want 1 (the other group's)", n)
	}
	if n := countGrants(t, d, "user"); n != 1 {
		t.Errorf("a USER grant sharing the group's id was removed with it (%d left)", n)
	}
	if again, _ := d.DeleteGroup(made.ID); again {
		t.Error("removing it twice reported a second row")
	}
}

// TestSetGroupMembersReplacesAndDedupes.
func TestSetGroupMembersReplacesAndDedupes(t *testing.T) {
	d := groupDB(t)
	made, _ := d.CreateGroup(map[string]any{"name": "Ops"})

	got, err := d.SetGroupMembers(made.ID, []string{"u-1", "u-2", "u-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "u-1" || got[1] != "u-2" {
		t.Errorf("returned %v, want [u-1 u-2] -- deduped, first occurrence keeping its "+
			"position", got)
	}
	members, err := d.GroupMembers(made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Errorf("stored %v", members)
	}

	// REPLACES, not merges.
	if _, err := d.SetGroupMembers(made.ID, []string{"u-3"}); err != nil {
		t.Fatal(err)
	}
	members, _ = d.GroupMembers(made.ID)
	if len(members) != 1 || members[0] != "u-3" {
		t.Errorf("the second save merged instead of replacing: %v", members)
	}

	// ...and an EMPTY list empties the group, which is what makes this one of
	// the ways to orphan the last administrator.
	if _, err := d.SetGroupMembers(made.ID, nil); err != nil {
		t.Fatal(err)
	}
	members, _ = d.GroupMembers(made.ID)
	if len(members) != 0 {
		t.Errorf("an empty list left %v", members)
	}
}

// TestEmptyingAnAdminGroupIsCaughtBeforeItHappens.
//
// The seam `SetGroupMembersTx` exists for this: the guard has to ask what a
// membership change WOULD do, and it can only do that by performing it inside a
// transaction it then rolls back.
func TestEmptyingAnAdminGroupIsCaughtBeforeItHappens(t *testing.T) {
	d := groupDB(t)
	made, _ := d.CreateGroup(map[string]any{"name": "Admins"})

	if _, err := d.sql.Exec(`INSERT INTO grants
	    (principal_type, principal_id, role_id, scope_type, scope_id, created_at)
	  VALUES ('group', ?, 'administrator', 'global', '', 0)`, made.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetGroupMembers(made.ID, []string{"u-1"}); err != nil {
		t.Fatal(err)
	}

	// Believability: u-1 IS the only administrator right now.
	admins, err := d.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 1 || admins[0] != "u-1" {
		t.Fatalf("admins = %v, want [u-1]", admins)
	}

	orphan, err := d.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		return SetGroupMembersTx(tx, made.ID, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !orphan {
		t.Error("emptying the group holding the only global admin grant was not caught -- " +
			"the least obvious of the ways to orphan the last administrator")
	}

	// ...and the probe left the membership alone.
	members, _ := d.GroupMembers(made.ID)
	if len(members) != 1 {
		t.Errorf("the probe APPLIED the change: members = %v", members)
	}
}

func seedGrant(t *testing.T, d *DB, principalType, principalID string) {
	t.Helper()
	if _, err := d.sql.Exec(`INSERT INTO grants
	    (principal_type, principal_id, role_id, scope_type, scope_id, created_at)
	  VALUES (?, ?, 'administrator', 'router', 'r1', 0)`, principalType, principalID); err != nil {
		t.Fatal(err)
	}
}

func countGrants(t *testing.T, d *DB, principalType string) int {
	t.Helper()
	var n int
	var err error
	if principalType == "" {
		err = d.sql.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&n)
	} else {
		err = d.sql.QueryRow(
			`SELECT COUNT(*) FROM grants WHERE principal_type = ?`, principalType).Scan(&n)
	}
	if err != nil {
		t.Fatal(err)
	}
	return n
}

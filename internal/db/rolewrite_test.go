package db

// The role writes: create, partial update, the builtin refusal, and the matrix.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// roleDB uses the same DDL as the global-admin tests — the live shape of the
// principal tables — plus role_pages. `grants.role_id` carries ON DELETE
// RESTRICT so the ENGINE, not this port, refuses a role a grant still holds.
func roleDB(t *testing.T) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(adminDDL + `
CREATE TABLE role_pages (
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
  page    TEXT NOT NULL,
  access  TEXT NOT NULL,
  PRIMARY KEY (role_id, page));
`); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	return openTest(t, dir)
}

func TestCreateRoleIsNeverBuiltin(t *testing.T) {
	d := roleDB(t)

	got, err := d.CreateRole(map[string]any{
		"name": "Auditor", "description": "read only",
		// THE ESCALATION THIS REFUSES: a caller asking for a builtin role. A
		// builtin holds every known permission structurally, and a global grant
		// of one counts as administrator access in GlobalAdminUserIDs — so
		// honouring this would mint an administrator from a role nobody thought
		// conferred anything.
		"builtin": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Builtin {
		t.Error("the caller set `builtin` on a new role")
	}
	if got.Name != "Auditor" || got.Description == nil || *got.Description != "read only" {
		t.Errorf("stored %+v", got)
	}
	if len(got.ID) != 36 || got.CreatedAt == 0 {
		t.Errorf("id/created_at not minted: %+v", got)
	}

	// Believability: the fixture DOES contain a real builtin role, so "not
	// builtin" above is a fact about this row rather than about the table.
	admin, err := d.GetRole("administrator")
	if err != nil {
		t.Fatal(err)
	}
	if admin == nil || !admin.Builtin {
		t.Fatal("the fixture has no builtin role, so the assertion above proves nothing")
	}
}

// TestARoleCannotPromoteItselfToBuiltin.
//
// The update path's half of the same rule. `roleWritableColumns` has two entries
// and this is why.
func TestARoleCannotPromoteItselfToBuiltin(t *testing.T) {
	d := roleDB(t)
	made, err := d.CreateRole(map[string]any{"name": "Auditor"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := d.UpdateRole(made.ID, map[string]any{"name": "Renamed", "builtin": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Builtin {
		t.Error("a custom role promoted itself into the structural one")
	}
	if got.Name != "Renamed" {
		t.Errorf("the legitimate column was not written: %q", got.Name)
	}
	// ...and the id and timestamp are not writable either.
	if got.ID != made.ID || got.CreatedAt != made.CreatedAt {
		t.Errorf("id or created_at moved: %+v", got)
	}
}

// TestARoleUpdateWritesOnlyWhatItWasGiven.
func TestARoleUpdateWritesOnlyWhatItWasGiven(t *testing.T) {
	d := roleDB(t)
	made, _ := d.CreateRole(map[string]any{"name": "Auditor", "description": "read only"})

	got, err := d.UpdateRole(made.ID, map[string]any{"name": "Auditor 2"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Description == nil || *got.Description != "read only" {
		t.Errorf("a rename blanked the description (%v)", got.Description)
	}

	cleared, err := d.UpdateRole(made.ID, map[string]any{"description": nil})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Description != nil {
		t.Errorf("an explicit clear left %q", *cleared.Description)
	}
	if cleared.Name != "Auditor 2" {
		t.Error("clearing the description took the name with it")
	}

	// An EMPTY update returns the role unchanged rather than erroring.
	same, err := d.UpdateRole(made.ID, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if same == nil || same.Name != "Auditor 2" {
		t.Errorf("an empty update returned %+v", same)
	}
}

// TestDeletingABuiltinRoleIsRefused.
//
// Not an error — false, as the live function answers, because the route has
// already looked the role up and tells a missing one from a protected one.
func TestDeletingABuiltinRoleIsRefused(t *testing.T) {
	d := roleDB(t)

	gone, err := d.DeleteRole("administrator")
	if err != nil {
		t.Fatalf("refusing a builtin role errored: %v", err)
	}
	if gone {
		t.Error("the builtin role was deleted")
	}
	if r, _ := d.GetRole("administrator"); r == nil {
		t.Error("the builtin role is gone from the table")
	}

	// A MISSING role answers the same way, and for the same reason.
	if gone, err := d.DeleteRole("no-such-role"); err != nil || gone {
		t.Errorf("deleting a missing role = (%v, %v), want (false, nil)", gone, err)
	}

	// Believability: a CUSTOM role really can be deleted, so the refusals above
	// are about builtin-ness rather than about a function that never deletes.
	made, _ := d.CreateRole(map[string]any{"name": "Auditor"})
	if gone, err := d.DeleteRole(made.ID); err != nil || !gone {
		t.Errorf("a custom role was not deleted: (%v, %v)", gone, err)
	}
}

// TestARoleHeldByAGrantIsRefusedByTheEngine.
//
// `ON DELETE RESTRICT` on `grants.role_id` is what refuses it — deliberately not
// re-checked in Go, since a second answer to a question the schema answers could
// disagree with it under concurrency. `CountGrantsForRole` exists so the route
// can say HOW MANY rather than surfacing a bare constraint error.
func TestARoleHeldByAGrantIsRefusedByTheEngine(t *testing.T) {
	d := roleDB(t)
	made, _ := d.CreateRole(map[string]any{"name": "Auditor"})
	seedGrant(t, d, "user", "u-1")
	if _, err := d.sql.Exec(
		`UPDATE grants SET role_id = ? WHERE principal_id = 'u-1'`, made.ID); err != nil {
		t.Fatal(err)
	}

	n, err := d.CountGrantsForRole(made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the grant was not seeded onto the role (%d)", n)
	}

	if _, err := d.DeleteRole(made.ID); err == nil {
		t.Error("a role still held by a grant was deleted -- ON DELETE RESTRICT is not in " +
			"force, and a grant now names a role that does not exist")
	}
	if r, _ := d.GetRole(made.ID); r == nil {
		t.Error("the role went despite the refusal")
	}
}

// TestSetRolePagesReplacesAndDropsUnknownAccess.
func TestSetRolePagesReplacesAndDropsUnknownAccess(t *testing.T) {
	d := roleDB(t)
	made, _ := d.CreateRole(map[string]any{"name": "Auditor"})

	kept, err := d.SetRolePages(made.ID, []RolePage{
		{Page: "devices", Access: "write"},
		{Page: "logs", Access: "read"},
		// DROPPED, SILENTLY, and the rest of the matrix is still written. The
		// live filter accepts only read/write; refusing the whole save would turn
		// one bad row into a lost edit.
		{Page: "settings", Access: "admin"},
		{Page: "", Access: "read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Errorf("kept %v, want the two valid rows", kept)
	}

	stored, err := d.RolePages(made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored %v", stored)
	}
	for _, p := range stored {
		if p.Access != "read" && p.Access != "write" {
			t.Errorf("an unknown access reached the table: %+v", p)
		}
		if p.Page == "settings" {
			t.Error("the row with an invalid access was written anyway")
		}
	}

	// REPLACES, not merges.
	if _, err := d.SetRolePages(made.ID, []RolePage{{Page: "audit", Access: "read"}}); err != nil {
		t.Fatal(err)
	}
	stored, _ = d.RolePages(made.ID)
	if len(stored) != 1 || stored[0].Page != "audit" {
		t.Errorf("the second save merged instead of replacing: %v", stored)
	}

	// ...and an EMPTY matrix clears it, which is how a role is reduced to nothing.
	if _, err := d.SetRolePages(made.ID, nil); err != nil {
		t.Fatal(err)
	}
	stored, _ = d.RolePages(made.ID)
	if len(stored) != 0 {
		t.Errorf("an empty matrix left %v", stored)
	}
}

package db

// The grant writes: the upsert's replace-not-stack rule, the legacy mirror, and
// the two deletes.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// grantDDL is the live shape INCLUDING the constraints the upsert depends on.
//
// The UNIQUE index is not decoration here: ON CONFLICT names it, so without it
// the upsert silently becomes an insert and every "replaces" assertion below
// would pass against a port that stacked rows. The CHECK is the live one too --
// a global grant must carry an empty scope_id, which is what scopeIDFor exists
// to guarantee. adminDDL cannot be reused: it omits both.
// (No backticks in this comment: it sits inside a Go raw string.)
const grantDDL = `
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  builtin INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  created_at INTEGER NOT NULL);
CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
CREATE TABLE grants (
  id             TEXT PRIMARY KEY,
  principal_type TEXT NOT NULL CHECK (principal_type IN ('user','group')),
  principal_id   TEXT NOT NULL,
  role_id        TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
  role           TEXT,
  scope_type     TEXT NOT NULL CHECK (scope_type IN ('global','site','router')),
  scope_id       TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  created_by     TEXT,
  CHECK ((scope_type =  'global' AND scope_id =  '')
      OR (scope_type <> 'global' AND scope_id <> '')),
  UNIQUE (principal_type, principal_id, scope_type, scope_id));
INSERT INTO roles (id, name, builtin, created_at) VALUES
  ('administrator','Administrator',1,0),
  ('operator','Operator',0,0),
  ('readonly','Read only',0,0),
  ('custom','Custom',0,0);
`

func grantDB(t *testing.T) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(grantDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	return openTest(t, dir)
}

// TestUpsertReplacesRatherThanStacks.
//
// Keyed on principal AND scope. Two rows for one principal at one scope would
// have to be resolved later, and nothing downstream resolves them.
func TestUpsertReplacesRatherThanStacks(t *testing.T) {
	d := grantDB(t)

	if err := d.UpsertGrant(GrantSpec{
		PrincipalType: "user", PrincipalID: "u-1",
		RoleID: "readonly", ScopeType: "router", ScopeID: "r1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.UpsertGrant(GrantSpec{
		PrincipalType: "user", PrincipalID: "u-1",
		RoleID: "operator", ScopeType: "router", ScopeID: "r1",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := d.ListGrants(GrantFilter{PrincipalType: "user", PrincipalID: "u-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d grants after two upserts at the same scope, want 1", len(got))
	}
	if got[0].RoleID == nil || *got[0].RoleID != "operator" {
		t.Errorf("the second upsert did not replace the role: %v", got[0].RoleID)
	}

	// Believability: a DIFFERENT scope really is a different grant, so "1" above
	// is about the conflict key rather than about a writer that never inserts.
	if err := d.UpsertGrant(GrantSpec{
		PrincipalType: "user", PrincipalID: "u-1",
		RoleID: "readonly", ScopeType: "router", ScopeID: "r2",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.ListGrants(GrantFilter{PrincipalType: "user", PrincipalID: "u-1"})
	if len(got) != 2 {
		t.Errorf("%d grants across two scopes, want 2", len(got))
	}
}

// TestAGlobalGrantStoresAnEmptyScopeID.
//
// Never NULL -- SQLite treats NULLs as distinct in a UNIQUE index, so a NULL
// would let one principal hold two global grants and the constraint would
// silently never fire. And never a leftover id: a caller sending a scope id with
// a global type must not create a row nothing can interpret.
func TestAGlobalGrantStoresAnEmptyScopeID(t *testing.T) {
	d := grantDB(t)

	if err := d.UpsertGrant(GrantSpec{
		PrincipalType: "user", PrincipalID: "u-1",
		RoleID: "administrator", ScopeType: "global",
		// SENT AND IGNORED. Without scopeIDFor this violates the CHECK.
		ScopeID: "r1",
	}); err != nil {
		t.Fatalf("a global grant carrying a scope id was refused: %v", err)
	}

	got, _ := d.ListGrants(GrantFilter{PrincipalID: "u-1"})
	if len(got) != 1 {
		t.Fatalf("%d grants", len(got))
	}
	if got[0].ScopeID == nil || *got[0].ScopeID != "" {
		t.Errorf("scope_id = %v, want an empty string", got[0].ScopeID)
	}

	// A second global grant for the same principal REPLACES the first, which is
	// the property the empty string exists to make possible.
	if err := d.UpsertGrant(GrantSpec{
		PrincipalType: "user", PrincipalID: "u-1",
		RoleID: "readonly", ScopeType: "global",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.ListGrants(GrantFilter{PrincipalID: "u-1"})
	if len(got) != 1 {
		t.Errorf("%d global grants for one principal, want 1", len(got))
	}
}

// TestTheLegacyMirrorNeverGrantsMoreThanItShould.
//
// `grants.role` is read only by a downgraded binary predating the roles table. A
// CUSTOM role has no legacy equivalent, so it mirrors as the least-privileged
// value -- mirroring it as `admin` because it happens to confer a lot would hand
// a rolled-back install more access than anybody granted.
func TestTheLegacyMirrorNeverGrantsMoreThanItShould(t *testing.T) {
	for roleID, want := range map[string]string{
		"administrator": "admin",
		"operator":      "operator",
		"readonly":      "viewer",
		"custom":        "viewer",
		"":              "viewer",
	} {
		if got := LegacyMirror(roleID); got != want {
			t.Errorf("LegacyMirror(%q) = %q, want %q", roleID, got, want)
		}
	}

	d := grantDB(t)
	if err := d.UpsertGrant(GrantSpec{
		PrincipalType: "user", PrincipalID: "u-1",
		RoleID: "custom", ScopeType: "router", ScopeID: "r1",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := d.ListGrants(GrantFilter{PrincipalID: "u-1"})
	if got[0].Role == nil || *got[0].Role != "viewer" {
		t.Errorf("a CUSTOM role mirrored as %v -- a downgraded binary would read that as "+
			"more access than was granted", got[0].Role)
	}
}

// TestAnUnrecognisedRoleFallsBackToReadonly.
//
// Least privilege, and NOT an error: the live function answers `readonly` rather
// than refusing, so the operator sees what they got.
func TestAnUnrecognisedRoleFallsBackToReadonly(t *testing.T) {
	cases := []struct {
		spec GrantSpec
		want string
	}{
		{GrantSpec{RoleID: "operator"}, "operator"},
		// The LEGACY NAME still resolves, for a caller that has not migrated.
		{GrantSpec{Role: "admin"}, "administrator"},
		{GrantSpec{Role: "viewer"}, "readonly"},
		// Neither recognised.
		{GrantSpec{Role: "wizard"}, "readonly"},
		{GrantSpec{}, "readonly"},
		// The ID WINS over the legacy name when both are sent.
		{GrantSpec{RoleID: "operator", Role: "admin"}, "operator"},
	}
	for _, c := range cases {
		if got := resolveRoleID(c.spec); got != c.want {
			t.Errorf("resolveRoleID(%+v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

// TestDeleteGrantAndDeleteForPrincipal.
func TestDeleteGrantAndDeleteForPrincipal(t *testing.T) {
	d := grantDB(t)
	for _, s := range []GrantSpec{
		{PrincipalType: "user", PrincipalID: "u-1", RoleID: "readonly",
			ScopeType: "router", ScopeID: "r1"},
		{PrincipalType: "user", PrincipalID: "u-1", RoleID: "readonly",
			ScopeType: "router", ScopeID: "r2"},
		// A GROUP sharing the user's id. Ids are opaque, and a delete keyed on
		// the id alone would revoke a person's access along with a group's.
		{PrincipalType: "group", PrincipalID: "u-1", RoleID: "readonly",
			ScopeType: "router", ScopeID: "r1"},
	} {
		if err := d.UpsertGrant(s); err != nil {
			t.Fatal(err)
		}
	}

	all, _ := d.ListGrants(GrantFilter{})
	if len(all) != 3 {
		t.Fatalf("seeded %d grants, want 3", len(all))
	}

	// One by id.
	gone, err := d.DeleteGrant(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !gone {
		t.Error("deleting an existing grant reported nothing went")
	}
	if again, _ := d.DeleteGrant(all[0].ID); again {
		t.Error("deleting it twice reported a second row")
	}

	// The rest of the USER's, leaving the group's alone.
	n, err := d.DeleteGrantsForPrincipal("user", "u-1")
	if err != nil {
		t.Fatal(err)
	}
	left, _ := d.ListGrants(GrantFilter{})
	if len(left) != 1 || left[0].PrincipalType != "group" {
		t.Errorf("removed %d and left %+v -- the group's grant should survive", n, left)
	}
}

// TestAGrantNeedsAPrincipal. An empty principal would produce a row nothing can
// attribute, and `ListGrants` treats an empty filter as "everything" -- so it
// would be invisible to a targeted query and present in every listing.
func TestAGrantNeedsAPrincipal(t *testing.T) {
	d := grantDB(t)
	for _, s := range []GrantSpec{
		{PrincipalType: "user", RoleID: "readonly", ScopeType: "global"},
		{PrincipalID: "u-1", RoleID: "readonly", ScopeType: "global"},
		{PrincipalType: "  ", PrincipalID: "  ", RoleID: "readonly", ScopeType: "global"},
	} {
		if err := d.UpsertGrant(s); err == nil {
			t.Errorf("a grant with no principal was accepted: %+v", s)
		}
	}
	if all, _ := d.ListGrants(GrantFilter{}); len(all) != 0 {
		t.Errorf("a refused grant was still written: %+v", all)
	}
}

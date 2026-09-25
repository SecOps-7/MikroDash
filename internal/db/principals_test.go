package db

// The principal graph's reads.
//
// The DDL is the LIVE schema, copied — `groups.name` and `roles.name` are
// `COLLATE NOCASE`, and `roles.builtin` is an INTEGER. Both matter to what is
// asserted here, so a tidied-up test schema would be testing something else.

import (
	"database/sql"
	"path/filepath"
	"testing"
)

const principalsDDL = `
CREATE TABLE groups (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT,
  created_at  INTEGER NOT NULL
);
CREATE TABLE group_members (
  group_id TEXT NOT NULL,
  user_id  TEXT NOT NULL,
  PRIMARY KEY (group_id, user_id)
);
CREATE TABLE roles (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT,
  builtin     INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL
);
CREATE TABLE role_pages (
  role_id TEXT NOT NULL,
  page    TEXT NOT NULL,
  access  TEXT NOT NULL,
  PRIMARY KEY (role_id, page)
);
-- id IS A TEXT UUID, matching the live schema: grants.id is TEXT PRIMARY KEY
-- and upsertGrant fills it with crypto.randomUUID(). Every fixture here declared
-- INTEGER PRIMARY KEY AUTOINCREMENT until 2026-08-26, which is not the shape on
-- disk -- GrantRow.ID was int64 and scanned happily against all of them while
-- being unable to read a single real row. The default keeps the INSERTs readable.
-- (No backticks in this comment: it sits inside a Go raw string.)
CREATE TABLE grants (
  id             TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL,
  principal_id   TEXT NOT NULL,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
  role           TEXT,
  scope_type     TEXT NOT NULL,
  scope_id       TEXT,
  created_at     INTEGER NOT NULL,
  created_by     TEXT
);
`

func principalsDB(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	if _, err := h.Exec(principalsDDL); err != nil {
		t.Fatal(err)
	}
	return openTest(t, dir), h
}

// TestBuiltinRolesSortFirst — the seeded roles are what an operator picks from
// most of the time, so they head the list regardless of what a custom role is
// called. A plain name sort would bury them under anything beginning with "A".
func TestBuiltinRolesSortFirst(t *testing.T) {
	d, h := principalsDB(t)
	rows := []struct {
		id, name string
		builtin  int
	}{
		{"r1", "Auditor", 0}, {"r2", "Operator", 1}, {"r3", "administrator", 1}, {"r4", "zebra", 0},
	}
	for i, r := range rows {
		if _, err := h.Exec(`INSERT INTO roles (id,name,description,builtin,created_at) VALUES (?,?,?,?,?)`,
			r.id, r.name, nil, r.builtin, int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.ListRoles()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	want := []string{"administrator", "Operator", "Auditor", "zebra"}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("order = %v, want %v — builtin DESC first, then name COLLATE NOCASE",
				names, want)
		}
	}
	// AND builtin IS A BOOL on this side, converted from the INTEGER column the
	// way `_roleView` does with `!!r.builtin`.
	if !got[0].Builtin || got[3].Builtin {
		t.Errorf("builtin flags = %v/%v", got[0].Builtin, got[3].Builtin)
	}
}

// TestAnEmptyFilterListsEverything — the original builds its WHERE from
// whichever keys are TRUTHY, so a blank principal id is not a filter for the
// empty string. Filtering on it would return nothing and read as "this principal
// has no grants", which is a different claim.
func TestAnEmptyFilterListsEverything(t *testing.T) {
	d, h := principalsDB(t)
	seed := []struct{ ptype, pid, scope string }{
		{"user", "u1", "global"}, {"group", "g1", "router"}, {"user", "u2", "site"},
	}
	for i, s := range seed {
		if _, err := h.Exec(`INSERT INTO grants (principal_type,principal_id,role_id,role,scope_type,scope_id,created_at,created_by)
		                     VALUES (?,?,?,?,?,?,?,?)`,
			s.ptype, s.pid, "role-a", nil, s.scope, nil, int64(i), "admin"); err != nil {
			t.Fatal(err)
		}
	}

	all, err := d.ListGrants(GrantFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("an empty filter returned %d grants, want all 3", len(all))
	}

	byUser, err := d.ListGrants(GrantFilter{PrincipalType: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byUser) != 2 {
		t.Errorf("principalType=user returned %d, want 2", len(byUser))
	}
	both, err := d.ListGrants(GrantFilter{PrincipalType: "user", PrincipalID: "u2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(both) != 1 || both[0].PrincipalID != "u2" {
		t.Errorf("two filters returned %d rows", len(both))
	}
}

// TestNullableGrantColumnsStayNull — `role_id`, `role`, `scope_id` and
// `created_by` are all nullable, and the card distinguishes an absent value from
// an empty one: a grant with no `scope_id` is GLOBAL, and one with `scope_id: ""`
// would render as a scope whose name nobody set.
// TestNullableGrantColumnsStayNull.
//
// ── WHICH COLUMNS ARE ACTUALLY NULLABLE, CHECKED AGAINST THE SCHEMA ─────────
//
// This test used to insert a NULL `role_id` and a NULL `scope_id` and assert
// they came back nil. Neither row can exist: the live schema declares
// `role_id TEXT NOT NULL REFERENCES roles(id)` and
// `scope_id TEXT NOT NULL DEFAULT ”` — a global grant stores an EMPTY STRING,
// because SQLite treats NULLs as distinct in a UNIQUE index and a NULL there
// would let one principal hold two global grants.
//
// It passed because the FIXTURE was looser than the schema, and it stopped
// passing the moment the fixture was corrected (2026-08-26). What it asserts now
// is the two columns that ARE nullable: the legacy `role` name and `created_by`.
func TestNullableGrantColumnsStayNull(t *testing.T) {
	d, h := principalsDB(t)
	if _, err := h.Exec(`INSERT INTO grants (principal_type,principal_id,role_id,role,scope_type,scope_id,created_at,created_by)
	                     VALUES ('user','u1','role-a',NULL,'global','',1,NULL)`); err != nil {
		t.Fatal(err)
	}
	got, err := d.ListGrants(GrantFilter{})
	if err != nil {
		t.Fatal(err)
	}
	g := got[0]
	if g.Role != nil || g.CreatedBy != nil {
		t.Errorf("a NULL column came back non-nil: %+v", g)
	}
	// ...and the NOT NULL ones came back SET, which is what stops this test
	// passing against a reader that returned nil for everything.
	if g.RoleID == nil || *g.RoleID != "role-a" {
		t.Errorf("role_id = %v, want role-a", g.RoleID)
	}
	if g.ScopeID == nil || *g.ScopeID != "" {
		t.Errorf("scope_id = %v, want an empty string (a global grant stores '' not NULL)",
			g.ScopeID)
	}

	// The legacy `role` name column is still selected, because rows written
	// before the role table existed carry it.
	if _, err := h.Exec(`INSERT INTO grants (principal_type,principal_id,role_id,role,scope_type,scope_id,created_at,created_by)
	                     VALUES ('user','u2','role-a','viewer','global','',2,'admin')`); err != nil {
		t.Fatal(err)
	}
	got, _ = d.ListGrants(GrantFilter{PrincipalID: "u2"})
	if got[0].Role == nil || *got[0].Role != "viewer" {
		t.Errorf("the legacy role name was dropped: %+v", got[0])
	}
}

func TestGroupMembersAndCounts(t *testing.T) {
	d, h := principalsDB(t)
	if _, err := h.Exec(`INSERT INTO groups (id,name,description,created_at) VALUES
	  ('g1','Ops',NULL,1), ('g2','empty group','has nobody',2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`INSERT INTO group_members (group_id,user_id) VALUES ('g1','u1'),('g1','u2')`); err != nil {
		t.Fatal(err)
	}
	m, err := d.GroupMembers("g1")
	if err != nil || len(m) != 2 {
		t.Fatalf("members = %v (%v)", m, err)
	}
	// AN EMPTY GROUP IS AN ORDINARY STATE, and must come back as an empty list
	// rather than nil — the card renders `[]` and `null` differently.
	empty, err := d.GroupMembers("g2")
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("an empty group gave %#v, want an empty slice", empty)
	}
	// A group nobody has heard of behaves the same way.
	unknown, _ := d.GroupMembers("nosuchgroup")
	if unknown == nil || len(unknown) != 0 {
		t.Errorf("an unknown group gave %#v", unknown)
	}

	if _, err := h.Exec(`INSERT INTO grants (principal_type,principal_id,role_id,role,scope_type,scope_id,created_at,created_by)
	                     VALUES ('user','u1','role-a',NULL,'global',NULL,1,NULL),
	                            ('group','g1','role-a',NULL,'global',NULL,2,NULL),
	                            ('user','u2','role-b',NULL,'global',NULL,3,NULL)`); err != nil {
		t.Fatal(err)
	}
	n, err := d.CountGrantsForRole("role-a")
	if err != nil || n != 2 {
		t.Errorf("countGrantsForRole = %d (%v), want 2", n, err)
	}
	if n, _ := d.CountGrantsForRole("role-unused"); n != 0 {
		t.Errorf("an unused role counted %d grants", n)
	}
}

func TestRolePagesAreOrderedByPage(t *testing.T) {
	d, h := principalsDB(t)
	for _, p := range []struct{ page, access string }{
		{"wireless", "read"}, {"dashboard", "write"}, {"logs", "read"},
	} {
		if _, err := h.Exec(`INSERT INTO role_pages (role_id,page,access) VALUES ('r1',?,?)`,
			p.page, p.access); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.RolePages("r1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"dashboard", "logs", "wireless"}
	for i := range want {
		if got[i].Page != want[i] {
			t.Fatalf("pages = %+v, want %v in order", got, want)
		}
	}
	if got[0].Access != "write" {
		t.Errorf("access was lost: %+v", got[0])
	}
}

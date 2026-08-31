package db

// The grants table AS THE LIVE MIGRATIONS LEAVE IT (migration 12's `grants_new`,
// renamed): a TEXT uuid primary key and a NOT NULL scope_id.
//
// Every fixture in this repo had declared `id INTEGER PRIMARY KEY AUTOINCREMENT`,
// which is not the shape on disk — so `GrantRow.ID int64` scanned happily in
// tests and could never have scanned a real row.

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

const realGrantsDDL = `
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL,
  builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0);
INSERT INTO roles (id, name) VALUES ('manager','manager'), ('auditor','auditor');
CREATE TABLE grants (
  id             TEXT PRIMARY KEY,
  principal_type TEXT NOT NULL CHECK (principal_type IN ('user','group')),
  principal_id   TEXT NOT NULL,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
  role           TEXT,
  scope_type     TEXT NOT NULL CHECK (scope_type IN ('global','site','router')),
  scope_id       TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  created_by     TEXT,
  CHECK ((scope_type =  'global' AND scope_id =  '')
      OR (scope_type <> 'global' AND scope_id <> '')),
  UNIQUE (principal_type, principal_id, scope_type, scope_id)
);
INSERT INTO grants (id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at)
VALUES ('3f2b1a44-9c7e-4d51-8a0b-1e2c3d4f5a6b','user','u-1','manager','admin','router','r1',1),
       ('7c8d9e00-1122-4334-8556-778899aabbcc','user','u-1','auditor',NULL,'global','',2);
`

func TestGrantsScanAgainstTheRealSchema(t *testing.T) {
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(realGrantsDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()

	d := openTest(t, dir)
	got, err := d.ListGrants(GrantFilter{PrincipalType: "user", PrincipalID: "u-1"})
	if err != nil {
		t.Fatalf("ListGrants against the REAL schema: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d grants, want 2", len(got))
	}
	for _, g := range got {
		// `fmt.Sprint`, not `g.ID == ""`: a direct comparison ties this assertion
		// to the field's TYPE, so reverting it to int64 would fail to COMPILE —
		// and a mutation that does not build is not a kill. Written this way, the
		// revert compiles and is killed by the scan above, which is the defect.
		if fmt.Sprint(g.ID) == "" {
			t.Error("a grant came back with no id")
		}
	}
	// A GLOBAL grant stores '' rather than NULL -- the live schema says why:
	// SQLite treats NULLs as distinct in a UNIQUE index, so a NULL here would
	// let one principal hold two global grants and the constraint never fire.
	var global *GrantRow
	for i := range got {
		if got[i].ScopeType == "global" {
			global = &got[i]
		}
	}
	if global == nil {
		t.Fatal("no global grant came back")
	}
	if global.ScopeID == nil || *global.ScopeID != "" {
		t.Errorf("a global grant's scope_id is %v, want an empty string", global.ScopeID)
	}
}

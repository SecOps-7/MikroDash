package db

import (
	"sort"
	"testing"

	"mikrodash/internal/pages"
)

// toolPages is the four pages the one `tools` page became on 2026-09-27.
var toolPages = []string{"tools-ping", "tools-traceroute", "tools-torch", "tools-btest"}

// Migration 28: the four tool pages' grants, carried across from the Tools
// page's.
//
// ── THE FAILURE THIS EXISTS FOR IS SILENT, AND IT IS A LOSS OF REACH ───────
//
// Ping, traceroute, torch and the bandwidth test were four tabs on one `tools`
// page, so a `tools` grant is the only record of who could run a diagnostic. The
// tabs are four pages now, and a page key is a PERMISSION key, so without this
// migration every role holding `tools` confers nothing at all: an unknown key is
// denied before any role is consulted, and nothing anywhere says so.
//
// `pages.Renamed` cannot carry it. It maps one old key to ONE new key, so it
// would move the grant onto a single tool and take the other three away — which
// is a quieter version of the same loss. Migration 19 is the precedent: a split
// is a COPY.
//
// The access level is carried AS IT STANDS. Torch and the bandwidth test needed
// WRITE on `tools` and need write on their own page, so a role with `tools` read
// keeps ping and traceroute and still cannot torch. Copying every row at write
// would be an escalation that looks exactly like this migration working.
func TestMigrationTwentyEightCarriesToolsGrantsOntoTheFourToolPages(t *testing.T) {
	d := openTest(t, t.TempDir())

	// Wind back to an install that predates the split: a `tools` grant at each
	// access, held by two custom roles, and no tool-page rows.
	for _, stmt := range []string{
		`DELETE FROM role_pages WHERE page LIKE 'tools-%'`,
		// `created_at` IS NOT NULL WITH NO DEFAULT and `OR IGNORE` swallows the
		// failure, so the columns are named — the trap migration 19's test
		// records.
		`INSERT OR IGNORE INTO roles (id, name, description, builtin, created_at)
		 VALUES ('netops', 'Net Ops', 'a role the operator wrote', 0, 1758000000000)`,
		`INSERT OR IGNORE INTO roles (id, name, description, builtin, created_at)
		 VALUES ('helpdesk', 'Help Desk', 'another one', 0, 1758000000000)`,
		`INSERT OR IGNORE INTO role_pages (role_id, page, access) VALUES ('netops', 'tools', 'write')`,
		`INSERT OR IGNORE INTO role_pages (role_id, page, access) VALUES ('helpdesk', 'tools', 'read')`,
		`DELETE FROM schema_version WHERE version >= 28`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// THE CONTROL. Without it everything below would pass against a database
	// that never needed migrating — the wind-back is the whole premise.
	var before int
	if err := d.sql.QueryRow(
		`SELECT count(*) FROM role_pages WHERE page LIKE 'tools-%'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("%d tool-page grant(s) before the migration; the wind-back did not take", before)
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v, _ := d.SchemaVersion(); v < 28 {
		t.Errorf("schema is v%d after Migrate, want at least 28", v)
	}

	for role, want := range map[string]string{"netops": "write", "helpdesk": "read"} {
		for _, page := range toolPages {
			var got string
			err := d.sql.QueryRow(
				`SELECT access FROM role_pages WHERE role_id = ? AND page = ?`, role, page).Scan(&got)
			if err != nil {
				t.Errorf("role %q held tools %q and holds no %s grant: %v — whoever could run "+
					"that diagnostic yesterday silently cannot today", role, want, page, err)
				continue
			}
			if got != want {
				t.Errorf("role %q held tools %q but holds %s %q. The level must be CARRIED, not "+
					"flattened: raising it is an escalation that looks like success",
					role, want, page, got)
			}
		}
	}

	// AND IT IS SAFE TO RUN TWICE, which every statement in that map must be.
	if _, err := d.sql.Exec(`DELETE FROM schema_version WHERE version >= 28`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate a second time: %v", err)
	}
	var dupes int
	if err := d.sql.QueryRow(
		`SELECT count(*) FROM role_pages WHERE role_id = 'netops' AND page LIKE 'tools-%'`).
		Scan(&dupes); err != nil {
		t.Fatal(err)
	}
	if dupes != len(toolPages) {
		t.Errorf("a second run left %d tool-page rows for netops, want %d", dupes, len(toolPages))
	}
}

// ── AND THEN THE STALE ROW GOES ─────────────────────────────────────────────
//
// The migration copies; it deliberately does not delete. `pages.Renamed["tools"]`
// is what sweeps the row naming a page that no longer exists, and
// `RenamePageGrants` runs AFTER the migrations from cmd/mikrodash. The two are
// only correct together: the migration alone leaves a dead row in every upgraded
// database, and the rename alone would move the grant onto `tools-ping` and take
// the other three diagnostics away.
//
// THE COLLISION IS THE POINT and it is worth asserting rather than reasoning
// about. `role_pages` is `PRIMARY KEY (role_id, page)`, so by the time the sweep
// runs, `tools-ping` already exists at the same access: the UPDATE OR IGNORE
// finds the conflict and does nothing, and the DELETE clears the old row. A
// plain UPDATE would fail the whole sweep.
func TestTheStaleToolsGrantIsSweptAndTheFourSurviveIt(t *testing.T) {
	if pages.Renamed["tools"] != "tools-ping" {
		t.Fatalf("pages.Renamed[\"tools\"] = %q; this test is about the sweep that entry performs",
			pages.Renamed["tools"])
	}
	d := openTest(t, t.TempDir())

	for _, stmt := range []string{
		`DELETE FROM role_pages WHERE page LIKE 'tools-%'`,
		`INSERT OR IGNORE INTO roles (id, name, description, builtin, created_at)
		 VALUES ('netops', 'Net Ops', 'a role the operator wrote', 0, 1758000000000)`,
		`INSERT OR IGNORE INTO role_pages (role_id, page, access) VALUES ('netops', 'tools', 'write')`,
		`DELETE FROM schema_version WHERE version >= 28`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// The order cmd/mikrodash uses: migrations at Open, then the sweep.
	if _, err := d.RenamePageGrants(); err != nil {
		t.Fatalf("RenamePageGrants: %v", err)
	}

	rows, err := d.sql.Query(`SELECT page, access FROM role_pages WHERE role_id = 'netops'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var page, access string
		if err := rows.Scan(&page, &access); err != nil {
			t.Fatal(err)
		}
		if page == "tools" {
			t.Error("the `tools` grant survived the sweep: it names no page, so it confers " +
				"nothing and explains nothing to whoever reads the role next")
		}
		if page != "tools" && access != "write" {
			t.Errorf("%s came out at %q; the sweep changed an access level it should only move", page, access)
		}
		got = append(got, page)
	}
	sort.Strings(got)
	want := append([]string(nil), toolPages...)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("netops holds %v, want exactly %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("netops holds %v, want exactly %v", got, want)
		}
	}
}

package db

import "testing"

// Migration 19: the WireGuard page's grants, carried across from the VPN page's.
//
// ── THE FAILURE THIS EXISTS FOR IS SILENT ──────────────────────────────────
//
// The WireGuard peers used to live on the VPN page, so a `vpn` grant is what
// conferred managing them. They moved to their own page on 2026-09-20 and BOTH
// keys stayed live, so `pages.Renamed` cannot express it: a rename MOVES a
// grant, and `TestRenamedNamesNoLivePage` refuses an entry naming a page that
// still exists.
//
// Without migration 19 every install's operators keep a VPN page with no peers
// on it and quietly lose the ability to manage them — and NOTHING FAILS. That is
// the shape of the 2026-09-01 incident, where readonly and operator silently
// lost pages and the only symptom was somebody noticing.
//
// The access level is carried AS IT STANDS rather than flattened. A role that
// could only read still only reads; copying every row at `write` would be an
// escalation that looks exactly like this migration working.
func TestMigrationNineteenCarriesVpnGrantsOntoWireguard(t *testing.T) {
	d := openTest(t, t.TempDir())

	// A fresh database is already at the current version and has been SEEDED
	// with `wireguard`, so it cannot show the migration doing anything. Wind it
	// back to an install that predates the split: no wireguard rows, a CUSTOM
	// role holding a vpn grant, and the version behind.
	for _, stmt := range []string{
		`DELETE FROM role_pages WHERE page = 'wireguard'`,
		// `created_at` IS NOT NULL WITH NO DEFAULT, and `OR IGNORE` swallows the
		// failure — the first draft of this test inserted without it, the role
		// never appeared, and the only symptom was the foreign key on the row
		// below. Named columns, all of them.
		`INSERT OR IGNORE INTO roles (id, name, description, builtin, created_at)
		 VALUES ('netops', 'Net Ops', 'a role the operator wrote', 0, 1758000000000)`,
		`INSERT OR IGNORE INTO role_pages (role_id, page, access) VALUES ('netops', 'vpn', 'write')`,
		`DELETE FROM schema_version WHERE version >= 19`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// THE CONTROL. Without it everything below would pass against a database
	// that never needed migrating — the wind-back is the whole premise.
	var before int
	if err := d.sql.QueryRow(
		`SELECT count(*) FROM role_pages WHERE page = 'wireguard'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("%d wireguard grant(s) before the migration; the wind-back did not take", before)
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v, _ := d.SchemaVersion(); v < 19 {
		t.Errorf("schema is v%d after Migrate, want at least 19", v)
	}

	// Every role that holds vpn now holds wireguard AT THE SAME ACCESS — the
	// custom one included, which is where this differs from migration 17. That
	// one GRANTED something new and was rightly bounded to the builtin roles;
	// this one PRESERVES reach that already existed.
	rows, err := d.sql.Query(`SELECT role_id, access FROM role_pages WHERE page = 'vpn'`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{}
	for rows.Next() {
		var id, access string
		if err := rows.Scan(&id, &access); err != nil {
			t.Fatal(err)
		}
		want[id] = access
	}
	rows.Close()
	if len(want) == 0 {
		t.Fatal("no role holds a vpn grant at all — this test is proving nothing")
	}
	if _, ok := want["netops"]; !ok {
		t.Fatal("the custom role's vpn grant vanished; the wind-back is wrong")
	}

	for id, access := range want {
		var got string
		err := d.sql.QueryRow(
			`SELECT access FROM role_pages WHERE role_id = ? AND page = 'wireguard'`, id).Scan(&got)
		if err != nil {
			t.Errorf("role %q holds vpn %q and no wireguard grant: %v — its operators "+
				"silently lost the peers when those moved pages", id, access, err)
			continue
		}
		if got != access {
			t.Errorf("role %q holds vpn %q but wireguard %q. The level must be CARRIED, "+
				"not flattened: raising it is an escalation that looks like success",
				id, access, got)
		}
	}

	// AND IT IS SAFE TO RUN TWICE, which every statement in that map must be.
	if _, err := d.sql.Exec(`DELETE FROM schema_version WHERE version >= 19`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate a second time: %v", err)
	}
	var dupes int
	if err := d.sql.QueryRow(
		`SELECT count(*) FROM role_pages WHERE page = 'wireguard' AND role_id = 'netops'`).
		Scan(&dupes); err != nil {
		t.Fatal(err)
	}
	if dupes != 1 {
		t.Errorf("a second run left %d wireguard rows for netops, want 1", dupes)
	}
}

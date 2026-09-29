package db

import (
	"strconv"
	"strings"
	"testing"
)

// credProfTableSQL is the stored DDL for everything migration 31 owns.
func credProfTableSQL(t *testing.T, d *DB) string {
	t.Helper()
	rows, err := d.sql.Query(`SELECT name, sql FROM sqlite_master
	    WHERE name LIKE 'cred_%' OR name LIKE 'idx_cred_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		b.WriteString(name + ": " + sql + "\n")
	}
	return b.String()
}

// MIGRATION 31 BUILDS WHAT A FRESH DATABASE IS BORN WITH.
//
// `credProfTablesDDL` is one constant used by both `freshSchemaDDL` and
// `portMigrations[31]` precisely so the two cannot drift — but "cannot" is a
// claim about how the code is written today, and the next person to add a column
// may add it to only one of them. Same shape as
// TestMigrationTwentyNineBuildsWhatAFreshDatabaseHas.
func TestMigrationThirtyOneBuildsWhatAFreshDatabaseHas(t *testing.T) {
	d := openTest(t, t.TempDir())
	fresh := credProfTableSQL(t, d)
	// NAMED RATHER THAN COUNTED: "not empty" would pass on a migration that
	// created one of the two tables.
	for _, want := range []string{"cred_profiles", "cred_profile_links",
		"idx_cred_links_router", "idx_cred_links_due"} {
		if !strings.Contains(fresh, want) {
			t.Fatalf("a fresh database has no %s:\n%s", want, fresh)
		}
	}

	// Wind back to an install from before credential profiles. The link table
	// goes first: its foreign key is the point of a later test.
	cfgExec(t, d,
		`DROP INDEX idx_cred_links_due`,
		`DROP INDEX idx_cred_links_router`,
		`DROP TABLE cred_profile_sites`,
		`DROP TABLE cred_profile_links`,
		`DROP TABLE cred_profiles`,
		`DELETE FROM schema_version WHERE version >= 31`)
	if got := credProfTableSQL(t, d); got != "" {
		t.Fatalf("the wind-back left %s", got)
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := credProfTableSQL(t, d); got != fresh {
		t.Errorf("migrated:\n%s\nfresh:\n%s", got, fresh)
	}
}

// TestZTPDevicesCarriesItsCredentialProfilesColumn.
//
// ── THE HALF OF MIGRATION 31 THAT IS AN ALTER, NOT A CREATE ────────────────
//
// `credProfTablesDDL` is shared between the fresh path and the migration, so
// the tables cannot drift. The ZTP column is NOT: `ztpTablesDDL` builds it on a
// fresh database and an `ALTER TABLE` adds it to an existing one, which is two
// descriptions of one column. That shape is exactly how migration 30 was first
// written against a table called `backups` that has always been
// `config_backups` — passing on every fresh install and failing at startup on
// every real one.
//
// So this asserts the column exists AND takes a value, on a database that got
// it the migration's way.
func TestZTPDevicesCarriesItsCredentialProfilesColumn(t *testing.T) {
	d := openTest(t, t.TempDir())

	var fresh string
	if err := d.sql.QueryRow(
		`SELECT sql FROM sqlite_master WHERE name = 'ztp_devices'`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fresh, "cred_profiles") {
		t.Fatalf("a fresh ztp_devices has no cred_profiles column:\n%s", fresh)
	}

	// An install from before 31: rebuild the table without the column, which is
	// the only way SQLite lets one be taken off on the versions this runs on.
	cfgExec(t, d,
		`ALTER TABLE ztp_devices RENAME TO ztp_devices_old`,
		`CREATE TABLE ztp_devices (
		   id TEXT PRIMARY KEY, mode TEXT NOT NULL, state TEXT NOT NULL,
		   label TEXT NOT NULL DEFAULT '', serial TEXT NOT NULL DEFAULT '',
		   token_hash TEXT UNIQUE, batch_id TEXT, expires_at INTEGER,
		   tunnel_ip TEXT UNIQUE, peer_key TEXT UNIQUE, lan_from TEXT,
		   secret TEXT, template_id TEXT,
		   values_json TEXT NOT NULL DEFAULT '{}',
		   acked_json TEXT NOT NULL DEFAULT '[]',
		   site_ids TEXT NOT NULL DEFAULT '[]',
		   router_id TEXT, facts_json TEXT NOT NULL DEFAULT '{}', run_id TEXT,
		   created_by TEXT NOT NULL, created_at INTEGER NOT NULL,
		   first_seen INTEGER, last_seen INTEGER, error TEXT)`,
		`DROP TABLE ztp_devices_old`,
		`DELETE FROM schema_version WHERE version >= 31`)

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// The column is there, defaults to an empty list, and holds one.
	now := strconv.FormatInt(1759000000000, 10)
	cfgExec(t, d, `INSERT INTO ztp_devices (id, mode, state, created_by, created_at)
	    VALUES ('d1', 'token', 'waiting', 'u-1', `+now+`)`)
	var got string
	if err := d.sql.QueryRow(
		`SELECT cred_profiles FROM ztp_devices WHERE id = 'd1'`).Scan(&got); err != nil {
		t.Fatalf("reading cred_profiles back: %v", err)
	}
	if got != "[]" {
		t.Errorf("a device with no profiles reads %q, want []", got)
	}
	cfgExec(t, d, `UPDATE ztp_devices SET cred_profiles = '["p1","p2"]' WHERE id = 'd1'`)
	if err := d.sql.QueryRow(
		`SELECT cred_profiles FROM ztp_devices WHERE id = 'd1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != `["p1","p2"]` {
		t.Errorf("cred_profiles reads back as %q", got)
	}
}

// TestDeletingAProfileWithLinksIsRefused.
//
// ── RESTRICT, NOT CASCADE, AND IT IS ONE WORD ──────────────────────────────
//
// A link row is MikroDash's only record that it put an account on a router. If
// deleting a profile cascaded them away, the accounts would stay on the devices
// and nothing here would know they exist — an orphaned login at whatever
// privilege the profile carried, invisible for ever.
//
// So the delete fails while any link survives. The operator unlinks first,
// which takes the accounts off, or uses the explicit Forget action, which
// audits every device it abandons. Neither is silent, which is the whole point.
func TestDeletingAProfileWithLinksIsRefused(t *testing.T) {
	d := openTest(t, t.TempDir())
	now := strconv.FormatInt(1759000000000, 10)

	cfgExec(t, d,
		`INSERT INTO cred_profiles (id, name, ros_username, group_name,
	    secret, created_by, created_at, updated_at)
		 VALUES ('cp1', 'NOC Read-only', 'noc', 'grp-noc', 'sealed', 'u-1', `+now+`, `+now+`)`,
		`INSERT INTO cred_profile_links (profile_id, router_id, linked_by, linked_at)
		 VALUES ('cp1', 'r-1', 'u-1', `+now+`)`)

	if _, err := d.sql.Exec(`DELETE FROM cred_profiles WHERE id = 'cp1'`); err == nil {
		t.Error("a profile with a live link was deleted; the accounts it created would " +
			"have stayed on their routers with nothing left that knows about them")
	}

	// THE CONTROL: with the link gone the profile deletes, so the refusal above
	// is about the reference and not about profiles having become undeletable.
	cfgExec(t, d, `DELETE FROM cred_profile_links WHERE profile_id = 'cp1'`)
	if _, err := d.sql.Exec(`DELETE FROM cred_profiles WHERE id = 'cp1'`); err != nil {
		t.Errorf("an unlinked profile could not be deleted: %v", err)
	}
}

// TestOneProfileCannotBeLinkedToARouterTwice: the composite primary key. Two
// rows for one pair would each carry their own state, and the reconciler would
// apply the profile twice per sweep and disagree with itself about the result.
func TestOneProfileCannotBeLinkedToARouterTwice(t *testing.T) {
	d := openTest(t, t.TempDir())
	now := strconv.FormatInt(1759000000000, 10)
	cfgExec(t, d,
		`INSERT INTO cred_profiles (id, name, ros_username, group_name,
	    secret, created_by, created_at, updated_at)
		 VALUES ('cp1', 'NOC', 'noc', 'grp-noc', 'sealed', 'u-1', `+now+`, `+now+`)`,
		`INSERT INTO cred_profile_links (profile_id, router_id, linked_by, linked_at)
		 VALUES ('cp1', 'r-1', 'u-1', `+now+`)`)

	if _, err := d.sql.Exec(`INSERT INTO cred_profile_links (profile_id, router_id, linked_by, linked_at)
	    VALUES ('cp1', 'r-1', 'u-1', ` + now + `)`); err == nil {
		t.Error("one profile was linked to one router twice")
	}
	// THE CONTROL: the same profile on a DIFFERENT router is the ordinary case.
	if _, err := d.sql.Exec(`INSERT INTO cred_profile_links (profile_id, router_id, linked_by, linked_at)
	    VALUES ('cp1', 'r-2', 'u-1', ` + now + `)`); err != nil {
		t.Errorf("linking a profile to a second router was refused: %v", err)
	}
}

// TestTwoProfilesCannotClaimOneRouterOSUsername.
//
// Two profiles both creating `noc` on one router is a pair of writers with no
// tiebreak: whichever applied last would own the password, per device, and the
// other would read as permanently drifted. The ownership marker in the row's
// comment names ONE profile id, so the conflict is not even expressible on the
// device — it has to be refused before it gets there.
func TestTwoProfilesCannotClaimOneRouterOSUsername(t *testing.T) {
	d := openTest(t, t.TempDir())
	now := strconv.FormatInt(1759000000000, 10)
	cfgExec(t, d, `INSERT INTO cred_profiles (id, name, ros_username, group_name,
	    secret, created_by, created_at, updated_at)
	  VALUES ('cp1', 'NOC Read', 'noc', 'grp-noc', 'sealed', 'u-1', `+now+`, `+now+`)`)

	if _, err := d.sql.Exec(`INSERT INTO cred_profiles (id, name, ros_username, group_name,
	    secret, created_by, created_at, updated_at)
	  VALUES ('cp2', 'NOC Write', 'noc', 'grp-nocw', 'sealed', 'u-1', ` + now + `, ` + now + `)`); err == nil {
		t.Error("two profiles claimed the RouterOS username 'noc'")
	}
	// THE CONTROL: a different username is fine, so the refusal is about the
	// collision rather than about the second insert being malformed.
	if _, err := d.sql.Exec(`INSERT INTO cred_profiles (id, name, ros_username, group_name,
	    secret, created_by, created_at, updated_at)
	  VALUES ('cp2', 'NOC Write', 'nocw', 'grp-nocw', 'sealed', 'u-1', ` + now + `, ` + now + `)`); err != nil {
		t.Errorf("a second profile with its own username was refused: %v", err)
	}
}

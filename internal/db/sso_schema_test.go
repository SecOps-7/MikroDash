package db

import (
	"strconv"
	"strings"
	"testing"
)

// ssoTableSQL is the stored DDL for everything migration 29 owns.
func ssoTableSQL(t *testing.T, d *DB) string {
	t.Helper()
	rows, err := d.sql.Query(`SELECT name, sql FROM sqlite_master
	    WHERE name LIKE 'sso_%' OR name = 'idx_sso_identities_user'
	    ORDER BY name`)
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

// MIGRATION 29 BUILDS WHAT A FRESH DATABASE IS BORN WITH.
//
// `ssoTablesDDL` is one constant used by both `freshSchemaDDL` and
// `portMigrations[29]` precisely so the two cannot drift — but "cannot" is a
// claim about how the code is written today, and the next person to add a column
// may add it to only one of them. This makes the claim checkable, and it is the
// same shape as TestMigrationTwentyFourBuildsWhatAFreshDatabaseHas.
func TestMigrationTwentyNineBuildsWhatAFreshDatabaseHas(t *testing.T) {
	d := openTest(t, t.TempDir())
	fresh := ssoTableSQL(t, d)
	// THREE TABLES AND AN INDEX, NAMED RATHER THAN COUNTED: a test that only
	// asserted "not empty" would pass on a migration that created one of them.
	for _, want := range []string{"sso_providers", "sso_role_map", "sso_identities",
		"idx_sso_identities_user"} {
		if !strings.Contains(fresh, want) {
			t.Fatalf("a fresh database has no %s:\n%s", want, fresh)
		}
	}

	// Wind back to an install from before SSO. Children first: the foreign keys
	// below are the point of the next test.
	cfgExec(t, d,
		`DROP INDEX idx_sso_identities_user`,
		`DROP TABLE sso_identities`,
		`DROP TABLE sso_role_map`,
		`DROP TABLE sso_providers`,
		`DELETE FROM schema_version WHERE version >= 29`)
	if got := ssoTableSQL(t, d); got != "" {
		t.Fatalf("the wind-back left %s", got)
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := ssoTableSQL(t, d); got != fresh {
		t.Errorf("migrated:\n%s\nfresh:\n%s", got, fresh)
	}
}

// ── THE TWO FOREIGN KEYS THAT CARRY A DECISION ──────────────────────────────
//
// A mapping's `role_id` is RESTRICT and a provider's children are CASCADE, and
// the difference is deliberate rather than incidental:
//
//   - Deleting a PROVIDER should take its mappings and identities with it. They
//     describe that provider and mean nothing without it.
//   - Deleting a ROLE a mapping still names must FAIL. Cascading would be
//     silent, and the effect would surface later as somebody else's sign-in
//     refused for "no role matched" — a lockout with no visible cause.
//
// Both are one word in the DDL, neither shows up in a round-trip test, and the
// wrong one is invisible until the day it matters.
func TestSSOForeignKeysCascadeAProviderAndRefuseARoleInUse(t *testing.T) {
	d := openTest(t, t.TempDir())
	now := strconv.FormatInt(1759000000000, 10)

	cfgExec(t, d,
		`INSERT INTO sso_providers (id, name, enabled, issuer, client_id, client_secret,
		   created_at, updated_at)
		 VALUES ('p1', 'Entra', 1, 'https://issuer.example', 'cid', 'sealed', `+now+`, `+now+`)`,
		`INSERT INTO sso_role_map (provider_id, claim_value, role_id)
		 VALUES ('p1', 'netops', 'operator')`,
		`INSERT INTO sso_identities (provider_id, subject, user_id, created_at, last_seen_at)
		 VALUES ('p1', 'sub-1', 'u-1', `+now+`, `+now+`)`)

	// A ROLE IN USE CANNOT BE DELETED.
	if _, err := d.sql.Exec(`DELETE FROM roles WHERE id = 'operator'`); err == nil {
		t.Error("deleting a role a mapping still names was allowed; the mapping would have gone " +
			"silently and taken somebody's access with it at their next sign-in")
	}

	// THE CONTROL: a role nothing maps to still deletes, so the check above is
	// about the reference and not about roles having become undeletable.
	cfgExec(t, d, `INSERT INTO roles (id, name, builtin, created_at)
	    VALUES ('spare', 'Spare', 0, `+now+`)`)
	if _, err := d.sql.Exec(`DELETE FROM roles WHERE id = 'spare'`); err != nil {
		t.Errorf("an unmapped role could not be deleted: %v", err)
	}

	// DELETING THE PROVIDER TAKES ITS CHILDREN.
	cfgExec(t, d, `DELETE FROM sso_providers WHERE id = 'p1'`)
	for _, q := range []struct{ what, query string }{
		{"role mappings", `SELECT COUNT(*) FROM sso_role_map`},
		{"identities", `SELECT COUNT(*) FROM sso_identities`},
	} {
		var n int
		if err := d.sql.QueryRow(q.query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%d %s survived their provider; they describe a provider that is gone",
				n, q.what)
		}
	}
}

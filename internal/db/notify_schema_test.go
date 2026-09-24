package db

import (
	"strings"
	"testing"
)

// notifyTableSQL is the stored DDL for everything migration 24 owns.
func notifyTableSQL(t *testing.T, d *DB) string {
	t.Helper()
	rows, err := d.sql.Query(`SELECT name, sql FROM sqlite_master
	    WHERE name LIKE 'notify_%' OR name = 'idx_notify_channels_owner'
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

// MIGRATION 24 BUILDS WHAT A FRESH DATABASE IS BORN WITH.
//
// `notifyTablesDDL` is one constant used by both `freshSchemaDDL` and
// `portMigrations[24]` precisely so the two cannot drift — but "cannot" is a
// claim about how the code is written today, and the next person to add a column
// may add it to only one of them. This makes the claim checkable, and it is the
// same shape as TestMigrationTwentyBuildsWhatAFreshDatabaseHas.
func TestMigrationTwentyFourBuildsWhatAFreshDatabaseHas(t *testing.T) {
	d := openTest(t, t.TempDir())
	fresh := notifyTableSQL(t, d)
	if !strings.Contains(fresh, "CREATE TABLE") {
		t.Fatalf("a fresh database has no notification channel table:\n%s", fresh)
	}
	if !strings.Contains(fresh, "idx_notify_channels_owner") {
		t.Fatalf("the owner index is missing from a fresh database:\n%s", fresh)
	}

	// Wind back to an install from before notification channels.
	cfgExec(t, d,
		`DROP INDEX idx_notify_channels_owner`,
		`DROP TABLE notify_channels`,
		`DELETE FROM schema_version WHERE version >= 24`)
	if got := notifyTableSQL(t, d); got != "" {
		t.Fatalf("the wind-back left %s", got)
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := notifyTableSQL(t, d); got != fresh {
		t.Errorf("migrated:\n%s\nfresh:\n%s", got, fresh)
	}

	// AND A SECOND RUN CHANGES NOTHING, which every port migration must allow:
	// a database created by `freshSchemaDDL` already holds this table and is
	// stamped at the current version, so the two paths meet here.
	cfgExec(t, d, `DELETE FROM schema_version WHERE version >= 24`)
	if _, err := d.Migrate(); err != nil {
		t.Errorf("running migration 24 twice: %v", err)
	}
	if got := notifyTableSQL(t, d); got != fresh {
		t.Errorf("a second run changed the schema:\n%s\nfresh:\n%s", got, fresh)
	}
}

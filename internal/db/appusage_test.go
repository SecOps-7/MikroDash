package db

import "testing"

// tableExists reports whether the database has a table of that name.
func tableExists(t *testing.T, d *DB, name string) bool {
	t.Helper()
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// Migration 38: the Apps Usage history's table, on a database that predates
// it. Wound back to v37, migrated, and then written and read through the real
// calls.
func TestMigrationThirtyEightAddsTheAppUsageTable(t *testing.T) {
	d := openTest(t, t.TempDir())
	for _, stmt := range []string{
		`DROP TABLE app_usage`,
		`DELETE FROM schema_version WHERE version >= 38`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// THE CONTROL: the wind-back took, so the calls below fail without the
	// migration.
	if _, err := d.AppUsage("r1", 0, 1); err == nil {
		t.Fatal("app_usage still answers after it was dropped; the wind-back did not take")
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v, _ := d.SchemaVersion(); v != 39 {
		t.Errorf("schema is v%d after Migrate, want 39", v)
	}

	// Bytes ADD to the hour they belong to, per router and per app.
	const h = int64(3_600_000)
	for _, batch := range [][]AppUsageRow{
		{{RouterID: "r1", App: "netflix", Hour: h, RxBytes: 100, TxBytes: 10},
			{RouterID: "r1", App: "teams", Hour: h, RxBytes: 0, TxBytes: 0}},
		{{RouterID: "r1", App: "netflix", Hour: h, RxBytes: 50, TxBytes: 5},
			{RouterID: "r2", App: "netflix", Hour: h, RxBytes: 7, TxBytes: 7},
			{RouterID: "r1", App: "netflix", Hour: 2 * h, RxBytes: 1, TxBytes: 0}},
	} {
		if err := d.AddAppUsage(batch); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := d.AppUsage("r1", h, 2*h)
	if err != nil {
		t.Fatal(err)
	}
	want := []AppUsageRow{
		{RouterID: "r1", App: "netflix", Hour: h, RxBytes: 150, TxBytes: 15},
		{RouterID: "r1", App: "netflix", Hour: 2 * h, RxBytes: 1},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v, want %+v (an empty row is not written, another router's is not read)", rows, want)
	}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], want[i])
		}
	}
	if rows, _ := d.AppUsage("r1", 2*h+1, 3*h); len(rows) != 0 {
		t.Errorf("an hour outside the range was read: %+v", rows)
	}
}

// Migration 39: the per-router opt-in table goes. The operator's database is at
// v38 with it, holding the routers someone pressed "Record this router" for;
// every router is recorded now while the feature is on. A new database never
// has it.
func TestMigrationThirtyNineDropsThePerRouterOptIn(t *testing.T) {
	d := openTest(t, t.TempDir())
	if tableExists(t, d, "app_history_routers") {
		t.Fatal("a new database has app_history_routers; the opt-in it held is gone")
	}
	// Wound back to v38 as migration 38 left it, with a router switched on.
	for _, stmt := range []string{
		`CREATE TABLE app_history_routers (router_id TEXT PRIMARY KEY)`,
		`INSERT INTO app_history_routers (router_id) VALUES ('r1')`,
		`DELETE FROM schema_version WHERE version >= 39`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if !tableExists(t, d, "app_history_routers") {
		t.Fatal("the control did not take: no table to drop")
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if tableExists(t, d, "app_history_routers") {
		t.Error("app_history_routers survived migration 39")
	}
	if v, _ := d.SchemaVersion(); v != 39 {
		t.Errorf("schema is v%d after Migrate, want 39", v)
	}
	// Replayed over a database that no longer has it: harmless.
	if _, err := d.sql.Exec(appHistoryRoutersDropDDL); err != nil {
		t.Errorf("migration 39 is not safe to run twice: %v", err)
	}
}

package db

import "testing"

// Migration 38: the Apps Usage history's two tables, on a database that
// predates them. Wound back to v37 the way the operator's /data is, migrated,
// and then written and read through the real calls.
func TestMigrationThirtyEightAddsTheAppUsageTables(t *testing.T) {
	d := openTest(t, t.TempDir())
	for _, stmt := range []string{
		`DROP TABLE app_usage`,
		`DROP TABLE app_history_routers`,
		`DELETE FROM schema_version WHERE version >= 38`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// THE CONTROL: the wind-back took, so the calls below fail without the
	// migration.
	if _, err := d.AppHistoryRouters(); err == nil {
		t.Fatal("app_history_routers still answers after it was dropped; the wind-back did not take")
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v, _ := d.SchemaVersion(); v != 38 {
		t.Errorf("schema is v%d after Migrate, want 38", v)
	}

	if err := d.SetAppHistory("r1", true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetAppHistory("r1", true); err != nil {
		t.Fatalf("switching on twice: %v", err)
	}
	if on, err := d.AppHistoryRouters(); err != nil || !on["r1"] || on["r2"] {
		t.Fatalf("recorded = %v, %v; want r1 only", on, err)
	}
	if err := d.SetAppHistory("r1", false); err != nil {
		t.Fatal(err)
	}
	if on, _ := d.AppHistoryRouters(); on["r1"] {
		t.Errorf("recorded = %v after switching off", on)
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

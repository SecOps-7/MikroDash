package db

import (
	"strings"
	"testing"
)

// monitorRunsSQL is the stored DDL for everything migration 34 owns.
func monitorRunsSQL(t *testing.T, d *DB) string {
	t.Helper()
	rows, err := d.sql.Query(`SELECT name, sql FROM sqlite_master
	    WHERE name = 'monitor_runs' OR name = 'idx_monitor_runs_router'
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

// MIGRATION 34 BUILDS WHAT A FRESH DATABASE IS BORN WITH, and it can run twice.
// The same shape as TestMigrationTwentyNineBuildsWhatAFreshDatabaseHas: one
// constant serves both paths, and this is what makes "cannot drift" checkable.
func TestMigrationThirtyFourBuildsWhatAFreshDatabaseHas(t *testing.T) {
	d := openTest(t, t.TempDir())
	fresh := monitorRunsSQL(t, d)
	for _, want := range []string{"monitor_runs", "idx_monitor_runs_router"} {
		if !strings.Contains(fresh, want) {
			t.Fatalf("a fresh database has no %s:\n%s", want, fresh)
		}
	}

	cfgExec(t, d,
		`DROP INDEX idx_monitor_runs_router`,
		`DROP TABLE monitor_runs`,
		`DELETE FROM schema_version WHERE version >= 34`)
	if got := monitorRunsSQL(t, d); got != "" {
		t.Fatalf("the wind-back left %s", got)
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := monitorRunsSQL(t, d); got != fresh {
		t.Errorf("migrated:\n%s\nfresh:\n%s", got, fresh)
	}

	// SAFE TO RUN TWICE, the rule every migration statement here follows.
	cfgExec(t, d, `DELETE FROM schema_version WHERE version >= 34`)
	if _, err := d.Migrate(); err != nil {
		t.Errorf("migration 34 failed on a database that already has the table: %v", err)
	}
}

// RUNS ARE READ BY OVERLAP, and touched as a set.
//
// The overlap rule is the one that matters: a run that began before the window
// and is still going covers the window's START, and a containment query would
// drop it and draw that start grey on a router that was watched throughout.
func TestMonitorRunsAreReadByOverlap(t *testing.T) {
	d := openTest(t, t.TempDir())
	a, err := d.OpenMonitorRun("r1", 1_000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := d.OpenMonitorRun("r1", 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.OpenMonitorRun("r2", 1_000); err != nil {
		t.Fatal(err)
	}
	if err := d.TouchMonitorRuns([]int64{a}, 5_000); err != nil {
		t.Fatal(err)
	}
	if err := d.TouchMonitorRuns([]int64{b}, 20_000); err != nil {
		t.Fatal(err)
	}

	got, err := d.MonitorRunsIn(4_000, 12_000)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(got["r1"]); n != 2 {
		t.Errorf("r1 has %d run(s) overlapping [4000,12000], want 2: the first began "+
			"before the window and the second ends after it", n)
	}
	if len(got["r2"]) != 0 {
		t.Errorf("r2's run [1000,1000] does not overlap the window and was returned: %+v", got["r2"])
	}
	if r := got["r1"][0]; r.StartedAt != 1_000 || r.LastSeenAt != 5_000 {
		t.Errorf("the touched run reads %+v, want started 1000, last seen 5000", r)
	}

	// THE CONTROL: a window between the two runs touches neither.
	if gap, _ := d.MonitorRunsIn(6_000, 9_000); len(gap["r1"]) != 0 {
		t.Errorf("a window between the runs returned %+v", gap["r1"])
	}
}

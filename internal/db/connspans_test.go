package db

import (
	"reflect"
	"testing"
)

func seedConn(t *testing.T, d *DB, rows ...any) {
	t.Helper()
	for i := 0; i+2 < len(rows)+1; i += 3 {
		if _, err := d.sql.Exec(`INSERT INTO connectivity_events (router_id, connected, ts) VALUES (?, ?, ?)`,
			rows[i], rows[i+1], rows[i+2]); err != nil {
			t.Fatal(err)
		}
	}
}

// TestConnStatesAtTakesTheNewestRowAtOrBefore — the "state before the window" a
// strip starts from. It leans on SQLite's bare-column-with-MAX rule, which is a
// SQLite guarantee and not SQL's, so it is pinned here rather than trusted.
func TestConnStatesAtTakesTheNewestRowAtOrBefore(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedConn(t, d, "r1", 1, 10, "r1", 0, 20, "r1", 1, 30, "r2", 0, 5)

	got, err := d.ConnStatesAt(25)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"r1": false, "r2": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("states at 25 = %v, want %v: r1's newest row at or before 25 is the DOWN at 20", got, want)
	}
	// INCLUSIVE of t: a row exactly at the window's start is that start's state.
	if got, _ := d.ConnStatesAt(30); !got["r1"] {
		t.Error("the row AT t was not counted; a window starting on a transition starts in the old state")
	}
	// THE CONTROL: before any row, a router has no state - absent, not false.
	if got, _ := d.ConnStatesAt(4); len(got) != 0 {
		t.Errorf("states before every row = %v, want none: unknown is not down", got)
	}
}

// TestConnEventsInDoesNotCountTheAnchorTwice — strictly after `from`, so the row
// `ConnStatesAt(from)` already returned is not also a transition inside.
func TestConnEventsInDoesNotCountTheAnchorTwice(t *testing.T) {
	d := openTest(t, t.TempDir())
	seedConn(t, d, "r1", 1, 10, "r1", 0, 20, "r1", 1, 30)
	got, err := d.ConnEventsIn(10, 30)
	if err != nil {
		t.Fatal(err)
	}
	var ts []int64
	for _, e := range got["r1"] {
		ts = append(ts, e.TS)
	}
	if !reflect.DeepEqual(ts, []int64{20, 30}) {
		t.Errorf("events in (10,30] = %v, want [20 30]: 10 is the anchor, 30 is inside", ts)
	}
}

// TestBackupOverviewSeparatesTheLastRunFromTheLastSuccess — the newest run can
// have FAILED, and then "when could I last have restored this?" is a different,
// older answer. Showing only the newest run hides exactly that.
func TestBackupOverviewSeparatesTheLastRunFromTheLastSuccess(t *testing.T) {
	d := openTest(t, t.TempDir())
	for _, r := range []struct {
		router, outcome string
		at              int64
	}{
		{"r1", "changed", 100}, {"r1", "failed", 200},
		{"r2", "skipped", 50},
	} {
		if _, err := d.sql.Exec(`INSERT INTO config_backups
		    (router_id, taken_at, outcome, source, rsc_bytes, backup_bytes, ms)
		    VALUES (?, ?, ?, 'schedule', 0, 0, 0)`, r.router, r.at, r.outcome); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.BackupOverview()
	if err != nil {
		t.Fatal(err)
	}
	r1 := got["r1"]
	if r1.LastAt != 200 || r1.LastOutcome != "failed" {
		t.Errorf("r1's last run = %d/%s, want 200/failed", r1.LastAt, r1.LastOutcome)
	}
	if r1.LastSuccessAt == nil || *r1.LastSuccessAt != 100 {
		t.Errorf("r1's last success = %v, want 100: the failed run is not a restore point", r1.LastSuccessAt)
	}
	if got["r2"].LastSuccessAt != nil {
		t.Errorf("r2 only ever SKIPPED, yet has a last success of %v", *got["r2"].LastSuccessAt)
	}
	if _, have := got["r3"]; have {
		t.Error("a router with no backup runs has an entry")
	}
}

package backups

import (
	"testing"

	"mikrodash/internal/db"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }
func i64p(n int64) *int64 { return &n }

// TestStoredZeroSurvivesTheDefault is the three-way rule.
//
// `keepCount: 0` means NO LIMIT, which is a real choice an operator can make.
// A port using `||` or a plain int would replace it with 30 and start deleting
// restore points the operator asked to keep — silently, because a retention
// sweep that removes the right number of files looks like it is working.
func TestStoredZeroSurvivesTheDefault(t *testing.T) {
	s := SettingsFrom(&Backup{Enabled: true, Schedule: "daily"}, ip(0), ip(0), "")
	if s.KeepCount != 0 || s.KeepDays != 0 {
		t.Errorf("keepCount=%d keepDays=%d; a stored 0 means NO LIMIT and must survive",
			s.KeepCount, s.KeepDays)
	}

	// Absent takes the default.
	s = SettingsFrom(&Backup{Enabled: true, Schedule: "daily"}, nil, nil, "")
	if s.KeepCount != DefaultKeepCount || s.KeepDays != DefaultKeepDays {
		t.Errorf("absent limits = %d/%d, want %d/%d",
			s.KeepCount, s.KeepDays, DefaultKeepCount, DefaultKeepDays)
	}
}

// TestTimeIsThreeWayHere Too — absent takes 08:00, an explicit "" is "any time".
// Same distinction due.go rests on; the card has to show it correctly or an
// operator clearing the field sees it reappear.
func TestTimeIsThreeWayHereToo(t *testing.T) {
	if got := SettingsFrom(&Backup{}, nil, nil, "").Time; got != DefaultTime {
		t.Errorf("absent time = %q, want %q", got, DefaultTime)
	}
	empty := ""
	if got := SettingsFrom(&Backup{Time: &empty}, nil, nil, "").Time; got != "" {
		t.Errorf("explicit empty time = %q, want \"\" — otherwise clearing the "+
			"field reads back as unset and the default reappears", got)
	}
	at := "02:00"
	if got := SettingsFrom(&Backup{Time: &at}, nil, nil, "").Time; got != "02:00" {
		t.Errorf("time = %q", got)
	}
}

func TestSettingsFallBackForARouterWithNoBackupBlock(t *testing.T) {
	s := SettingsFrom(nil, nil, nil, "Europe/Berlin")
	if s.Enabled {
		t.Error("a router with no backup block must not read as enabled")
	}
	if s.Schedule != DefaultSchedule || s.Time != DefaultTime {
		t.Errorf("settings = %+v", s)
	}
	if s.Timezone != "Europe/Berlin" {
		t.Error("the display zone is what tells the card which clock 02:00 means")
	}
}

// TestRowBytesAreThePair — the Size column shows both halves together, so a port
// reporting only the binary would understate every row.
func TestRowBytesAreThePair(t *testing.T) {
	rows := RowsFrom([]db.BackupRow{
		{ID: 1, RscBytes: 1000, BackupBytes: 4000, Outcome: "changed"},
		{ID: 2, RscBytes: 0, BackupBytes: 0, Outcome: "unchanged"},
		{ID: 3, RscBytes: 2000, BackupBytes: 8000, Outcome: "changed", PrunedAt: i64p(123)},
	})
	if rows[0].Bytes != 5000 {
		t.Errorf("bytes = %d, want 5000 (both halves)", rows[0].Bytes)
	}
	if rows[1].Bytes != 0 {
		t.Errorf("a run that stored nothing reported %d bytes", rows[1].Bytes)
	}
	if !rows[2].Pruned {
		t.Error("a pruned row must say so — the History table explains the disappearance")
	}
	if rows[0].Pruned {
		t.Error("a live row was marked pruned")
	}
}

func TestRowsFromEmptyIsEmptyNotNil(t *testing.T) {
	if rows := RowsFrom(nil); rows == nil || len(rows) != 0 {
		t.Errorf("got %v; an empty history must serialise as [] not null", rows)
	}
}

// TestARowOnAnotherRouterIsNotFound — the two answers are the same from outside,
// and distinguishing them would confirm the id exists.
func TestARowOnAnotherRouterIsNotFound(t *testing.T) {
	row := &db.BackupRow{ID: 7, RouterID: "router-a", Stem: sp("2026-01-01T000000")}
	if !RowBelongsTo(row, "router-a") {
		t.Error("the row's own router was refused")
	}
	if RowBelongsTo(row, "router-b") {
		t.Error("a row belonging to another router was accepted — naming a router " +
			"you may write must never reach a record belonging to a different one")
	}
	if RowBelongsTo(nil, "router-a") {
		t.Error("a missing row was accepted")
	}
}

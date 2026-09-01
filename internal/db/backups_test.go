package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// The differential gate for the backup history reads.
//
// The corpus is built by the backup-history corpus, which SEEDS a
// throwaway database through the live `recordBackup` and `markBackupPruned` and
// records what the live queries then answer. This test rebuilds the same rows
// and must produce the same numbers.
//
// The edges it exists for are the ones a healthy fleet rarely produces: a run
// that stored nothing, a pair retention has pruned, and a router with no history
// at all — where SUM is NULL rather than 0 in SQLite.

type bkCases struct {
	Rows []struct {
		RouterID    string  `json:"routerId"`
		TakenAt     int64   `json:"takenAt"`
		Outcome     string  `json:"outcome"`
		Source      string  `json:"source"`
		Actor       *string `json:"actor"`
		Stem        *string `json:"stem"`
		Dir         *string `json:"dir"`
		Fingerprint *string `json:"fingerprint"`
		RscBytes    int64   `json:"rscBytes"`
		BackupBytes int64   `json:"backupBytes"`
		MS          int64   `json:"ms"`
		Error       *string `json:"error"`
		Identity    *struct {
			Model     string `json:"model"`
			Serial    string `json:"serial"`
			OSVersion string `json:"osVersion"`
		} `json:"identity"`
	} `json:"rows"`
	PrunedIndex int   `json:"prunedIndex"`
	PrunedAt    int64 `json:"prunedAt"`
	Answers     struct {
		ListAll       []BackupRow   `json:"listAll"`
		ListLimited   []BackupRow   `json:"listLimited"`
		ListZeroLimit []BackupRow   `json:"listZeroLimit"`
		ListOther     []BackupRow   `json:"listOther"`
		ListEmpty     []BackupRow   `json:"listEmpty"`
		Stored        []BackupRow   `json:"stored"`
		StoredEmpty   []BackupRow   `json:"storedEmpty"`
		LastRun       int64         `json:"lastRun"`
		LastRunEmpty  int64         `json:"lastRunEmpty"`
		Summary       BackupSummary `json:"summary"`
		SummaryOther  BackupSummary `json:"summaryOther"`
		SummaryEmpty  BackupSummary `json:"summaryEmpty"`
		GetFirst      *BackupRow    `json:"getFirst"`
		GetMissing    *BackupRow    `json:"getMissing"`
	} `json:"answers"`
}

func seededBackupDB(t *testing.T, c bkCases) *DB {
	t.Helper()
	// Reuses the package's own helper, so this test sits on the same schema
	// version and audit DDL every other db test does.
	dir := newDB(t, 14, true)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`CREATE TABLE config_backups (
		id INTEGER PRIMARY KEY AUTOINCREMENT, router_id TEXT NOT NULL,
		taken_at INTEGER NOT NULL, outcome TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'schedule', actor TEXT, stem TEXT, dir TEXT,
		fingerprint TEXT, rsc_bytes INTEGER NOT NULL DEFAULT 0,
		backup_bytes INTEGER NOT NULL DEFAULT 0, model TEXT, serial TEXT,
		os_version TEXT, ms INTEGER NOT NULL DEFAULT 0, pruned_at INTEGER, error TEXT);`); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range c.Rows {
		var model, serial, osv any
		if r.Identity != nil {
			model, serial, osv = r.Identity.Model, r.Identity.Serial, r.Identity.OSVersion
		}
		src := r.Source
		if src == "" {
			src = "schedule"
		}
		res, err := h.Exec(`INSERT INTO config_backups
			(router_id, taken_at, outcome, source, actor, stem, dir, fingerprint,
			 rsc_bytes, backup_bytes, model, serial, os_version, ms, error)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.RouterID, r.TakenAt, r.Outcome, src, r.Actor, r.Stem, r.Dir,
			r.Fingerprint, r.RscBytes, r.BackupBytes, model, serial, osv, r.MS, r.Error)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	if _, err := h.Exec(`UPDATE config_backups SET pruned_at = ? WHERE id = ?`,
		c.PrunedAt, ids[c.PrunedIndex]); err != nil {
		t.Fatal(err)
	}
	h.Close()
	return openTest(t, dir)
}

func sameRows(t *testing.T, what string, got, want []BackupRow) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Errorf("%s\n    got  %s\n    live %s", what, g, w)
	}
}

func TestBackupReadsAgainstLive(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/backup-history-cases.json")
	if err != nil {
		t.Fatalf("case file missing — see tools/backup-history-cases.js: %v", err)
	}
	var c bkCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Rows) == 0 || len(c.Answers.ListAll) == 0 {
		t.Fatal("empty corpus — recordBackup swallows a bad row shape, so check the generator")
	}
	d := seededBackupDB(t, c)

	const R, OTHER, EMPTY = "router-a", "router-b", "router-empty"

	got, err := d.ListBackups(R, 200)
	if err != nil {
		t.Fatal(err)
	}
	sameRows(t, "ListBackups(R, 200)", got, c.Answers.ListAll)

	got, _ = d.ListBackups(R, 2)
	sameRows(t, "ListBackups(R, 2)", got, c.Answers.ListLimited)

	// A zero limit means 200, not none — `Number(limit) || 200`.
	got, _ = d.ListBackups(R, 0)
	sameRows(t, "ListBackups(R, 0)", got, c.Answers.ListZeroLimit)

	got, _ = d.ListBackups(OTHER, 200)
	sameRows(t, "ListBackups(OTHER)", got, c.Answers.ListOther)

	got, _ = d.ListBackups(EMPTY, 200)
	sameRows(t, "ListBackups(EMPTY)", got, c.Answers.ListEmpty)

	got, _ = d.StoredBackups(R)
	sameRows(t, "StoredBackups(R)", got, c.Answers.Stored)

	got, _ = d.StoredBackups(EMPTY)
	sameRows(t, "StoredBackups(EMPTY)", got, c.Answers.StoredEmpty)

	if ts, _ := d.LastBackupRun(R); ts != c.Answers.LastRun {
		t.Errorf("LastBackupRun(R) = %d, live %d", ts, c.Answers.LastRun)
	}
	if ts, _ := d.LastBackupRun(EMPTY); ts != c.Answers.LastRunEmpty {
		t.Errorf("LastBackupRun(EMPTY) = %d, live %d", ts, c.Answers.LastRunEmpty)
	}

	for _, tc := range []struct {
		name string
		id   string
		want BackupSummary
	}{
		{"summary", R, c.Answers.Summary},
		{"summaryOther", OTHER, c.Answers.SummaryOther},
		{"summaryEmpty", EMPTY, c.Answers.SummaryEmpty},
	} {
		s, err := d.GetBackupSummary(tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		g, _ := json.Marshal(s)
		w, _ := json.Marshal(tc.want)
		if string(g) != string(w) {
			t.Errorf("%s\n    got  %s\n    live %s", tc.name, g, w)
		}
	}

	// A router with no history at all: SUM over no rows is NULL in SQLite, and a
	// port scanning that into a plain int64 errors rather than reporting zero.
	if c.Answers.SummaryEmpty.Runs != 0 || c.Answers.SummaryEmpty.Bytes != 0 {
		t.Fatal("the empty-router case is not actually empty; the corpus is wrong")
	}

	if b, _ := d.GetBackup(1); b == nil {
		t.Error("GetBackup(1) returned nothing")
	}
	if b, err := d.GetBackup(999999); b != nil || err != nil {
		t.Errorf("GetBackup(missing) = %v, %v; want nil, nil", b, err)
	}
}

// TestSummaryExcludesPrunedAndUnstored states the arithmetic in words, because
// it is the arithmetic the page's cards show.
func TestSummaryExcludesPrunedAndUnstored(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/backup-history-cases.json")
	if err != nil {
		t.Skip("no corpus")
	}
	var c bkCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	d := seededBackupDB(t, c)
	s, err := d.GetBackupSummary("router-a")
	if err != nil {
		t.Fatal(err)
	}
	// Five runs: two stored pairs, one pruned, one unchanged, one failed.
	if s.Runs != 5 {
		t.Errorf("runs = %d, want 5 — every run gets a row, stored or not", s.Runs)
	}
	if s.Stored != 2 {
		t.Errorf("stored = %d, want 2 — a pruned pair and a run that stored "+
			"nothing are both not stored", s.Stored)
	}
	if s.Bytes != 13000 {
		t.Errorf("bytes = %d, want 13000 — the pruned pair's 10000 bytes are "+
			"no longer on disk and must leave the total", s.Bytes)
	}
	if s.LastOutcome == nil || *s.LastOutcome != "failed" {
		t.Error("lastOutcome must come from the NEWEST row, which failed")
	}
}

// TestListBackupsClampIsUnreachableFromTheCorpus tests the limit clamp DIRECTLY,
// because the corpus cannot reach it.
//
// `Math.min(Number(limit) || 200, 1000)` has two constants, and a corpus of five
// rows returns all five for either of them — a mutation changing the zero-limit
// default from 200 to 1000 passed the differential test untouched. Reaching it
// through the corpus would mean carrying a thousand seeded rows in a file that
// exists to be reviewable; seeding them here costs nothing and says the same
// thing.
func TestListBackupsClampIsUnreachableFromTheCorpus(t *testing.T) {
	dir := newDB(t, 14, true)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`CREATE TABLE config_backups (
		id INTEGER PRIMARY KEY AUTOINCREMENT, router_id TEXT NOT NULL,
		taken_at INTEGER NOT NULL, outcome TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'schedule', actor TEXT, stem TEXT, dir TEXT,
		fingerprint TEXT, rsc_bytes INTEGER NOT NULL DEFAULT 0,
		backup_bytes INTEGER NOT NULL DEFAULT 0, model TEXT, serial TEXT,
		os_version TEXT, ms INTEGER NOT NULL DEFAULT 0, pruned_at INTEGER, error TEXT);`); err != nil {
		t.Fatal(err)
	}
	tx, _ := h.Begin()
	for i := 0; i < 1500; i++ {
		if _, err := tx.Exec(`INSERT INTO config_backups (router_id, taken_at, outcome)
			VALUES ('r', ?, 'changed')`, int64(1767225600000+i*1000)); err != nil {
			t.Fatal(err)
		}
	}
	tx.Commit()
	h.Close()
	d := openTest(t, dir)

	for _, tc := range []struct{ ask, want int }{
		{0, 200},   // `Number(0) || 200`
		{-5, 200},  // and anything unreadable
		{50, 50},   // honoured
		{999, 999}, // still under the cap
		{1000, 1000},
		{5000, 1000}, // clamped
	} {
		got, err := d.ListBackups("r", tc.ask)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.want {
			t.Errorf("ListBackups(limit=%d) returned %d rows, want %d", tc.ask, len(got), tc.want)
		}
	}
}

// THE BACKUPS TABLE SHOWS ONE `unchanged` ROW, NOT ONE PER DAY.
//
// An unchanged run stores no pair, so every one of them was a row offering
// nothing to restore; on a stable router with a daily schedule they arrive one
// per day and bury the rows that ARE restore points. The Node side filtered
// these out in SQL and the port did not carry it.
//
// The corpus cannot catch this: it holds exactly one unchanged row, so a
// filtered and an unfiltered query return the same six rows.
func TestListBackupsKeepsOnlyTheNewestUnchangedRun(t *testing.T) {
	dir := newDB(t, 14, true)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`CREATE TABLE config_backups (
		id INTEGER PRIMARY KEY AUTOINCREMENT, router_id TEXT NOT NULL,
		taken_at INTEGER NOT NULL, outcome TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'schedule', actor TEXT, stem TEXT, dir TEXT,
		fingerprint TEXT, rsc_bytes INTEGER NOT NULL DEFAULT 0,
		backup_bytes INTEGER NOT NULL DEFAULT 0, model TEXT, serial TEXT,
		os_version TEXT, ms INTEGER NOT NULL DEFAULT 0, pruned_at INTEGER, error TEXT);`); err != nil {
		t.Fatal(err)
	}
	const base = int64(1767225600000)
	// A real stable router: one genuine backup, then a fortnight of no-ops.
	if _, err := h.Exec(`INSERT INTO config_backups (router_id, taken_at, outcome, stem)
		VALUES ('r', ?, 'changed', 'cfg-2026-01-01')`, base); err != nil {
		t.Fatal(err)
	}
	newestUnchanged := base
	for i := 1; i <= 14; i++ {
		newestUnchanged = base + int64(i)*86_400_000
		if _, err := h.Exec(`INSERT INTO config_backups (router_id, taken_at, outcome)
			VALUES ('r', ?, 'unchanged')`, newestUnchanged); err != nil {
			t.Fatal(err)
		}
	}
	// A second router must be unaffected by the first one's newest run.
	if _, err := h.Exec(`INSERT INTO config_backups (router_id, taken_at, outcome)
		VALUES ('other', ?, 'unchanged')`, base+99); err != nil {
		t.Fatal(err)
	}
	h.Close()
	d := openTest(t, dir)

	got, err := d.ListBackups("r", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ListBackups returned %d rows, want 2 (one changed, one unchanged)", len(got))
	}
	// Newest first, so the surviving no-op sits on top — which is where it
	// answers "did the schedule fire?".
	if got[0].Outcome != "unchanged" || got[0].TakenAt != newestUnchanged {
		t.Errorf("top row = %s at %d, want the newest unchanged at %d",
			got[0].Outcome, got[0].TakenAt, newestUnchanged)
	}
	if got[1].Outcome != "changed" {
		t.Errorf("second row = %s, want the real restore point", got[1].Outcome)
	}

	// PER ROUTER, not globally: `other`'s only run is older than `r`'s newest,
	// and a filter written against one router's maximum would drop it.
	other, err := d.ListBackups("other", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 {
		t.Errorf("the other router returned %d rows, want its own 1", len(other))
	}

	// AND THE ROWS ARE STILL THERE. This is a view filter; LastBackupRun reads
	// the newest run of any outcome and gates the scheduler, so if the filter
	// had deleted anything a stable router would re-export on every tick.
	last, err := d.LastBackupRun("r")
	if err != nil {
		t.Fatal(err)
	}
	if last != newestUnchanged {
		t.Errorf("LastBackupRun = %d, want %d — the unchanged runs must stay recorded",
			last, newestUnchanged)
	}
}

// TestRecordBackupRoundTripsEveryOutcome pins the write against the reads.
//
// The three shapes a run can take are all ordinary states, and the reads treat
// them differently: a `changed` run has a stem and bytes, an `unchanged` run has
// a fingerprint and NO stem, and a `failed` run has an error and neither. A
// write that stored "" instead of NULL for the absent ones would make
// StoredBackups count runs that stored nothing.
func TestRecordBackupRoundTripsEveryOutcome(t *testing.T) {
	dir := newDB(t, 14, true)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`CREATE TABLE config_backups (
		id INTEGER PRIMARY KEY AUTOINCREMENT, router_id TEXT NOT NULL,
		taken_at INTEGER NOT NULL, outcome TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'schedule', actor TEXT, stem TEXT, dir TEXT,
		fingerprint TEXT, rsc_bytes INTEGER NOT NULL DEFAULT 0,
		backup_bytes INTEGER NOT NULL DEFAULT 0, model TEXT, serial TEXT,
		os_version TEXT, ms INTEGER NOT NULL DEFAULT 0, pruned_at INTEGER, error TEXT);`); err != nil {
		t.Fatal(err)
	}
	h.Close()
	d := openTest(t, dir)

	s := func(v string) *string { return &v }
	base := int64(1767225600000)

	changed, err := d.RecordBackup(BackupRun{
		RouterID: "r1", TakenAt: base, Outcome: "changed", Source: "manual",
		Actor: s("alice"), Stem: s("2026-01-01T000000"), Dir: s("/data/config-backups/r1"),
		Fingerprint: s("f1"), RscBytes: 1000, BackupBytes: 4000,
		Model: s("hAP ax^3"), Serial: s("S1"), OSVersion: s("7.24"), MS: 5000,
	})
	if err != nil || changed == 0 {
		t.Fatalf("changed run: %d, %v", changed, err)
	}
	if _, err := d.RecordBackup(BackupRun{
		RouterID: "r1", TakenAt: base + 1000, Outcome: "unchanged",
		Fingerprint: s("f1"), MS: 1200,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RecordBackup(BackupRun{
		RouterID: "r1", TakenAt: base + 2000, Outcome: "failed",
		Error: s("no space left on device"), MS: 900,
	}); err != nil {
		t.Fatal(err)
	}

	// All three are RUNS.
	sum, err := d.GetBackupSummary("r1")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Runs != 3 {
		t.Errorf("runs = %d, want 3 — every run gets a row", sum.Runs)
	}
	// Only the first STORED anything.
	if sum.Stored != 1 || sum.Bytes != 5000 {
		t.Errorf("stored = %d bytes = %d, want 1 and 5000", sum.Stored, sum.Bytes)
	}
	// And the newest is the failure.
	if sum.LastOutcome == nil || *sum.LastOutcome != "failed" {
		t.Errorf("lastOutcome = %v, want failed", sum.LastOutcome)
	}

	// StoredBackups must see only the one with a stem — an "" stem would make it
	// count a run that stored nothing.
	stored, err := d.StoredBackups("r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].ID != changed {
		t.Fatalf("stored = %d rows, want only the changed run", len(stored))
	}

	// A source defaults to "schedule" when the runner does not say.
	rows, _ := d.ListBackups("r1", 10)
	for _, r := range rows {
		if r.Source == "" {
			t.Errorf("row %d has an empty source; the column defaults to 'schedule'", r.ID)
		}
	}

	// A newly written row is never born pruned.
	for _, r := range rows {
		if r.PrunedAt != nil {
			t.Errorf("row %d was born pruned", r.ID)
		}
	}

	// And retention can then mark one.
	ok, err := d.MarkBackupPruned(changed, base+9999)
	if err != nil || !ok {
		t.Fatalf("MarkBackupPruned: %v, %v", ok, err)
	}
	if sum, _ := d.GetBackupSummary("r1"); sum.Stored != 0 || sum.Bytes != 0 {
		t.Errorf("after pruning: stored = %d bytes = %d, want 0 and 0", sum.Stored, sum.Bytes)
	}

	// DeleteBackup removes the row outright; MarkBackupPruned kept it.
	before, _ := d.ListBackups("r1", 10)
	if ok, err := d.DeleteBackup(changed); err != nil || !ok {
		t.Fatalf("DeleteBackup: %v, %v", ok, err)
	}
	after, _ := d.ListBackups("r1", 10)
	if len(after) != len(before)-1 {
		t.Errorf("delete left %d rows, want %d", len(after), len(before)-1)
	}
	if ok, _ := d.DeleteBackup(changed); ok {
		t.Error("deleting a row twice reported a second change")
	}
}

// TestLatestFingerprintIgnoresFailedRuns is the rule that stops a transient
// failure being reported as drift.
//
// Any run that READ an export has a fingerprint, whether or not it stored a
// pair. A failed run has none. Taking the newest ROW instead of the newest
// FINGERPRINT would make one unreachable minute produce a false "configuration
// changed" notification and a redundant restore point on the next success.
func TestLatestFingerprintIgnoresFailedRuns(t *testing.T) {
	dir := newDB(t, 14, true)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(`CREATE TABLE config_backups (
		id INTEGER PRIMARY KEY AUTOINCREMENT, router_id TEXT NOT NULL,
		taken_at INTEGER NOT NULL, outcome TEXT NOT NULL,
		source TEXT NOT NULL DEFAULT 'schedule', actor TEXT, stem TEXT, dir TEXT,
		fingerprint TEXT, rsc_bytes INTEGER NOT NULL DEFAULT 0,
		backup_bytes INTEGER NOT NULL DEFAULT 0, model TEXT, serial TEXT,
		os_version TEXT, ms INTEGER NOT NULL DEFAULT 0, pruned_at INTEGER, error TEXT);`); err != nil {
		t.Fatal(err)
	}
	h.Close()
	d := openTest(t, dir)

	s := func(v string) *string { return &v }
	base := int64(1767225600000)

	// A router with no history at all answers "" rather than erroring.
	if fp, err := d.LatestFingerprint("r1"); err != nil || fp != "" {
		t.Fatalf("empty router: %q, %v", fp, err)
	}

	// A stored run.
	if _, err := d.RecordBackup(BackupRun{RouterID: "r1", TakenAt: base,
		Outcome: "changed", Fingerprint: s("fp-one"), Stem: s("2026-01-01T000000")}); err != nil {
		t.Fatal(err)
	}
	if fp, _ := d.LatestFingerprint("r1"); fp != "fp-one" {
		t.Errorf("after one run: %q", fp)
	}

	// An UNCHANGED run has a fingerprint and no pair — it must move this forward.
	if _, err := d.RecordBackup(BackupRun{RouterID: "r1", TakenAt: base + 1000,
		Outcome: "unchanged", Fingerprint: s("fp-two")}); err != nil {
		t.Fatal(err)
	}
	if fp, _ := d.LatestFingerprint("r1"); fp != "fp-two" {
		t.Errorf("an unchanged run did not move the fingerprint forward: %q", fp)
	}

	// A FAILED run has none — and must leave it alone.
	if _, err := d.RecordBackup(BackupRun{RouterID: "r1", TakenAt: base + 2000,
		Outcome: "failed", Error: s("unreachable")}); err != nil {
		t.Fatal(err)
	}
	if fp, _ := d.LatestFingerprint("r1"); fp != "fp-two" {
		t.Errorf("a failed run changed the last-seen fingerprint to %q — the next "+
			"successful run would store an unchanged configuration and report drift", fp)
	}

	// And it is scoped: another router's fingerprint is not this one's.
	if _, err := d.RecordBackup(BackupRun{RouterID: "r2", TakenAt: base + 3000,
		Outcome: "changed", Fingerprint: s("other")}); err != nil {
		t.Fatal(err)
	}
	if fp, _ := d.LatestFingerprint("r1"); fp != "fp-two" {
		t.Errorf("another router's fingerprint leaked in: %q", fp)
	}
}

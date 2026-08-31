package db

// The backup history's READ path.
//
// The SQL is COPIED from src/db.js rather than reimplemented. Both sides run
// SQLite against the same file, so identical query text makes the answers
// identical by construction rather than by comparison — and the aggregate in
// BackupSummary is the kind of thing two hand-written versions agree on for
// every case anybody thought to try.
//
// Nothing here writes. The write path belongs with the runner and is separate
// for the reason history.go's header gives: a promise contradicted three
// functions later is worse than no promise.

import (
	"database/sql"
	"errors"
)

// BackupRow is one run of the backup job, stored or not.
//
// EVERY RUN GETS A ROW, whatever the outcome. A router that has failed nightly
// for a month should be able to show that from its own history, so `stem` and
// `dir` being NULL is an ordinary state — it means the run happened and stored
// nothing.
type BackupRow struct {
	ID          int64   `json:"id"`
	RouterID    string  `json:"router_id"`
	TakenAt     int64   `json:"taken_at"`
	Outcome     string  `json:"outcome"`
	Source      string  `json:"source"`
	Actor       *string `json:"actor"`
	Stem        *string `json:"stem"`
	Dir         *string `json:"dir"`
	Fingerprint *string `json:"fingerprint"`
	RscBytes    int64   `json:"rsc_bytes"`
	BackupBytes int64   `json:"backup_bytes"`
	Model       *string `json:"model"`
	Serial      *string `json:"serial"`
	OSVersion   *string `json:"os_version"`
	MS          int64   `json:"ms"`
	// PrunedAt is set when retention removed the FILES. The row stays, so the
	// History table can explain the disappearance rather than the pair simply
	// vanishing from the list.
	PrunedAt *int64  `json:"pruned_at"`
	Error    *string `json:"error"`
}

const backupCols = `id, router_id, taken_at, outcome, source, actor, stem, dir,
	fingerprint, rsc_bytes, backup_bytes, model, serial, os_version, ms, pruned_at, error`

func scanBackups(rows *sql.Rows) ([]BackupRow, error) {
	out := []BackupRow{}
	for rows.Next() {
		var r BackupRow
		if err := rows.Scan(&r.ID, &r.RouterID, &r.TakenAt, &r.Outcome, &r.Source,
			&r.Actor, &r.Stem, &r.Dir, &r.Fingerprint, &r.RscBytes, &r.BackupBytes,
			&r.Model, &r.Serial, &r.OSVersion, &r.MS, &r.PrunedAt, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListBackups is the History table's page, newest first.
//
// The limit is clamped exactly as the original clamps it: `Math.min(Number(limit)
// || 200, 1000)`, so a zero or unreadable limit becomes 200 rather than none.
func (d *DB) ListBackups(routerID string, limit int) ([]BackupRow, error) {
	if d == nil || d.sql == nil {
		return []BackupRow{}, errors.New("db not open")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := d.sql.Query(`SELECT `+backupCols+` FROM config_backups WHERE router_id = ?
                ORDER BY taken_at DESC LIMIT ?`, routerID, limit)
	if err != nil {
		return []BackupRow{}, err
	}
	defer rows.Close()
	return scanBackups(rows)
}

// GetBackup reads one row, or nil when it does not exist.
func (d *DB) GetBackup(id int64) (*BackupRow, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT `+backupCols+` FROM config_backups WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got, err := scanBackups(rows)
	if err != nil || len(got) == 0 {
		return nil, err
	}
	return &got[0], nil
}

// LastBackupRun is when this router was last attempted — the value `IsDue`
// measures its interval from.
//
// ANY RUN THAT READ AN EXPORT COUNTS, whether or not it stored a pair. So an
// unchanged run still moves this forward and a FAILED one leaves it alone, which
// is what stops a transient failure from being reported as drift on the next
// successful run.
func (d *DB) LastBackupRun(routerID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	var ts int64
	err := d.sql.QueryRow(`SELECT taken_at FROM config_backups WHERE router_id = ?
                     ORDER BY taken_at DESC LIMIT 1`, routerID).Scan(&ts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return ts, err
}

// LatestFingerprint is the configuration this router was last SEEN with, which
// is what the next run compares against to decide whether anything changed.
//
// ── `fingerprint IS NOT NULL` IS THE WHOLE POINT ────────────────────────────
//
// Any run that READ an export has a fingerprint, whether or not it stored a
// pair. So an unchanged run still moves this forward, and a FAILED one — which
// has no fingerprint — leaves it alone.
//
// Take the newest row instead and a failed run answers "", the next successful
// run finds no previous fingerprint, and a configuration nobody touched is
// stored again and reported as drift. One unreachable minute would produce a
// false "configuration changed" notification and a redundant restore point.
func (d *DB) LatestFingerprint(routerID string) (string, error) {
	if d == nil || d.sql == nil {
		return "", errors.New("db not open")
	}
	var fp string
	err := d.sql.QueryRow(`SELECT fingerprint FROM config_backups
                     WHERE router_id = ? AND fingerprint IS NOT NULL
                     ORDER BY taken_at DESC LIMIT 1`, routerID).Scan(&fp)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fp, err
}

// StoredBackups are the pairs that still have files — what retention decides
// over. A row with no stem never stored anything; a pruned one no longer has it.
func (d *DB) StoredBackups(routerID string) ([]BackupRow, error) {
	if d == nil || d.sql == nil {
		return []BackupRow{}, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT `+backupCols+` FROM config_backups
                WHERE router_id = ? AND stem IS NOT NULL AND pruned_at IS NULL
                ORDER BY taken_at DESC`, routerID)
	if err != nil {
		return []BackupRow{}, err
	}
	defer rows.Close()
	return scanBackups(rows)
}

// BackupSummary is the page's four cards.
type BackupSummary struct {
	Runs        int     `json:"runs"`
	Stored      int     `json:"stored"`
	Bytes       int64   `json:"bytes"`
	LastAt      int64   `json:"lastAt"`
	LastOutcome *string `json:"lastOutcome"`
}

// GetBackupSummary counts runs, stored pairs and the disk they occupy.
//
// TWO QUERIES, not one, because the original uses two: the aggregate cannot also
// report the LAST row's outcome without a window function or a self-join, and
// the second query is an indexed single-row read.
func (d *DB) GetBackupSummary(routerID string) (BackupSummary, error) {
	s := BackupSummary{}
	if d == nil || d.sql == nil {
		return s, errors.New("db not open")
	}
	// SUM over no rows is NULL in SQLite, which is why these scan into nullable
	// holders — `agg.stored || 0` on the other side collapses the same thing.
	var runs int
	var stored, bytes sql.NullInt64
	err := d.sql.QueryRow(`
    SELECT COUNT(*) AS runs,
           SUM(CASE WHEN stem IS NOT NULL AND pruned_at IS NULL THEN 1 ELSE 0 END) AS stored,
           SUM(CASE WHEN stem IS NOT NULL AND pruned_at IS NULL
                    THEN rsc_bytes + backup_bytes ELSE 0 END) AS bytes
    FROM config_backups WHERE router_id = ?`, routerID).Scan(&runs, &stored, &bytes)
	if err != nil {
		return s, err
	}
	s.Runs = runs
	s.Stored = int(stored.Int64)
	s.Bytes = bytes.Int64

	var takenAt int64
	var outcome string
	err = d.sql.QueryRow(`SELECT taken_at, outcome FROM config_backups WHERE router_id = ?
                      ORDER BY taken_at DESC LIMIT 1`, routerID).Scan(&takenAt, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	s.LastAt = takenAt
	s.LastOutcome = &outcome
	return s, nil
}

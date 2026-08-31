package db

// The backup history's two writes.
//
// Kept apart from backups.go because that file's header promises it does not
// write — the same split history.go and schedule_write.go already use, for the
// reason that file gives: a promise contradicted three functions later is worse
// than no promise.
//
// ── PRUNING AND DELETING ARE DIFFERENT ACTS ─────────────────────────────────
//
// `MarkBackupPruned` is RETENTION: MikroDash aged a pair out on its own, so the
// artefacts go and THE ROW STAYS. The History table then explains the
// disappearance instead of the pair simply vanishing from the list.
//
// `DeleteBackup` is an OPERATOR saying "I do not want this listed", and leaving
// a tombstone behind answers a question they did not ask — so the row goes too.
//
// Neither loses the trail: `audit_events` independently records the backup.run
// that created a pair and the backup.delete that removed it, and that table is
// deliberately absent from PURGE_TABLES and from deleteRouterData(), which is
// what makes it the one place hard to erase.

import "errors"

// BackupRun is one completed run, as the runner reports it.
//
// EVERY RUN GETS A ROW, whatever the outcome. A router that has failed nightly
// for a month should be able to show that from its own history — which is why
// `Stem` and `Dir` are pointers: a run that stored nothing is an ordinary state,
// not a missing value to paper over with "".
type BackupRun struct {
	RouterID    string
	TakenAt     int64 // epoch ms
	Outcome     string
	Source      string // "schedule" | "manual"
	Actor       *string
	Stem        *string
	Dir         *string
	Fingerprint *string
	RscBytes    int64
	BackupBytes int64
	Model       *string
	Serial      *string
	OSVersion   *string
	MS          int64
	Error       *string
}

// RecordBackup writes one run and returns its id.
//
// `pruned_at` is NOT in the column list: a row is born un-pruned, and retention
// is the only thing that sets it. Including it here would let a runner claim a
// pair was already aged out.
func (d *DB) RecordBackup(r BackupRun) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db: not open")
	}
	source := r.Source
	if source == "" {
		source = "schedule"
	}
	res, err := d.sql.Exec(`
    INSERT INTO config_backups
      (router_id, taken_at, outcome, source, actor, stem, dir, fingerprint,
       rsc_bytes, backup_bytes, model, serial, os_version, ms, error)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RouterID, r.TakenAt, r.Outcome, source, r.Actor, r.Stem, r.Dir,
		r.Fingerprint, r.RscBytes, r.BackupBytes, r.Model, r.Serial, r.OSVersion,
		r.MS, r.Error)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MarkBackupPruned records that retention removed a pair's files. `ts` is epoch
// milliseconds, as every other timestamp in this schema is.
//
// Reports whether a row actually changed, as `info.changes > 0` does. A caller
// that pruned files for a row that no longer exists should know: the two halves
// of retention have disagreed about what is on disk.
func (d *DB) MarkBackupPruned(id int64, ts int64) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db: not open")
	}
	res, err := d.sql.Exec(`UPDATE config_backups SET pruned_at = ? WHERE id = ?`, ts, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteBackup removes a row outright — a deliberate operator delete.
//
// THE CALLER RESOLVES THE ID ROUTER-FIRST, so this can only ever be aimed at a
// row it was already allowed to see. Nothing here re-checks that, on purpose: a
// second, weaker copy of an authorization rule is how the two come to disagree.
func (d *DB) DeleteBackup(id int64) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db: not open")
	}
	res, err := d.sql.Exec(`DELETE FROM config_backups WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

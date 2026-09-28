package db

// The backup history's two writes.
//
// Kept apart from backups.go because that file's header promises it does not
// write — the same split history.go and schedule_write.go already use, for the
// reason that file gives: a promise contradicted three functions later is worse
// than no promise.
//
// ── PRUNING AND DELETING WERE DIFFERENT ACTS, AND ARE NOW ONE ───────────────
//
// `MarkBackupPruned` was RETENTION: MikroDash aged a pair out on its own, so the
// artefacts went and THE ROW STAYED, letting the History table explain the
// disappearance rather than have the pair vanish from the list.
//
// One sentence of explanation, at the price of a row per router per day for
// ever — and nothing else ever read those rows. Restore, download, raw-serving
// and diff each refuse a pruned row, and the scheduler's `LastBackupRun` cannot
// see one because retention always spares the newest run. Worse, the row went on
// reporting the outcome of the day it ran: a green "Stored" badge and the size
// of a file that had not existed for weeks.
//
// `DeleteBackup` is an OPERATOR saying "I do not want this listed", and is now
// also what retention calls, because it is the same act: the pair is gone and
// the record of it has nothing left to describe. `backups.PruneStore.ForgetRow`
// is the seam, and `PurgePrunedBackups` clears what older builds marked.
//
// AND THE CLAIM THAT MADE THIS SAFE WAS FALSE. It read: "audit_events
// independently records the backup.run that created a pair and the
// backup.delete that removed it, and that table is deliberately absent from
// PURGE_TABLES and from deleteRouterData(), which is what makes it the one place
// hard to erase." The second half still holds. The first half was true only of
// MANUAL runs — every `backup.run` event came from the Backups page — so on an
// install with a schedule the trail recorded almost none of them. Measured on a
// real install: 7 events against 19 recorded runs. `runScheduledBackup` writes
// one now, which is what makes removing the row safe rather than merely tidy.

import "errors"

// PurgePrunedBackups removes the tombstones an older build left behind.
//
// Retention used to mark a row and keep it; it deletes now. Rows marked by a
// previous version would otherwise sit in the History table for ever, labelled
// "Pruned" and describing files that have long since gone — the exact state this
// change was made to stop producing.
//
// ONE-OFF AND IDEMPOTENT: afterwards nothing has `pruned_at` set, and nothing
// sets it again. Called from `cmd/mikrodash` rather than from `Open`, for the
// reason every migration here is: `cmd/compat` opens a real /data through a
// READ-ONLY mount, and a write in Open would fail the one tool whose job is to
// read a production directory untouched.
func (d *DB) PurgePrunedBackups() (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	res, err := d.sql.Exec(
		`DELETE FROM config_backups WHERE pruned_at IS NOT NULL`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

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
	// SecretsBytes is 0 unless this router keeps a migration export.
	SecretsBytes int64
	Model        *string
	Serial       *string
	OSVersion    *string
	MS           int64
	Error        *string
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
       rsc_bytes, backup_bytes, secrets_bytes, model, serial, os_version, ms, error)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.RouterID, r.TakenAt, r.Outcome, source, r.Actor, r.Stem, r.Dir,
		r.Fingerprint, r.RscBytes, r.BackupBytes, r.SecretsBytes, r.Model, r.Serial,
		r.OSVersion, r.MS, r.Error)
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

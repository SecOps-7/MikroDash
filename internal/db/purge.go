package db

import (
	"errors"
	"fmt"
	"log"
)

// Removing a router takes its data with it — and deliberately does NOT take
// everything.
//
// ── THE ABSENCES ARE THE DESIGN, NOT AN OVERSIGHT ───────────────────────────
//
// `config_backups` is not here, and the live source says why where the list is
// written: "Nothing that sweeps time-series data can reach a restore point.
// Pruning backups is its own thing, bounded by the per-router keepCount/keepDays,
// and it clears `stem` rather than the row — the history of when a router was
// checked outlives the artefacts."
//
// `audit_events` is not here either, for a different reason: the trail is
// deliberately absent from every purge path, so a row cannot be withdrawn short
// of age-based retention. Removing a router must not erase the record of who
// removed it.
//
// Both are the kind of omission a port "completes" by accident — the list looks
// short, the tables are obviously router-scoped, and adding them leaves every
// test passing. `TestTheRouterPurgeTablesMatchLive` reads the live function and
// fails on a table added OR missing, in either direction.

// routerDataTables is what `deleteRouterData` clears, in the live order.
//
// Written out and CHECKED against `src/db.js` rather than lifted at run time,
// because this has to work in a binary with no live tree beside it. The test is
// what keeps the two honest.
var routerDataTables = []string{
	"ping_samples",
	"traffic_samples",
	"bandwidth_usage",
	"alert_events",
	"connectivity_events",
}

// routerPurgeExcluded is what a router purge must NEVER touch, with the reason.
// Asserted by the same test, so "completing" the list above fails twice.
var routerPurgeExcluded = map[string]string{
	"config_backups": "a restore point is not time-series data. Pruning backups is bounded by " +
		"the per-router keepCount/keepDays and clears `stem` rather than the row, so the " +
		"history of when a router was checked outlives the artefacts",
	"audit_events": "the trail is absent from every purge path by design, so a row cannot be " +
		"withdrawn short of age-based retention. Removing a router must not erase the record " +
		"of who removed it",
}

// DeleteRouterData clears every time-series table for one router, in ONE
// transaction.
//
// The transaction is not decoration: these five tables are read together by the
// Reports page, and a partial purge leaves a report joining live rows to deleted
// ones — which reads as corrupt data rather than as a failed removal.
func (d *DB) DeleteRouterData(routerID string) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	if routerID == "" {
		return errors.New("db: no router id")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	for _, table := range routerDataTables {
		// The table names come from the constant above, never from a caller.
		if _, err := tx.Exec(
			fmt.Sprintf(`DELETE FROM %s WHERE router_id = ?`, table), routerID); err != nil {
			return fmt.Errorf("db: purge %s: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("[db] deleted all data for router %s", routerID)
	return nil
}

// DeleteGrantsForScope removes every grant scoped to one thing, and reports how
// many.
//
// ── THIS IS AN AUTHORIZATION CHANGE, NOT A CLEANUP ──────────────────────────
//
// A grant naming a removed router is not merely stale: it is a permission with
// no visible subject, and it will not appear in any principal's summary. The
// live route calls this BEFORE tearing the session down, and records the count.
func (d *DB) DeleteGrantsForScope(scopeType, scopeID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db: not open")
	}
	res, err := d.sql.Exec(
		`DELETE FROM grants WHERE scope_type = ? AND scope_id = ?`, scopeType, scopeID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteReportSchedulesForRouter removes schedules for a router that no longer
// exists.
//
// A schedule for a removed router cannot run, and left behind it is a LIVE
// OUTBOUND EMAIL LOOP — the live comment says so, and says it is removed here,
// "where it is visible, rather than as a side effect of a retention sweep".
func (d *DB) DeleteReportSchedulesForRouter(routerID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db: not open")
	}
	res, err := d.sql.Exec(`DELETE FROM report_schedules WHERE router_id = ?`, routerID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

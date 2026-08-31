package db

// The only writes this package makes to the shared database, kept apart from
// history.go because that file's header promises it does not write — and a
// promise contradicted three functions later is worse than no promise.
//
// ── WRITING A TABLE THE NODE APP ALSO WRITES ────────────────────────────────
//
// This is why `Open` takes ONE connection with `_txlock=immediate`. A pool would
// mean several writers inside this process competing for a lock the Node process
// may already hold, and SQLite answers an upgrade deadlock with SQLITE_BUSY
// immediately — a busy timeout does not save it.
//
// ── A SCHEDULE IS NOT A ROUTER RESOURCE ─────────────────────────────────────
//
// So none of the write guards in internal/server/resource.go apply: nothing here
// reaches a router, and there is no configuration to lock anyone out of. What it
// does reach is a mail sender, which is why the HTTP gate is write-level and the
// validation lives in internal/reports.

import "errors"

// UpsertReportSchedule inserts or replaces a schedule.
//
// `created_by` and `created_at` are NOT in the update branch, deliberately: an
// edit must not rewrite who made a schedule or when. The original's ON CONFLICT
// list omits them for the same reason — a record that can be edited by editing
// the thing it describes is not much of a record.
func (d *DB) UpsertReportSchedule(s ReportSchedule) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	_, err := d.sql.Exec(`
    INSERT INTO report_schedules
      (id, router_id, name, sections, interface, aggregate, recipients, frequency,
       send_hour, enabled, disabled_reason, created_by, created_at, updated_at)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
    ON CONFLICT(id) DO UPDATE SET
      name = excluded.name, sections = excluded.sections,
      interface = excluded.interface, aggregate = excluded.aggregate,
      recipients = excluded.recipients, frequency = excluded.frequency,
      send_hour = excluded.send_hour, enabled = excluded.enabled,
      disabled_reason = excluded.disabled_reason, updated_at = excluded.updated_at
  `, s.ID, s.RouterID, s.Name, s.Sections, s.Interface, s.Aggregate, s.Recipients,
		s.Frequency, s.SendHour, s.Enabled, s.DisabledReason, s.CreatedBy,
		s.CreatedAt, s.UpdatedAt)
	return err
}

// RemoveReportSchedule removes one schedule.
//
// Its runs go with it through the table's ON DELETE CASCADE, which is why `Open`
// turns foreign keys on: SQLite defaults them OFF per CONNECTION, so without that
// pragma the cascade is parsed and then ignored, leaving orphan run rows behind
// invisibly.
func (d *DB) RemoveReportSchedule(id string) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	_, err := d.sql.Exec(`DELETE FROM report_schedules WHERE id = ?`, id)
	return err
}

// SetReportScheduleEnabled switches a schedule on or off.
//
// `disabled_reason` is cleared when enabling and kept only while disabled, so the
// reason can never outlive the state it explains — a schedule that is ON and
// still carries "smtp not configured" reads as broken.
func (d *DB) SetReportScheduleEnabled(id string, enabled bool, reason string, now int64) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	on := 0
	if enabled {
		on = 1
		reason = ""
	}
	_, err := d.sql.Exec(`
    UPDATE report_schedules SET enabled = ?, disabled_reason = ?, updated_at = ?
    WHERE id = ?
  `, on, nul(reason), now, id)
	return err
}

// RecordReportRun writes one attempt to the run history.
//
// EVERY attempt, whatever its outcome — the live scheduler records this in a
// `finally`, so a run that threw is still on the record. That is the point of
// the table: a schedule that has been failing silently for a month is visible
// only if the failures were written down.
//
// `error` is stored as NULL rather than "" when there is none, so the Reports
// page can distinguish "no error" from "an error nobody described".
func (d *DB) RecordReportRun(r ReportRun) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	var actor any
	if r.Actor != nil && *r.Actor != "" {
		actor = *r.Actor
	}
	var errText any
	if r.Error != nil && *r.Error != "" {
		errText = *r.Error
	}
	_, err := d.sql.Exec(`
    INSERT INTO report_runs
      (schedule_id, ran_at, period_from, period_to, outcome, source, actor,
       recipients_n, bytes, rows_n, ms, error)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  `, r.ScheduleID, r.RanAt, r.PeriodFrom, r.PeriodTo, r.Outcome, r.Source, actor,
		r.Recipients, r.Bytes, r.Rows, r.Ms, errText)
	return err
}

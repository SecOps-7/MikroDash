package db

// Carrying a schedule's own recipient list onto a channel.
//
// ── WHY THESE ARE HERE AND NOT IN `portMigrations` ────────────────────────
//
// The conversion has to SEAL mail credentials into each new channel's config,
// which needs the settings store, which plain SQL in a migration list cannot
// reach. So migration 25 adds `channel_id` and stops, and `SeedReportChannels`
// in internal/server does the carrying through the calls below. Same division,
// and the same reason, as `SeedNotifyChannels`.
//
// ── THE COLUMN IS DROPPED, NOT LEFT ───────────────────────────────────────
//
// Recipients are configured on a channel and nowhere else. A `recipients`
// column that nothing reads is a second copy of that list which stops being
// true the first time somebody edits the channel, and a reader who found it
// would have no way to know which of the two was live. It goes once every
// schedule has been carried.

import "errors"

// ScheduleRecipients is one schedule that has not been carried onto a channel.
type ScheduleRecipients struct {
	ID         string
	Name       string
	Recipients string
}

// HasReportRecipientsColumn reports whether the legacy column is still there.
//
// THE WHOLE MIGRATION IS GUARDED ON THIS, so it runs exactly once: the column is
// dropped at the end, and its absence afterwards is what makes a second startup
// a no-op. A flag would have been a second thing to keep true.
func (d *DB) HasReportRecipientsColumn() (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db: not open")
	}
	rows, err := d.sql.Query(`PRAGMA table_info(report_schedules)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "recipients" {
			return true, rows.Err()
		}
	}
	return false, rows.Err()
}

// ReportSchedulesToCarry lists every schedule still holding its own recipients.
//
// ORDERED BY created_at so the channels it produces are numbered in the order
// the operator made the schedules, which is the only order that means anything
// to them.
func (d *DB) ReportSchedulesToCarry() ([]ScheduleRecipients, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db: not open")
	}
	rows, err := d.sql.Query(
		`SELECT id, name, recipients FROM report_schedules ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduleRecipients{}
	for rows.Next() {
		var r ScheduleRecipients
		if err := rows.Scan(&r.ID, &r.Name, &r.Recipients); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetReportScheduleChannel points one schedule at a channel.
func (d *DB) SetReportScheduleChannel(scheduleID, channelID string) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	_, err := d.sql.Exec(
		`UPDATE report_schedules SET channel_id = ? WHERE id = ?`, channelID, scheduleID)
	return err
}

// DropReportRecipientsColumn removes the legacy column.
//
// CALLED ONLY AFTER EVERY SCHEDULE HAS BEEN CARRIED, because this is the point
// of no return: the lists exist as channels afterwards and nowhere else.
func (d *DB) DropReportRecipientsColumn() error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	_, err := d.sql.Exec(`ALTER TABLE report_schedules DROP COLUMN recipients`)
	return err
}

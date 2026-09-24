package db

// Reads and writes for the notification channels table.
//
// ── A CHANNEL IS NOT A ROUTER RESOURCE ─────────────────────────────────────
//
// So none of the write guards in internal/server/resource.go apply, for the same
// reason `schedule_write.go` states: nothing here reaches a router. What it does
// reach is an outbound sender, which is why the HTTP gate is write-level and the
// validation lives above this file.
//
// ── THIS FILE ENCRYPTS NOTHING ─────────────────────────────────────────────
//
// `Config` arrives with its secrets already sealed, exactly as
// `user_notify_config` does: the crypto lives on the server, where a missing
// store can refuse the save, rather than here, where `internal/db` would have to
// learn about the settings envelope.

import (
	"database/sql"
	"errors"
)

// InstallOwner is the owner value for a channel that belongs to the install
// rather than to a person. The same string `alertdispatch` already uses for the
// install recipient, so the two vocabularies cannot drift apart.
const InstallOwner = "_install"

// NotifyChannel is one configured destination.
//
// `Config`, `Events` and `Routers` are JSON held as TEXT, the convention every
// list-bearing table here follows (`report_schedules.sections`,
// `cfg_runs.values_json`). `Enabled` is an INTEGER for the same reason
// `report_schedules.enabled` is.
type NotifyChannel struct {
	ID      string `json:"id"`
	Owner   string `json:"owner"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Enabled int    `json:"enabled"`
	Config  string `json:"config"`
	Events  string `json:"events"`
	Routers string `json:"routers"`
	// IfaceTypes is a JSON array of ether/wlan/bridge/vlan/other. EMPTY IS ALL,
	// exactly as Routers is, so a channel nobody has narrowed covers everything.
	IfaceTypes string `json:"iface_types"`
	CreatedBy  string `json:"created_by"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

const notifyChannelCols = `id, owner, name, kind, enabled, config, events, routers,
	COALESCE(iface_types, '[]'), COALESCE(created_by, ''), created_at, updated_at`

func scanNotifyChannels(rows *sql.Rows) ([]NotifyChannel, error) {
	defer rows.Close()
	// NEVER NIL. A nil slice marshals to `null`, and the browser's list render
	// would have to defend against it — what
	// `TestNoServerPayloadSendsANullArray` exists to stop.
	out := []NotifyChannel{}
	for rows.Next() {
		var c NotifyChannel
		if err := rows.Scan(&c.ID, &c.Owner, &c.Name, &c.Kind, &c.Enabled,
			&c.Config, &c.Events, &c.Routers, &c.IfaceTypes, &c.CreatedBy,
			&c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// NotifyChannels returns every channel, for every owner, in name order.
//
// The dispatcher wants them all: it decides per alert which owner a channel
// belongs to and whether that owner may see the router. Filtering by owner here
// would put that decision in two places.
func (d *DB) NotifyChannels() ([]NotifyChannel, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("no database")
	}
	rows, err := d.sql.Query(`SELECT ` + notifyChannelCols +
		` FROM notify_channels ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	return scanNotifyChannels(rows)
}

// NotifyChannelsFor returns one owner's channels.
func (d *DB) NotifyChannelsFor(owner string) ([]NotifyChannel, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("no database")
	}
	rows, err := d.sql.Query(`SELECT `+notifyChannelCols+
		` FROM notify_channels WHERE owner = ? ORDER BY name, id`, owner)
	if err != nil {
		return nil, err
	}
	return scanNotifyChannels(rows)
}

// NotifyChannelByID returns one channel. The second result is false when there
// is no such row, which the API needs in order to answer 404 before it answers
// "not permitted" — the order `alertAck` already follows.
func (d *DB) NotifyChannelByID(id string) (NotifyChannel, bool, error) {
	if d == nil || d.sql == nil {
		return NotifyChannel{}, false, errors.New("no database")
	}
	var c NotifyChannel
	err := d.sql.QueryRow(`SELECT `+notifyChannelCols+
		` FROM notify_channels WHERE id = ?`, id).
		Scan(&c.ID, &c.Owner, &c.Name, &c.Kind, &c.Enabled, &c.Config,
			&c.Events, &c.Routers, &c.IfaceTypes, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return NotifyChannel{}, false, nil
	}
	if err != nil {
		return NotifyChannel{}, false, err
	}
	return c, true, nil
}

// UpsertNotifyChannel inserts or replaces a channel.
//
// `owner`, `created_by` and `created_at` are NOT in the update branch,
// deliberately and for the reason `UpsertReportSchedule` gives: an edit must not
// rewrite who made a channel or when. Owner joins them because a channel that
// could change hands on an edit would let a user's own channel be re-pointed at
// the install, escaping the permission that put it there.
func (d *DB) UpsertNotifyChannel(c NotifyChannel) error {
	if d == nil || d.sql == nil {
		return errors.New("no database")
	}
	_, err := d.sql.Exec(`
    INSERT INTO notify_channels
      (id, owner, name, kind, enabled, config, events, routers, iface_types,
       created_by, created_at, updated_at)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
    ON CONFLICT(id) DO UPDATE SET
      name = excluded.name, kind = excluded.kind, enabled = excluded.enabled,
      config = excluded.config, events = excluded.events,
      routers = excluded.routers, iface_types = excluded.iface_types,
      updated_at = excluded.updated_at
  `, c.ID, c.Owner, c.Name, c.Kind, c.Enabled, c.Config, c.Events, c.Routers,
		c.IfaceTypes, nullIfEmpty(c.CreatedBy), c.CreatedAt, c.UpdatedAt)
	return err
}

// DeleteNotifyChannel removes one channel and reports whether it existed.
func (d *DB) DeleteNotifyChannel(id string) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("no database")
	}
	res, err := d.sql.Exec(`DELETE FROM notify_channels WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CountNotifyChannels is what the migration asks before it runs: an install that
// already has channels is one this has already been done for.
func (d *DB) CountNotifyChannels() (int, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("no database")
	}
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) FROM notify_channels`).Scan(&n)
	return n, err
}

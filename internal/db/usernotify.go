package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The table, as the live migration creates it:
//
//	user_notify_config(user_id TEXT PRIMARY KEY, data TEXT NOT NULL,
//	                   updated_at INTEGER NOT NULL)
//
// `data` is a JSON blob holding only the allowlisted keys — the channel
// toggles, the three credentials and the three destination strings — with
// credentials stored encrypted. `updated_at` is epoch MILLISECONDS, matching
// the live `Date.now()`.

// UserNotifyConfig reads one user's personal notification channels.
//
// A CORRUPT BLOB READS AS "NOT CONFIGURED" rather than returning an error, which
// is the live behaviour and worth keeping: this is read from inside the alert
// path, where an error would take down delivery for every OTHER recipient too.
// One user's unreadable row must cost that user their alerts and nobody else
// theirs.
func (d *DB) UserNotifyConfig(userID string) (map[string]any, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db: not open")
	}
	if userID == "" {
		return nil, nil
	}
	var data string
	err := d.sql.QueryRow(`SELECT data FROM user_notify_config WHERE user_id = ?`, userID).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		return nil, nil // see above: unreadable is "not configured"
	}
	return out, nil
}

// SetUserNotifyConfig writes one user's channels.
func (d *DB) SetUserNotifyConfig(userID string, data map[string]any) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	if userID == "" {
		return errors.New("db: no user")
	}
	blob, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`
    INSERT INTO user_notify_config (user_id, data, updated_at) VALUES (?, ?, ?)
    ON CONFLICT (user_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at
  `, userID, string(blob), time.Now().UnixMilli())
	return err
}

// RemoveUserNotifyConfig drops a user's channels.
//
// Called when the user is deleted: a personal destination must not outlive the
// account that owns it. A deleted user's stored ntfy URL is still a host this
// server would connect to.
func (d *DB) RemoveUserNotifyConfig(userID string) error {
	if d == nil || d.sql == nil {
		return errors.New("db: not open")
	}
	if userID == "" {
		return nil
	}
	_, err := d.sql.Exec(`DELETE FROM user_notify_config WHERE user_id = ?`, userID)
	return err
}

// ListUserNotifyConfigs returns every user's personal notification channels.
//
// ── THE ALERT FAN-OUT NEEDS ALL OF THEM, NOT ONE ──────────────────────────
//
// `UserNotifyConfig` answers "where does THIS user want alerts", which is what
// the settings page asks. The alert path asks the other question: which users
// have configured a channel at all, so each can then be tested for access to the
// router that fired. Live's `userNotify.recipientsFor` iterates
// `db.listUserNotifyConfigs()` for exactly this.
//
// A CORRUPT BLOB IS SKIPPED, not returned as an error — the same rule
// `UserNotifyConfig` states: "One user's unreadable row must cost that user their
// alerts and nobody else theirs." A single bad row must not stop the fan-out.
func (d *DB) ListUserNotifyConfigs() (map[string]map[string]any, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db: not open")
	}
	rows, err := d.sql.Query(`SELECT user_id, data FROM user_notify_config`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]map[string]any{}
	for rows.Next() {
		var userID, data string
		if err := rows.Scan(&userID, &data); err != nil {
			return nil, err
		}
		if userID == "" {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(data), &cfg); err != nil {
			continue // unreadable is "not configured", per the rule above
		}
		out[userID] = cfg
	}
	return out, rows.Err()
}

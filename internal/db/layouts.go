package db

// Opaque JSON preference blobs, keyed by (user_id, kind) — the port of
// `db.js`'s getLayout/setLayout.
//
// Three kinds exist and the schema CHECK names them: 'dashboard', 'topology'
// and 'nav'. Only `nav` is read and written here so far; the other two belong to
// pages this port has not taken on, and their routes are still proxied.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// SharedLayoutUser is `SHARED_LAYOUT_USER` — the stand-in identity for authMode
// 'none', where there is no user to key on. It also stands in for the old
// unsuffixed `dashboard-layout.json`, which is why the value is not arbitrary.
const SharedLayoutUser = "_shared"

// LayoutUser is `_layoutUser(req)`: the signed-in user, or the shared identity.
func LayoutUser(userID string) string {
	if userID == "" {
		return SharedLayoutUser
	}
	return userID
}

// Layout reads one blob, or nil when there is none.
//
// ── A CORRUPT BLOB IS NIL, NOT AN ERROR ─────────────────────────────────────
//
// The live comment: "A corrupt blob starts the user clean rather than 500ing a
// whole page over a saved card position — the same forgiveness the old file
// readers had." Reproduced, and it matters more here than it reads: the nav
// preference decides whether the sidebar renders at all, so a parse failure that
// propagated would take out the navigation over a stored preference.
func (d *DB) Layout(userID, kind string) (json.RawMessage, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var data string
	err := d.sql.QueryRow(
		`SELECT data FROM user_layouts WHERE user_id = ? AND kind = ?`,
		LayoutUser(userID), kind).Scan(&data)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(data)) {
		return nil, nil
	}
	return json.RawMessage(data), nil
}

// SetLayout writes one blob, replacing whatever was there.
func (d *DB) SetLayout(userID, kind string, data any) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	blob, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO user_layouts (user_id, kind, data, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (user_id, kind)
		 DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		LayoutUser(userID), kind, string(blob), time.Now().UnixMilli())
	return err
}

// DeleteLayouts removes every saved layout for one user.
//
// ── IT IS CLEANUP, AND THE LIVE APP DID NOT HAVE IT FOR A WHILE ────────────
//
// The live comment on the call site: "The JSON files had no cleanup path at all,
// so every deleted user left their dashboard and topology layouts on disk
// indefinitely." Users live in JSON, so there is no foreign key for a delete to
// cascade through — the rows have to be cleared by hand or they sit pointing at
// an id that could later be reused.
//
// Returns the number of rows removed, matching `deleteLayouts`, which returns
// `.changes`. Nothing reads it today; it is what makes a test able to tell "no
// layouts" from "did not run".
func (d *DB) DeleteLayouts(userID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	if userID == "" {
		// `if (!_db || !userId) return 0` — an empty id is not an error, and a
		// query with one would match the `LayoutUser` fallback row rather than
		// nothing. See LayoutUser.
		return 0, nil
	}
	res, err := d.sql.Exec(`DELETE FROM user_layouts WHERE user_id = ?`, LayoutUser(userID))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

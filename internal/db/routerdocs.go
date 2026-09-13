package db

// Operator-owned documents, keyed by router.
//
// ── WHAT BELONGS HERE, AND WHY IT IS NOT A LAYOUT ───────────────────────────
//
// Facts about the SITE that no RouterOS menu holds: which interfaces the
// operator calls uplinks, how the cabling actually runs between devices a
// discovery protocol cannot see through, and where the access points physically
// are. None of the three is derivable from the router, and none of them is a
// matter of taste.
//
// `user_layouts` is the same shape keyed on a PERSON and holds what that person
// likes looking at — where they dragged a card. A second user opening the
// topology must see the same cabling, and a second user opening the Wi-Fi map
// must see the same building, so these are keyed on the router and shared.
//
// ── NO `CHECK (kind IN …)`, DELIBERATELY ────────────────────────────────────
//
// `user_layouts` carries one, and SQLite cannot alter a constraint: adding a
// fourth kind there means rebuilding the table and renaming it into place. The
// kinds are validated in Go instead, by `internal/sitedoc`, which is also where
// each document's shape is defined — one list, one place, and a new kind is a
// line rather than a migration.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mikrodash/internal/sitedoc"
)

// Doc reads one document, or nil when there is none.
//
// A CORRUPT BLOB IS NIL, NOT AN ERROR, for the reason `Layout` gives: a parse
// failure must cost the stored arrangement rather than the page that renders it.
func (d *DB) Doc(routerID, kind string) (json.RawMessage, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	if routerID == "" || !sitedoc.ValidKind(kind) {
		return nil, fmt.Errorf("router doc %q: unknown kind or empty router", kind)
	}
	var data string
	err := d.sql.QueryRow(
		`SELECT data FROM router_docs WHERE router_id = ? AND kind = ?`,
		routerID, kind).Scan(&data)
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

// SetDoc writes one document, replacing whatever was there.
func (d *DB) SetDoc(routerID, kind string, data any) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	if routerID == "" || !sitedoc.ValidKind(kind) {
		return fmt.Errorf("router doc %q: unknown kind or empty router", kind)
	}
	blob, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(
		`INSERT INTO router_docs (router_id, kind, data, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (router_id, kind)
		 DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		routerID, kind, string(blob), time.Now().UnixMilli())
	return err
}

// DeleteRouterDocs removes every document for one router, for when it is removed
// from the fleet.
//
// ── ROUTERS LIVE IN routers.json, SO NOTHING CASCADES ───────────────────────
//
// There is no foreign key for a delete to travel along — the same reason
// `DeleteLayouts` exists for users. Without this, removing a router leaves its
// site plan and its pinned cabling behind, pointing at an id that a later `Add
// Router` could reuse.
func (d *DB) DeleteRouterDocs(routerID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	if routerID == "" {
		return 0, nil
	}
	res, err := d.sql.Exec(`DELETE FROM router_docs WHERE router_id = ?`, routerID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

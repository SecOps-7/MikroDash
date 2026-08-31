package db

// Role writes — create, partial update, delete, and the page matrix.
//
// ── `builtin` IS NOT WRITABLE, AND THAT IS A SECURITY PROPERTY ──────────────
//
// The live comment says it in one line: "Only name and description are mutable;
// `builtin` and `id` are not, so a custom role can never promote itself into the
// structural one." A builtin role holds every KNOWN permission structurally, and
// `GlobalAdminUserIDs` counts a global grant of one as administrator access — so
// a role that could set its own `builtin` flag would be a way to mint an
// administrator from a role nobody thought conferred anything.
//
// `CreateRole` therefore writes a literal 0, and `roleWritableColumns` has two
// entries. Neither is a place to add a third without reading this.

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// roleWritableColumns is what `updateRole` may set, in the live order. See the
// note above before extending it, and `siteWritableColumns` for why a whitelist
// of column names is an injection boundary rather than tidiness.
var roleWritableColumns = []string{"name", "description"}

// GetRole is one role, or (nil, nil) when there is none with that id.
func (d *DB) GetRole(id string) (*RoleRow, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var r RoleRow
	// `description` is NULLABLE; `RoleRow.Description` is already a *string, the
	// shape `principals.go` chose so NULL stays distinguishable from empty.
	var desc sql.NullString
	err := d.sql.QueryRow(
		`SELECT id, name, description, builtin, created_at FROM roles WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &desc, &r.Builtin, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if desc.Valid {
		v := desc.String
		r.Description = &v
	}
	return &r, nil
}

// CreateRole inserts a CUSTOM role — `builtin` is a literal 0, never taken from
// the caller. See the file header.
func (d *DB) CreateRole(cols map[string]any) (*RoleRow, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	name, _ := cols["name"].(string)
	if name == "" {
		return nil, errors.New("db: a role needs a name")
	}
	id, err := newSiteID()
	if err != nil {
		return nil, err
	}
	if _, err := d.sql.Exec(
		`INSERT INTO roles (id, name, description, builtin, created_at) VALUES (?, ?, ?, 0, ?)`,
		id, name, cols["description"], time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	return d.GetRole(id)
}

// UpdateRole writes only the columns actually supplied, and only the two that
// are writable at all.
func (d *DB) UpdateRole(id string, cols map[string]any) (*RoleRow, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	sets := make([]string, 0, len(roleWritableColumns))
	params := make([]any, 0, len(roleWritableColumns)+1)
	for _, col := range roleWritableColumns {
		if v, ok := cols[col]; ok {
			sets = append(sets, col+" = ?")
			params = append(params, v)
		}
	}
	if len(sets) == 0 {
		return d.GetRole(id)
	}
	params = append(params, id)
	if _, err := d.sql.Exec(
		`UPDATE roles SET `+strings.Join(sets, ", ")+` WHERE id = ?`, params...); err != nil {
		return nil, err
	}
	return d.GetRole(id)
}

// DeleteRole refuses on a BUILTIN role and reports whether a row went.
//
// ── THE GRANT CHECK IS THE ENGINE'S, NOT THIS FUNCTION'S ────────────────────
//
// `role_id` is `REFERENCES roles(id) ON DELETE RESTRICT`, so a role still held by
// a grant is refused by SQLite. That is deliberate and is why
// `CountGrantsForRole` exists: the caller asks HOW MANY so it can say so, rather
// than surfacing a bare constraint error. Reproducing the check here as well
// would be a second answer to a question the schema already answers, and the two
// could disagree under concurrency.
func (d *DB) DeleteRole(id string) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	row, err := d.GetRole(id)
	if err != nil {
		return false, err
	}
	// A MISSING role and a BUILTIN one both answer false without an error, as the
	// live function does — the route turns the first into a 404 and the second
	// into a refusal, and it has already looked the role up to tell them apart.
	if row == nil || row.Builtin {
		return false, nil
	}
	res, err := d.sql.Exec(`DELETE FROM roles WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SetRolePages replaces a role's whole page matrix and returns what was written.
//
// ── AN UNKNOWN ACCESS IS DROPPED, NOT REFUSED ───────────────────────────────
//
// The live filter is `p && p.page && (p.access === 'read' || p.access ===
// 'write')`. Anything else — a typo, a null, an access of 'admin' — is skipped
// silently and the rest of the matrix is written. Reproduced rather than
// tightened: refusing the whole save would turn one bad row into a lost edit,
// and the original's choice is visible to the operator as a toggle that did not
// take.
//
// Delete-then-insert in ONE transaction: a failure halfway would leave the role
// holding part of its old matrix and part of its new one, which is a permission
// set nobody chose.
func (d *DB) SetRolePages(roleID string, pages []RolePage) ([]RolePage, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	kept := make([]RolePage, 0, len(pages))
	for _, p := range pages {
		if p.Page == "" || (p.Access != "read" && p.Access != "write") {
			continue
		}
		kept = append(kept, p)
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM role_pages WHERE role_id = ?`, roleID); err != nil {
		return nil, err
	}
	for _, p := range kept {
		if _, err := tx.Exec(
			`INSERT INTO role_pages (role_id, page, access) VALUES (?, ?, ?)`,
			roleID, p.Page, p.Access); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return kept, nil
}

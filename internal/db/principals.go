package db

// The principal graph's READ path: groups, roles, grants and a role's page
// matrix — what the Settings page's Access Management card is drawn from.
//
// The SQL is COPIED from src/db.js rather than reimplemented, for the reason
// backups.go's header gives: both sides run SQLite against the same file, so
// identical query text makes the answers identical by construction rather than
// by comparison. That matters more here than anywhere else in the port — this is
// the table that decides who may do what, and two hand-written queries agree for
// every case anybody thought to try.
//
// Nothing here writes. Node owns every mutation of these tables.

import (
	"database/sql"
	"errors"
)

// Group is one row of `groups`.
type Group struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	CreatedAt   int64   `json:"created_at"`
}

// ListGroups is every group, ordered as the original orders them.
//
// `COLLATE NOCASE` is copied rather than reasoned about; see ListSites for the
// measured note on when that clause is load-bearing and when the column's own
// declaration is already doing the work.
func (d *DB) ListGroups() ([]Group, error) {
	if d == nil || d.sql == nil {
		return []Group{}, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT id, name, description, created_at FROM groups ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return []Group{}, err
	}
	defer rows.Close()

	out := []Group{}
	for rows.Next() {
		var g Group
		var desc sql.NullString
		if err := rows.Scan(&g.ID, &g.Name, &desc, &g.CreatedAt); err != nil {
			return []Group{}, err
		}
		if desc.Valid {
			v := desc.String
			g.Description = &v
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GroupMembers is the user ids in one group.
//
// EMPTY IS A REAL ANSWER, not an error: a group with nobody in it is an ordinary
// state, and the card shows it as such.
func (d *DB) GroupMembers(groupID string) ([]string, error) {
	if d == nil || d.sql == nil {
		return []string{}, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT user_id FROM group_members WHERE group_id = ?`, groupID)
	if err != nil {
		return []string{}, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return []string{}, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RoleRow is a role as the ACCESS MANAGEMENT CARD needs it.
//
// NOT the existing `Role`, for the same reason `GrantRow` is not `Grant`: that
// one is the resolver's view — id, builtin, pages — and is read on every
// permission check. This one carries the name, description and timestamp the
// card renders and the resolver has no use for.
//
// Builtin is a BOOL here and an INTEGER in SQLite; the original converts with
// `builtin: !!r.builtin` in `_roleView`, and the card uses it to refuse a delete.
type RoleRow struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Builtin     bool    `json:"builtin"`
	CreatedAt   int64   `json:"created_at"`
}

// ListRoles is every role, BUILT-IN ONES FIRST and then by name.
//
// `ORDER BY builtin DESC, name COLLATE NOCASE` — the seeded roles are what an
// operator picks from most of the time, so they sort to the top regardless of
// what a custom role is called.
func (d *DB) ListRoles() ([]RoleRow, error) {
	if d == nil || d.sql == nil {
		return []RoleRow{}, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT id, name, description, builtin, created_at FROM roles
                ORDER BY builtin DESC, name COLLATE NOCASE`)
	if err != nil {
		return []RoleRow{}, err
	}
	defer rows.Close()

	out := []RoleRow{}
	for rows.Next() {
		var r RoleRow
		var desc sql.NullString
		var builtin int
		if err := rows.Scan(&r.ID, &r.Name, &desc, &builtin, &r.CreatedAt); err != nil {
			return []RoleRow{}, err
		}
		if desc.Valid {
			v := desc.String
			r.Description = &v
		}
		r.Builtin = builtin != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// RolePages is a role's matrix, ordered by page. `RolePage` is declared in
// db.go, where the resolver already uses it — one shape, two readers.
//
// ── THE `ORDER BY page` IS REDUNDANT, AND KEPT ANYWAY ──────────────────────
//
// Measured, not assumed: removing it leaves the order identical and the test
// still passes. `role_pages` is `PRIMARY KEY (role_id, page)`, so SQLite answers
// `WHERE role_id = ?` from that index and page order falls out of the scan.
//
// It stays because it is what the original writes, and because the alternative
// is a query whose ordering depends silently on an index this package does not
// own and must never migrate — the same reasoning as ListSites' COLLATE clause.
// What the test DOES catch is a wrong order, not a missing clause.
func (d *DB) RolePages(roleID string) ([]RolePage, error) {
	if d == nil || d.sql == nil {
		return []RolePage{}, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT page, access FROM role_pages WHERE role_id = ? ORDER BY page`, roleID)
	if err != nil {
		return []RolePage{}, err
	}
	defer rows.Close()

	out := []RolePage{}
	for rows.Next() {
		var p RolePage
		if err := rows.Scan(&p.Page, &p.Access); err != nil {
			return []RolePage{}, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountGrantsForRole is how many grants name a role — what stops the card
// offering to delete one that is in use.
func (d *DB) CountGrantsForRole(roleID string) (int, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	var n int
	err := d.sql.QueryRow(`SELECT COUNT(*) AS n FROM grants WHERE role_id = ?`, roleID).Scan(&n)
	return n, err
}

// GrantRow is a grant as the ACCESS MANAGEMENT CARD needs it: every column.
//
// ── DELIBERATELY NOT THE EXISTING `Grant` ──────────────────────────────────
//
// `Grant` in db.go carries three fields — role, scope type, scope id — because
// that is all the RESOLVER needs to answer "may this principal do X here". This
// listing needs nine, including who created the grant and when.
//
// Widening `Grant` to serve both would put six unused fields in the hot path of
// every permission check, and — the reason that actually matters — would make a
// query the security decision depends on carry columns nothing verifies. Two
// types, two questions, the same table.
//
// `Role` is the LEGACY name column, kept because rows written before the role
// table existed still carry it and the original still selects it.
type GrantRow struct {
	// A TEXT UUID, not an integer. `grants.id` is `TEXT PRIMARY KEY` and the
	// live `upsertGrant` fills it with `crypto.randomUUID()`. This was `int64`
	// until 2026-08-26, and `ListGrants` could not scan a single real row —
	// "converting driver.Value type string to a int64". Every test fixture in
	// this repo had declared `id INTEGER PRIMARY KEY AUTOINCREMENT`, which is not
	// the shape the migrations leave on disk, so the type scanned happily against
	// all of them. `TestGrantsScanAgainstTheRealSchema` uses the real DDL.
	ID            string  `json:"id"`
	PrincipalType string  `json:"principal_type"`
	PrincipalID   string  `json:"principal_id"`
	RoleID        *string `json:"role_id"`
	Role          *string `json:"role"`
	ScopeType     string  `json:"scope_type"`
	// A POINTER even though the column is `NOT NULL DEFAULT ''`. A global grant
	// stores an EMPTY STRING, not NULL, and the live schema says why: "SQLite
	// treats NULLs as distinct in a UNIQUE index, so storing NULL here would let
	// one principal hold two global grants and the constraint below would
	// silently never fire." The pointer therefore never arrives nil from a
	// migrated database — it is kept because this reads rows the port does not
	// write, and a hand-repaired row is not worth crashing over.
	ScopeID   *string `json:"scope_id"`
	CreatedAt int64   `json:"created_at"`
	CreatedBy *string `json:"created_by"`
}

func scanGrants(rows *sql.Rows) ([]GrantRow, error) {
	out := []GrantRow{}
	for rows.Next() {
		var g GrantRow
		var roleID, role, scopeID, createdBy sql.NullString
		if err := rows.Scan(&g.ID, &g.PrincipalType, &g.PrincipalID, &roleID, &role,
			&g.ScopeType, &scopeID, &g.CreatedAt, &createdBy); err != nil {
			return []GrantRow{}, err
		}
		for _, p := range []struct {
			src sql.NullString
			dst **string
		}{{roleID, &g.RoleID}, {role, &g.Role}, {scopeID, &g.ScopeID}, {createdBy, &g.CreatedBy}} {
			if p.src.Valid {
				v := p.src.String
				*p.dst = &v
			}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GrantFilter narrows a grant listing. An empty field is NOT a filter — the
// original builds its WHERE from whichever keys are truthy, so a blank principal
// id lists every principal rather than none.
type GrantFilter struct {
	PrincipalType string
	PrincipalID   string
	ScopeType     string
	ScopeID       string
}

// ListGrants is the grant rows, filtered and ordered as the original orders
// them.
//
// ORDERED BY created_at, which is not the same as ordering by id: two grants
// written in one transaction share a timestamp, and SQLite is then free to
// return them in either order. The original has the same property; reproducing
// the ORDER BY reproduces it rather than inventing a tiebreak the live app does
// not have.
func (d *DB) ListGrants(f GrantFilter) ([]GrantRow, error) {
	if d == nil || d.sql == nil {
		return []GrantRow{}, errors.New("db not open")
	}
	where := ""
	args := []any{}
	add := func(col, val string) {
		if val == "" {
			return
		}
		if where == "" {
			where = " WHERE " + col + " = ?"
		} else {
			where += " AND " + col + " = ?"
		}
		args = append(args, val)
	}
	add("principal_type", f.PrincipalType)
	add("principal_id", f.PrincipalID)
	add("scope_type", f.ScopeType)
	add("scope_id", f.ScopeID)

	rows, err := d.sql.Query(`SELECT id, principal_type, principal_id, role_id, role, scope_type, scope_id, created_at, created_by
                FROM grants`+where+`
                ORDER BY created_at`, args...)
	if err != nil {
		return []GrantRow{}, err
	}
	defer rows.Close()
	return scanGrants(rows)
}

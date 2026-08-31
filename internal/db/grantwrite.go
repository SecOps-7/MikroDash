package db

// Grant writes — the upsert, and the two deletes keyed on a principal.
//
// `DeleteGrantsForScope` is NOT here: it already lives in `purge.go`, where
// removing a router or a site needs it, and one grant remover is enough.
//
// ── THE UPSERT IS KEYED ON PRINCIPAL AND SCOPE, NOT ON ID ───────────────────
//
// `ON CONFLICT (principal_type, principal_id, scope_type, scope_id)` — so
// changing what a principal holds AT A SCOPE replaces the existing grant rather
// than stacking a second one. The live comment says why that matters: two rows
// for one principal at one scope would have to be resolved later, and nothing
// downstream resolves them.
//
// ── AND A CUSTOM ROLE MIRRORS AS THE LEAST-PRIVILEGED LEGACY VALUE ──────────
//
// `grants.role` is a mirror of the role id, read only by a downgraded (v6)
// binary that predates the roles table. A custom role has no legacy equivalent,
// so it mirrors as `viewer` — the live comment: "a downgrade must not grant more
// than it should". Mirroring a custom role as `admin` because it happens to
// confer a lot would hand a rolled-back install more access than anybody
// granted.

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// legacyRoleIDs maps the pre-roles-table role NAMES onto role ids, for a caller
// that has not migrated. `_ROLE_ID_BY_LEGACY` in the original.
var legacyRoleIDs = map[string]string{
	"admin":    "administrator",
	"operator": "operator",
	"viewer":   "readonly",
}

// legacyByRoleID is the reverse, for the `role` mirror column.
// `_LEGACY_BY_ROLE_ID` in the original.
var legacyByRoleID = map[string]string{
	"administrator": "admin",
	"operator":      "operator",
	"readonly":      "viewer",
}

// LegacyMirror is the legacy role string to write into `grants.role`.
//
// UNKNOWN MIRRORS AS `viewer`, NOT AS THE ROLE'S OWN NAME. A custom role has no
// legacy equivalent and only a downgraded binary reads this column, so the
// least-privileged value is the only safe answer. Exported because the rule is
// worth being able to test directly.
func LegacyMirror(roleID string) string {
	if v, ok := legacyByRoleID[roleID]; ok {
		return v
	}
	return "viewer"
}

// GrantSpec is one grant as a caller describes it.
type GrantSpec struct {
	PrincipalType string // "user" | "group"
	PrincipalID   string
	// RoleID wins when set. Role is the legacy NAME, accepted from a caller that
	// has not migrated; neither recognised falls back to `readonly`.
	RoleID    string
	Role      string
	ScopeType string // "global" | "site" | "router"
	ScopeID   string
	CreatedBy string
}

// resolveRoleID is `roleId || _ROLE_ID_BY_LEGACY[role] || 'readonly'`.
//
// The final fallback is LEAST PRIVILEGE and not an error, matching the original:
// a grant naming a role nobody recognises confers the readonly role rather than
// failing the request, and the operator sees what they got.
func resolveRoleID(s GrantSpec) string {
	if s.RoleID != "" {
		return s.RoleID
	}
	if v, ok := legacyRoleIDs[s.Role]; ok {
		return v
	}
	return "readonly"
}

// scopeIDFor is `scopeType === 'global' ? ” : String(scopeId || ”)`.
//
// A GLOBAL GRANT STORES AN EMPTY STRING, never NULL and never a leftover id. The
// live schema explains the first half — SQLite treats NULLs as distinct in a
// UNIQUE index, so a NULL would let one principal hold two global grants and the
// constraint would silently never fire. The second half is this function: a
// caller sending `{scopeType: 'global', scopeId: 'r1'}` must not create a global
// grant that also carries a router id, because the CHECK constraint refuses it
// and, worse, a looser schema would store a row nothing can interpret.
func scopeIDFor(s GrantSpec) string {
	if s.ScopeType == "global" {
		return ""
	}
	return s.ScopeID
}

// UpsertGrant writes a grant, REPLACING whatever that principal held at that
// scope.
func (d *DB) UpsertGrant(s GrantSpec) error {
	if d == nil || d.sql == nil {
		return errors.New("db not open")
	}
	return upsertGrant(d.sql, s)
}

// UpsertGrantTx is the same write inside a caller's transaction, so
// `WouldOrphanGlobalAdmin` can probe a whole grant PROJECTION rather than a
// single delete.
//
// That is what `Rbac.wouldOrphanGlobalAdmin(() => Rbac.syncUserGrants(updated))`
// does on the live side, and it is not the same question as probing a delete:
// `syncUserGrants` deletes every grant the principal holds and then rebuilds
// them, so whether administration survives depends on what the REBUILD puts
// back. A probe that only ran the delete would refuse an edit that was about to
// restore the grant it removed.
func UpsertGrantTx(tx *sql.Tx, s GrantSpec) error {
	return upsertGrant(tx, s)
}

// execer is the half of *sql.DB and *sql.Tx this write needs.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertGrant(x execer, s GrantSpec) error {
	if strings.TrimSpace(s.PrincipalID) == "" || strings.TrimSpace(s.PrincipalType) == "" {
		return errors.New("db: a grant needs a principal")
	}
	roleID := resolveRoleID(s)
	id, err := newSiteID()
	if err != nil {
		return err
	}
	var createdBy any
	if s.CreatedBy != "" {
		createdBy = s.CreatedBy
	}
	_, err = x.Exec(`INSERT INTO grants
	    (id, principal_type, principal_id, role_id, role, scope_type, scope_id,
	     created_at, created_by)
	  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	  ON CONFLICT (principal_type, principal_id, scope_type, scope_id)
	  DO UPDATE SET role_id = excluded.role_id, role = excluded.role,
	                created_at = excluded.created_at, created_by = excluded.created_by`,
		id, s.PrincipalType, s.PrincipalID, roleID, LegacyMirror(roleID),
		s.ScopeType, scopeIDFor(s), time.Now().UnixMilli(), createdBy)
	return err
}

// DeleteGrant removes one grant by id and reports whether a row went.
func (d *DB) DeleteGrant(id string) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	res, err := d.sql.Exec(`DELETE FROM grants WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteGrantTx is the same delete inside a caller's transaction.
//
// `DELETE /api/grants/:id` is the most direct way to orphan the last
// administrator — it is literally "remove the grant that confers
// administration", not a side effect of some other edit — so it is the one place
// the probe is obvious. It still needs this seam, because
// `WouldOrphanGlobalAdmin` runs the mutation and rolls back.
func DeleteGrantTx(tx *sql.Tx, id string) error {
	_, err := tx.Exec(`DELETE FROM grants WHERE id = ?`, id)
	return err
}

// DeleteGrantsForPrincipal removes every grant a principal holds, and reports
// how many.
//
// KEYED ON THE TYPE AS WELL AS THE ID. Ids are opaque strings and nothing stops
// a group sharing a user's id, so a delete on the id alone would revoke a
// person's access along with a group's. The same rule as
// `DeleteGrantsForScope`'s, and it has already been caught once in this port.
func (d *DB) DeleteGrantsForPrincipal(principalType, principalID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	res, err := d.sql.Exec(
		`DELETE FROM grants WHERE principal_type = ? AND principal_id = ?`,
		principalType, principalID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteGrantsForPrincipalTx is the same delete inside a caller's transaction.
//
// `WouldOrphanGlobalAdmin` takes a `func(*sql.Tx) error` so it can run a
// mutation, ask whether any global administrator survives, and roll back. That
// probe is the ONLY correct way to answer "would deleting this user lock
// everybody out of administration":
//
//   - counting user records with `role === 'admin'` cannot see an administrator
//     whose grant is held through a GROUP, and counts one who was demoted in the
//     grant editor;
//   - and the user record lives in JSON, which cannot join the transaction — so
//     it is the GRANT deletion that gets probed, which is exactly what
//     `GlobalAdminUserIDs` reads.
//
// Same keying as the non-transactional form, for the same reason: ids are opaque
// strings and nothing stops a group sharing a user's id.
func DeleteGrantsForPrincipalTx(tx *sql.Tx, principalType, principalID string) error {
	_, err := tx.Exec(
		`DELETE FROM grants WHERE principal_type = ? AND principal_id = ?`,
		principalType, principalID)
	return err
}

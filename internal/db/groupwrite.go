package db

// Group writes — create, partial update, delete, and membership.
//
// ── TWO THINGS HERE ARE TRANSACTIONS FOR A STATED REASON ────────────────────
//
// `DeleteGroup` removes the group's GRANTS and the group in one transaction.
// Memberships cascade through a foreign key; the grants cannot, because
// `principal_id` is polymorphic and no key can point at two tables. The live
// comment says it plainly: "a group can never outlive its grants or vice versa".
// A half-applied delete leaves grants whose principal does not exist — invisible
// in every card, and still consulted by the resolver.
//
// `SetGroupMembers` replaces the whole list, so the delete and the inserts have
// to be one unit or a failure halfway empties the group.

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// groupWritableColumns is what `updateGroup` may set, in the live order.
//
// A WHITELIST, for the same reason as `siteWritableColumns`: a column name goes
// into SQL TEXT because an identifier cannot be parameterised, so this list is
// the injection boundary rather than a tidiness rule.
var groupWritableColumns = []string{"name", "description"}

// GetGroup is one group, or (nil, nil) when there is none with that id.
//
// A MISSING GROUP IS NOT AN ERROR: the caller answers 404 for nil and 500 for
// err, and collapsing them would turn a broken database into "No such group".
func (d *DB) GetGroup(id string) (*Group, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	var g Group
	// `description` is NULLABLE and `Group.Description` is a `*string` — the
	// type `principals.go` already chose, which keeps NULL distinguishable from
	// an empty string. Scanning a nullable column into a plain `string` is the
	// bug `GetSite` shipped with for three ticks; here the type prevents it.
	var desc sql.NullString
	err := d.sql.QueryRow(
		`SELECT id, name, description, created_at FROM groups WHERE id = ?`, id).
		Scan(&g.ID, &g.Name, &desc, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if desc.Valid {
		v := desc.String
		g.Description = &v
	}
	return &g, nil
}

// CreateGroup inserts a group and returns it as stored.
//
// `id` and `created_at` are minted HERE, never taken from a caller — the id is
// what every grant and membership row names, so a caller choosing it could reuse
// the id of a group just deleted and inherit its grants.
func (d *DB) CreateGroup(cols map[string]any) (*Group, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	name, _ := cols["name"].(string)
	if name == "" {
		return nil, errors.New("db: a group needs a name")
	}
	id, err := newSiteID()
	if err != nil {
		return nil, err
	}
	// `description` defaults to NULL when absent, matching the live signature's
	// `description = null`. Absent-versus-null only means something on an UPDATE,
	// where there is an existing value to leave alone.
	if _, err := d.sql.Exec(
		`INSERT INTO groups (id, name, description, created_at) VALUES (?, ?, ?, ?)`,
		id, name, cols["description"], time.Now().UnixMilli()); err != nil {
		return nil, err
	}
	return d.GetGroup(id)
}

// UpdateGroup writes only the columns actually supplied.
//
// An EMPTY map returns the group unchanged rather than erroring: a body whose
// every field was absent is a legitimate request that has nothing to say.
func (d *DB) UpdateGroup(id string, cols map[string]any) (*Group, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	sets := make([]string, 0, len(groupWritableColumns))
	params := make([]any, 0, len(groupWritableColumns)+1)
	for _, col := range groupWritableColumns {
		if v, ok := cols[col]; ok {
			sets = append(sets, col+" = ?")
			params = append(params, v)
		}
	}
	if len(sets) == 0 {
		return d.GetGroup(id)
	}
	params = append(params, id)
	if _, err := d.sql.Exec(
		`UPDATE groups SET `+strings.Join(sets, ", ")+` WHERE id = ?`, params...); err != nil {
		return nil, err
	}
	return d.GetGroup(id)
}

// DeleteGroup removes a group AND its grants, in one transaction, and reports
// whether a row went.
//
// The grants go first and explicitly. Memberships cascade through their foreign
// key; the grants cannot, because `principal_id` is polymorphic.
func (d *DB) DeleteGroup(id string) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// ONCE. An earlier edit called this twice — the second call deletes nothing,
	// so it reported `false` for a group it had just removed, and the route
	// would have answered 404 after a successful delete.
	n, err := deleteGroupTx(tx, id)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return n > 0, nil
}

// DeleteGroupTx is the same work inside a caller's transaction, so
// `WouldOrphanGlobalAdmin` can ask what deleting a group WOULD do.
//
// Deleting a group takes its GRANTS with it, which is why this is one of the
// ways to orphan the last administrator: nobody's account is touched and nobody
// is removed from anything, but a global admin grant held THROUGH that group
// stops conferring. A probe that only watched user grants would not see it.
func DeleteGroupTx(tx *sql.Tx, id string) error {
	_, err := deleteGroupTx(tx, id)
	return err
}

func deleteGroupTx(tx *sql.Tx, id string) (int64, error) {
	if _, err := tx.Exec(
		`DELETE FROM grants WHERE principal_type = 'group' AND principal_id = ?`,
		id); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM groups WHERE id = ?`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SetGroupMembers replaces the whole membership list and returns what was
// written.
//
// ── DEDUPED, AND THE FIRST OCCURRENCE KEEPS ITS POSITION ────────────────────
//
// `Array.from(new Set(...))` — a user listed twice is one member, and the return
// value is what the caller reports. There is no unique index to catch it, so the
// Groups card would simply count the person twice.
//
// REPLACES rather than merges: the caller sends the list it wants, and a member
// removed from it is removed. That is what makes emptying a group one of the
// ways to orphan the last administrator — see `WouldOrphanGlobalAdmin`.
func (d *DB) SetGroupMembers(groupID string, userIDs []string) ([]string, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	ids := dedupe(userIDs)

	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := setGroupMembersTx(tx, groupID, ids); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// setGroupMembersTx is the same work inside a caller's transaction, so
// `WouldOrphanGlobalAdmin` can ask what a membership change WOULD do without
// applying it. That is the least obvious of the ways to orphan the last
// administrator, and it needs this seam to be checkable at all.
func setGroupMembersTx(tx *sql.Tx, groupID string, ids []string) error {
	if _, err := tx.Exec(`DELETE FROM group_members WHERE group_id = ?`, groupID); err != nil {
		return err
	}
	for _, uid := range ids {
		if _, err := tx.Exec(
			`INSERT INTO group_members (group_id, user_id) VALUES (?, ?)`,
			groupID, uid); err != nil {
			return err
		}
	}
	return nil
}

// SetGroupMembersTx exposes the transactional form for a caller that is already
// probing — `WouldOrphanGlobalAdmin` takes a `func(*sql.Tx) error`.
func SetGroupMembersTx(tx *sql.Tx, groupID string, userIDs []string) error {
	return setGroupMembersTx(tx, groupID, dedupe(userIDs))
}

// dedupe is `Array.from(new Set(ids.map(String)))`: order preserved, first
// occurrence wins.
func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

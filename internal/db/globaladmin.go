package db

// Who still has administrator access — and whether a proposed change would
// leave nobody.
//
// ── THIS IS THE LAST-ADMINISTRATOR GUARD, AND IT HAS SIX CALLERS ────────────
//
// In the live app: editing a user's grants, deleting a user, emptying a group's
// membership, deleting a group, deleting a grant, and deleting a role. The live
// comment calls the membership one "one of the five ways to orphan the last
// administrator, and the least obvious", which is fair — nothing about editing a
// membership list looks like removing an administrator.
//
// ── WRONG IN EITHER DIRECTION IS BAD IN A DIFFERENT WAY ─────────────────────
//
// Too FEW admins counted and the app refuses a legitimate change permanently,
// with no way round it short of editing the database by hand. Too MANY and it
// hands out the last administrator's access and locks everybody out. Neither is
// recoverable through the UI, which is why the query is pinned against the live
// one by the global-admin corpus rather than read and reimplemented.

import (
	"database/sql"
	"errors"
)

// globalAdminQuery is the live `globalAdminUserIds` SQL, verbatim.
//
// COPIED RATHER THAN REWRITTEN, deliberately. It is short enough to read and
// subtle enough to get wrong: the UNION covers grants held DIRECTLY and grants
// held THROUGH A GROUP, `builtin = 1` excludes custom roles however permissive
// they look, and `DISTINCT` is what stops somebody holding both routes counting
// twice. `TestGlobalAdminMatchesLive` runs it against the corpus the original
// produced, and also asserts this text still matches the original.
const globalAdminQuery = `
    SELECT DISTINCT uid FROM (
      SELECT principal_id AS uid FROM grants
       WHERE principal_type = 'user'  AND scope_type = 'global'
         AND role_id IN (SELECT id FROM roles WHERE builtin = 1)
      UNION
      SELECT gm.user_id AS uid FROM grants g
        JOIN group_members gm ON gm.group_id = g.principal_id
       WHERE g.principal_type = 'group' AND g.scope_type = 'global'
         AND g.role_id IN (SELECT id FROM roles WHERE builtin = 1)
    )
  `

// GlobalAdminUserIDs is every user who holds a BUILTIN role at global scope,
// directly or through a group.
//
// "Builtin", not "can administer": a custom role may confer everything today and
// be edited to confer nothing tomorrow, so counting it would leave the last real
// administrator removable.
func (d *DB) GlobalAdminUserIDs() ([]string, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db not open")
	}
	return globalAdmins(d.sql.Query)
}

func globalAdmins(query func(string, ...any) (*sql.Rows, error)) ([]string, error) {
	rows, err := query(globalAdminQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// WouldOrphanGlobalAdmin runs `mutate` and reports whether it would leave nobody
// with administrator access — WITHOUT applying it.
//
// ── THE MUTATION REALLY RUNS, AND IS THEN ROLLED BACK ───────────────────────
//
// The live version does the same, and its comment says why in six words: "always
// unwind: this is a question, not a change". The alternative — predicting the
// effect of a delete without performing it — means reimplementing each caller's
// change inside the guard, and the guard would then be checking a model of the
// mutation rather than the mutation.
//
// Go has no exception to unwind with, so the rollback is explicit and
// unconditional: this function NEVER commits. A caller that wants the change
// applies it itself afterwards.
//
// ── AN ERROR IS NOT "NO" ────────────────────────────────────────────────────
//
// A failure to answer is returned, never swallowed into `false`. Reporting
// "this would not orphan anybody" because the query failed is how the last
// administrator gets removed by the check that was supposed to prevent it.
func (d *DB) WouldOrphanGlobalAdmin(mutate func(*sql.Tx) error) (bool, error) {
	if d == nil || d.sql == nil {
		return false, errors.New("db not open")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return false, err
	}
	// UNCONDITIONAL, and never a commit on any path. The rollback is the point
	// of the function, not its error handling.
	defer func() { _ = tx.Rollback() }()

	if err := mutate(tx); err != nil {
		return false, err
	}
	admins, err := globalAdmins(tx.Query)
	if err != nil {
		return false, err
	}
	return len(admins) == 0, nil
}

package db

import (
	"sort"

	"mikrodash/internal/pages"
)

// RenamePageGrants moves `role_pages` rows naming a former page key onto the key
// that replaced it, and reports how many it moved.
//
// ── WHY THIS IS NOT IN Open ─────────────────────────────────────────────────
//
// It writes. `cmd/compat` opens a real /data through a READ-ONLY mount to check
// this build against it, and a write in Open would turn that gate into a failure
// on the one path that must never modify what it inspects. The app calls this
// explicitly at startup instead; every other opener gets the database untouched.
//
// ── WHY IT IS NOT A MIGRATION ───────────────────────────────────────────────
//
// This package deliberately owns no migration machinery -- `Open` refuses a
// database below MinSchema and says the Node app owns migrations. That sentence
// outlived Node, which is gone; but the answer is still not to grow a migration
// framework for one map. This is idempotent, runs in milliseconds against a
// table with a handful of rows, and converges: after the first run it matches
// nothing and does nothing.
func (d *DB) RenamePageGrants() (int, error) {
	if d == nil || d.sql == nil {
		return 0, nil
	}

	// Sorted so the work is deterministic. Map order would make a log line that
	// differs run to run for no reason.
	olds := make([]string, 0, len(pages.Renamed))
	for old := range pages.Renamed {
		olds = append(olds, old)
	}
	sort.Strings(olds)

	moved := 0
	for _, old := range olds {
		// UPDATE OR IGNORE, because role_pages is PRIMARY KEY (role_id, page): a
		// role holding BOTH the old key and the new one would collide. Ignoring
		// keeps the grant the role already had under the new name -- which is the
		// one an administrator chose most recently -- and the sweep below then
		// clears the stale row either way.
		res, err := d.sql.Exec(`UPDATE OR IGNORE role_pages SET page = ? WHERE page = ?`,
			pages.Renamed[old], old)
		if err != nil {
			return moved, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return moved, err
		}
		moved += int(n)

		if _, err := d.sql.Exec(`DELETE FROM role_pages WHERE page = ?`, old); err != nil {
			return moved, err
		}
	}
	return moved, nil
}

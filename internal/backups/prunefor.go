package backups

// The retention sweep: which stored pairs go, and making them go.
//
// This is the join between prune.go (the pure selector — decides which stems,
// touches nothing) and store.go (RemovePair — filesystem only). Neither knows
// about the database; this does, and until it existed neither had a caller.
//
// ── DRIVEN BY THE DATABASE, NOT BY A DIRECTORY LISTING ──────────────────────
//
// Rows are the record of what MikroDash made, and A FILE IT DID NOT MAKE IS NOT
// ITS TO DELETE. Sweeping the directory instead would eventually remove an
// operator's own file that happened to sit there — the same shape as ToDo item
// 13, where a stray file already gets counted as a backup by ListPairs.
//
// ── A FAILURE TO REMOVE ONE PAIR DOES NOT STOP THE SWEEP ────────────────────
//
// The next pair is still over the limit, and a sweep that aborts on the first
// locked file leaves a directory that grows without bound while reporting an
// error nobody reads. Each failure is logged and skipped.

import "fmt"

// PruneStore is the half of the database this needs. An interface so the sweep
// can be tested without one.
type PruneStore interface {
	StoredBackupsFor(routerID string) ([]StoredPair, error)
	MarkPruned(id int64, ts int64) (bool, error)
}

// StoredPair is one row retention can act on.
//
// Dir comes from the ROW rather than from re-deriving a slug: labels change, and
// the row records where the pair was actually written.
type StoredPair struct {
	ID   int64
	Stem string
	Dir  string
	// Bytes is both halves, as the page reports them.
	Bytes int64
}

// PruneFor removes pairs beyond the router's retention and says how many went.
//
// `now` is passed rather than read, so a test does not depend on the clock and
// so every pair in one sweep is judged against the SAME instant — a sweep that
// re-read the clock per pair could keep one and drop its neighbour on a
// boundary.
func PruneFor(s PruneStore, routerID string, r Retention, now int64, log func(string)) (int, error) {
	if log == nil {
		log = func(string) {}
	}
	// NEITHER LIMIT SET MEANS NOTHING IS PRUNED, and the check is up front: with
	// both at zero SelectForPruning returns nothing anyway, but reading the rows
	// to discover that is a query per router per tick for a fleet that has opted
	// out of retention entirely.
	if r.KeepCount <= 0 && r.KeepDays <= 0 {
		return 0, nil
	}

	rows, err := s.StoredBackupsFor(routerID)
	if err != nil {
		return 0, err
	}
	pairs := make([]Pair, 0, len(rows))
	for _, x := range rows {
		pairs = append(pairs, Pair{Stem: x.Stem, BackupBytes: x.Bytes})
	}
	doomed := map[string]bool{}
	for _, stem := range SelectForPruning(pairs, r, now) {
		doomed[stem] = true
	}
	if len(doomed) == 0 {
		return 0, nil
	}

	removed := 0
	for _, row := range rows {
		if !doomed[row.Stem] {
			continue
		}
		if _, err := RemovePair(row.Dir, row.Stem); err != nil {
			log(fmt.Sprintf("could not prune %s: %v", row.Stem, err))
			continue
		}
		// THE ROW IS MARKED ONLY AFTER THE FILES ARE GONE. The other order would
		// leave a row claiming its artefacts were pruned while they are still on
		// disk, and nothing would ever try again — the sweep only considers rows
		// whose pruned_at is null.
		if _, err := s.MarkPruned(row.ID, now); err != nil {
			log(fmt.Sprintf("pruned %s but could not record it: %v", row.Stem, err))
			continue
		}
		removed++
	}
	if removed > 0 {
		log(fmt.Sprintf("pruned %d backup pair(s)", removed))
	}
	return removed, nil
}

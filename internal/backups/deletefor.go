package backups

// Deleting restore points on an operator's instruction.
//
// ── NOT THE SAME ACT AS RETENTION ───────────────────────────────────────────
//
// prunefor.go removes files and KEEPS the row, so the History table can explain
// the disappearance. This removes the row too, because an operator saying "I do
// not want this listed" is not asking for a tombstone.
//
// The trail is not lost: `audit_events` records the delete independently, and
// that table is deliberately absent from PURGE_TABLES and deleteRouterData().
// The audit entry IS the surviving record, which is why the note says so.
//
// ── FILES FIRST, THEN THE ROW ───────────────────────────────────────────────
//
// Drop the row first and fail the unlink, and several MB are orphaned on disk
// with nothing left pointing at them — nothing lists it, nothing prunes it, and
// only a human reading the directory would ever find it.
//
// ── A ROW WITH NO FILES IS STILL THE OPERATOR'S TO REMOVE ───────────────────
//
// A run that stored nothing because the configuration was unchanged, or one
// whose pair retention already took, has no pair to unlink and is still a row in
// their list. Refusing those would leave rows nothing can ever clear.

import "fmt"

// DeleteStore is the half of the database this needs.
type DeleteStore interface {
	// RowFor returns a row only if it belongs to this router. Nil for a row on
	// another router or one that does not exist — the two are the same answer
	// from outside.
	RowFor(id int64, routerID string) *DeletableRow
	DeleteRow(id int64) (bool, error)
}

// DeletableRow is what the sweep needs to know about one row.
type DeletableRow struct {
	ID     int64
	Stem   string
	Dir    string
	Pruned bool
}

// MaxDeletePerRequest bounds one message. ONE MESSAGE MUST NOT BE ABLE TO ASK
// FOR UNBOUNDED FILESYSTEM WORK — the ids come from a browser, and a selection
// is at most a page of rows.
const MaxDeletePerRequest = 200

// NormalizeIDs is `[...new Set(raw.map(Number).filter(Number.isInteger))].slice(0, 200)`.
//
// DE-DUPLICATED as well as bounded: the same id twice is one delete and two
// chances to log a failure for a row that is already gone.
func NormalizeIDs(raw []any) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, v := range raw {
		n, ok := wholeNumber(v)
		if !ok || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) == MaxDeletePerRequest {
			break
		}
	}
	return out
}

// wholeNumber is `Number.isInteger(Number(v))`: a fractional or unparseable
// value is dropped rather than truncated. An id is not a quantity, so rounding
// one would address a DIFFERENT row.
func wholeNumber(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		if t != float64(int64(t)) {
			return 0, false
		}
		return int64(t), true
	case string:
		f, ok := plainFloat(t)
		if !ok || f != float64(int64(f)) {
			return 0, false
		}
		return int64(f), true
	}
	return 0, false
}

// DeleteFor removes the named restore points and reports how many went.
//
// A row that is not ours, or already gone, is SKIPPED SILENTLY: a selection that
// raced a retention sweep is not an error worth showing.
func DeleteFor(s DeleteStore, routerID string, ids []int64, fallbackDir string,
	log func(string)) (removed []int64, failed int) {

	if log == nil {
		log = func(string) {}
	}
	for _, id := range ids {
		row := s.RowFor(id, routerID)
		if row == nil {
			continue
		}
		dir := row.Dir
		if dir == "" {
			dir = fallbackDir
		}
		// Only unlink when there is a pair to unlink. A pruned row's files are
		// already gone, and a run that stored nothing never had any.
		if row.Stem != "" && !row.Pruned {
			if _, err := RemovePair(dir, row.Stem); err != nil {
				failed++
				log(fmt.Sprintf("could not delete %s: %v", row.Stem, err))
				continue
			}
		}
		if _, err := s.DeleteRow(row.ID); err != nil {
			failed++
			log(fmt.Sprintf("removed the files for %s but could not remove the row: %v",
				row.Stem, err))
			continue
		}
		removed = append(removed, row.ID)
	}
	return removed, failed
}

// plainFloat parses a decimal number the way `Number()` does. Reports false for
// anything that is not a number at all — `Number("3x")` is NaN, not 3.
func plainFloat(s string) (float64, bool) {
	s = trimSpace(s)
	if s == "" {
		return 0, false
	}
	var f float64
	var neg bool
	i := 0
	if s[i] == '+' || s[i] == '-' {
		neg = s[i] == '-'
		i++
	}
	seen, frac, scale := false, false, 1.0
	for ; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			if frac {
				return 0, false
			}
			frac = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
		seen = true
		if frac {
			scale /= 10
			f += float64(c-'0') * scale
		} else {
			f = f*10 + float64(c-'0')
		}
	}
	if !seen {
		return 0, false
	}
	if neg {
		f = -f
	}
	return f, true
}

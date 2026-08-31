package backups

// Which stored backup pairs retention should remove.
//
// PURE, so the rule can be tested without a filesystem — and so a preview can
// never disagree with what the sweep actually deletes. That property is the
// reason the live version is factored this way and it is worth keeping: the two
// answers coming from one function is what makes "this will delete N pairs"
// trustworthy.

import (
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Pair is one stored backup: the `.rsc.gz` and `.backup` written together.
type Pair struct {
	Stem        string `json:"stem"`
	RscBytes    int64  `json:"rscBytes"`
	BackupBytes int64  `json:"backupBytes"`
}

// Retention is the per-router limit. Zero means the limit is not applied.
type Retention struct {
	KeepCount int
	KeepDays  int
}

var stemRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2})(\d{2})(\d{2})$`)

// stemToMs is `_stemToMs`: the stem back to epoch milliseconds.
//
// The second return is false where the original gives NaN, and the CALLER must
// keep that distinction rather than folding it into a zero. `NaN < cutoff` is
// false in JavaScript, so an unparseable stem is never aged out by keepDays; a
// port comparing 0 instead would age it out immediately and delete a file it
// could not identify.
func stemToMs(stem string) (int64, bool) {
	m := stemRe.FindStringSubmatch(stem)
	if m == nil {
		return 0, false
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	t := time.Date(n(m[1]), time.Month(n(m[2])), n(m[3]), n(m[4]), n(m[5]), n(m[6]), 0, time.UTC)
	return t.UnixMilli(), true
}

// SelectForPruning returns the stems retention should remove, newest-first.
//
// ── BOTH LIMITS APPLY AND THE STRICTER WINS ─────────────────────────────────
//
// They answer different questions: KeepCount bounds disk, KeepDays bounds
// relevance. A zero limit is not applied at all.
//
// ── THE NEWEST PAIR IS NEVER REMOVED ────────────────────────────────────────
//
// A router whose configuration has been stable for longer than KeepDays would
// otherwise age out its only restore point precisely because nothing has gone
// wrong — the case where losing it matters most. Pairs are written only when the
// configuration CHANGED, so the newest is the current configuration however old.
//
// ── ONLY STEMS THIS APP COULD HAVE WRITTEN ARE CONSIDERED ───────────────────
//
// The sort below is a string compare standing in for a time compare, which holds
// exactly as long as every stem is a timestamp. One that is not breaks it in the
// worst direction: `'n' > '2'`, so a pair named `not-a-timestamp` sorts above
// every real backup, takes the protected newest slot, and leaves every genuine
// restore point doomed — with no error, so the loss would look like backups that
// were never taken.
//
// Dropping them BEFORE the sort rather than after is the whole fix. It also
// means an unreadable stem is never returned as prunable, which is what the
// sweep's contract already asks for: a file this app did not make is not its to
// delete. KeepDays could not have removed one anyway — `stemToMs` reports "not a
// stem" and the comparison is skipped — so such a stem was unprunable and
// protective at once.
//
// PORTED FROM THE LIVE FIX, not invented here. This was reported upstream as
// ToDo item 13 and reproduced deliberately until the live side fixed it in
// `Pruning keeps its promise` (v0.7.33+); `testdata/backup-prune-cases.json` is
// regenerated from that implementation and is what pins the two together.
func SelectForPruning(pairs []Pair, r Retention, now int64) []string {
	sorted := make([]Pair, 0, len(pairs))
	for _, p := range pairs {
		if _, ok := stemToMs(p.Stem); ok {
			sorted = append(sorted, p)
		}
	}
	// DESCENDING BY STEM, and by string rather than by parsed time, as the
	// original sorts — which is safe now that every stem left is a timestamp.
	// SliceStable so equal stems keep their input order, as the original's
	// comparator (which returns 0 for equals) leaves them.
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Stem > sorted[j].Stem })
	if len(sorted) <= 1 {
		return []string{}
	}

	doomed := map[string]bool{}
	if r.KeepCount > 0 && len(sorted) > r.KeepCount {
		for _, p := range sorted[r.KeepCount:] {
			doomed[p.Stem] = true
		}
	}
	if r.KeepDays > 0 {
		cutoff := now - int64(r.KeepDays)*86400000
		for _, p := range sorted {
			// The `ok` test is redundant after the filter above and is kept
			// because the original keeps its NaN comparison: two independent
			// reasons an unparseable stem survives is what the live side has.
			if ms, ok := stemToMs(p.Stem); ok && ms < cutoff {
				doomed[p.Stem] = true
			}
		}
	}
	delete(doomed, sorted[0].Stem)

	out := []string{}
	for _, p := range sorted {
		if doomed[p.Stem] {
			out = append(out, p.Stem)
		}
	}
	return out
}

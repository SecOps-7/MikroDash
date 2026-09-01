package db

// The database cleanup card's two questions: what is in here, and what would a
// purge remove.
//
// ── THE PREDICATE IS BUILT ONCE AND USED TWICE ──────────────────────────────
//
// The live comment on `_purgeWhere` says why in one sentence: "Build the WHERE
// clause shared by the count and the delete, so a preview can never disagree
// with what the delete actually removes." That is the whole design, and it is
// why `purgeWhere` is a function here rather than two similar strings.
//
// ── TWO RULES THAT WIDEN A DELETE IF A PORT GETS THEM WRONG ─────────────────
//
//  1. NO CONDITIONS MEANS NO WHERE CLAUSE, which deletes everything in the
//     target tables. That is what "purge all history for all routers" means, and
//     it is also what a port that dropped a condition silently becomes.
//  2. `olderThanMs > 0` — a ZERO age adds NO condition, so "0 days" means
//     EVERYTHING regardless of age. A port using `>= 0` would compare against
//     "now" and keep the rows it was asked to remove.
//
// The purge corpus lifts both from the live source and records what they
// produce, including the empty-predicate case.
//
// ── `events` IS TWO TABLES WITH DIFFERENT TIMESTAMP COLUMNS ─────────────────
//
// `alert_events.fired_at` and `connectivity_events.ts`. A port modelling one
// table per type would filter the first on a column it does not have.

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// purgeTable is one table and the column its rows are aged by.
type purgeTable struct {
	Table string
	TS    string
}

// purgeTables is the live `PURGE_TABLES`.
//
// Written out rather than lifted, because this has to work in a binary with no
// live tree beside it — the same arrangement, and the same justification, as
// `routerDataTables` in purge.go. The corpus is what keeps the two honest.
var purgeTables = map[string][]purgeTable{
	"ping":      {{Table: "ping_samples", TS: "ts"}},
	"traffic":   {{Table: "traffic_samples", TS: "ts"}},
	"bandwidth": {{Table: "bandwidth_usage", TS: "ts"}},
	"events": {
		{Table: "alert_events", TS: "fired_at"},
		{Table: "connectivity_events", TS: "ts"},
	},
}

// PurgeTypes is `Object.keys(PURGE_TABLES)`, in the order the object declares
// them — which is the order the card renders its checkboxes in.
//
// A Go map has no order, so the list is written out.
var PurgeTypes = []string{"ping", "traffic", "bandwidth", "events"}

// PurgeOpts is what a purge was asked to do.
type PurgeOpts struct {
	// RouterID limits the purge to one router. EMPTY means every router, which
	// is a system-level action rather than a scoped one — see the HTTP layer.
	RouterID string
	// Types limits it to a subset of PurgeTypes. EMPTY means every type.
	Types []string
	// OlderThanMs keeps anything newer than that age. ZERO means no age
	// condition at all — everything, regardless of age. See the header.
	OlderThanMs int64
}

// purgeWhere is `_purgeWhere`: the clause shared by the count and the delete.
//
// `now` is passed in rather than read, so the corpus can freeze it — the live
// function calls `Date.now()` inline, and a cutoff that moved between the
// preview and the delete would make them disagree by however long the operator
// took to read the confirmation.
func purgeWhere(o PurgeOpts, tsCol string, now int64) (string, []any) {
	var where []string
	var params []any
	if o.RouterID != "" {
		where = append(where, "router_id = ?")
		params = append(params, o.RouterID)
	}
	// `> 0`, NOT `>= 0`. See the header, rule 2.
	if o.OlderThanMs > 0 {
		where = append(where, tsCol+" < ?")
		params = append(params, now-o.OlderThanMs)
	}
	if len(where) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(where, " AND "), params
}

// purgeTargets is `_purgeTargets`: which tables a type list resolves to.
//
// AN EMPTY OR ABSENT LIST MEANS EVERY TYPE, and an unrecognised type is DROPPED
// rather than raising. The HTTP layer refuses a list that is entirely
// unrecognised before it gets here — which is what stops `types: ["typo"]`
// quietly becoming "every table".
func purgeTargets(types []string) []purgeTable {
	wanted := types
	if len(wanted) == 0 {
		wanted = PurgeTypes
	}
	var out []purgeTable
	for _, t := range wanted {
		out = append(out, purgeTables[t]...)
	}
	return out
}

// PurgeCounts is what a purge would remove, per type.
type PurgeCounts struct {
	Total  int            `json:"total"`
	ByType map[string]int `json:"byType"`
}

// CountPurge is `countPurge`: the preview.
//
// It runs the SAME predicate as Purge, which is the point — the number shown
// before the operator confirms is exact, not an estimate.
func (d *DB) CountPurge(o PurgeOpts, now int64) (PurgeCounts, error) {
	out := PurgeCounts{ByType: map[string]int{}}
	if d == nil || d.sql == nil {
		return out, errors.New("db not open")
	}
	wanted := o.Types
	if len(wanted) == 0 {
		wanted = PurgeTypes
	}
	for _, typ := range wanted {
		tables, ok := purgeTables[typ]
		if !ok {
			// `if (!PURGE_TABLES[type]) continue` — an unknown type contributes
			// nothing AND does not appear in byType, so the card cannot render a
			// row for a type that does not exist.
			continue
		}
		n := 0
		for _, t := range tables {
			clause, params := purgeWhere(o, t.TS, now)
			var c int
			if err := d.sql.QueryRow(
				"SELECT COUNT(*) FROM "+t.Table+clause, params...).Scan(&c); err != nil {
				return PurgeCounts{ByType: map[string]int{}}, err
			}
			n += c
		}
		out.ByType[typ] = n
		out.Total += n
	}
	return out, nil
}

// Purge deletes the matching rows, in ONE transaction.
//
// The transaction is the same argument `DeleteRouterData`'s carries: these
// tables are read together by the Reports page, and a partial purge leaves a
// report joining live rows to deleted ones — which reads as corrupt data rather
// than as a failed cleanup.
func (d *DB) Purge(o PurgeOpts, now int64) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var deleted int64
	for _, t := range purgeTargets(o.Types) {
		clause, params := purgeWhere(o, t.TS, now)
		res, err := tx.Exec("DELETE FROM "+t.Table+clause, params...)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		deleted += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// DBStats is what the cleanup card shows before anything is chosen.
type DBStats struct {
	Bytes    int64          `json:"bytes"`
	Total    int            `json:"total"`
	ByType   map[string]int `json:"byType"`
	OldestTS *int64         `json:"oldestTs"`
	ByRouter []RouterRows   `json:"byRouter"`
}

// RouterRows is one router's share of the history.
type RouterRows struct {
	RouterID string `json:"routerId"`
	Rows     int    `json:"rows"`
}

// Stats is `stats()`.
//
// ── byRouter IS SORTED BY ROW COUNT, DESCENDING ─────────────────────────────
//
// `.sort((a, b) => b.rows - a.rows)`. The card renders it in that order, so the
// router with the most history is the one an operator sees first — which is the
// one they are usually looking for.
func (d *DB) Stats() (DBStats, error) {
	out := DBStats{ByType: map[string]int{}, ByRouter: []RouterRows{}}
	if d == nil || d.sql == nil {
		// `if (!_db) return { bytes: 0, … }` — an unopened database is zero of
		// everything rather than an error, because the card asks for this on
		// every open and a fresh install has no rows.
		return out, nil
	}

	perRouter := map[string]int{}
	for _, typ := range PurgeTypes {
		n := 0
		for _, t := range purgeTables[typ] {
			var c int
			if err := d.sql.QueryRow("SELECT COUNT(*) FROM " + t.Table).Scan(&c); err != nil {
				return DBStats{ByType: map[string]int{}, ByRouter: []RouterRows{}}, err
			}
			n += c

			rows, err := d.sql.Query(
				"SELECT router_id, COUNT(*) FROM " + t.Table + " GROUP BY router_id")
			if err != nil {
				return DBStats{ByType: map[string]int{}, ByRouter: []RouterRows{}}, err
			}
			for rows.Next() {
				var id string
				var rc int
				if err := rows.Scan(&id, &rc); err != nil {
					_ = rows.Close()
					return DBStats{ByType: map[string]int{}, ByRouter: []RouterRows{}}, err
				}
				perRouter[id] += rc
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return DBStats{ByType: map[string]int{}, ByRouter: []RouterRows{}}, err
			}
			_ = rows.Close()
		}
		out.ByType[typ] = n
		out.Total += n
	}

	// THE OLDEST ROW across all five tables, which is what the card shows as
	// "history back to …". `MIN` over a UNION ALL of five MINs, exactly as the
	// live query does it — a NULL means no rows anywhere, not zero.
	var oldest *int64
	if err := d.sql.QueryRow(`
	    SELECT MIN(t) FROM (
	      SELECT MIN(ts) AS t FROM ping_samples        UNION ALL
	      SELECT MIN(ts) AS t FROM traffic_samples     UNION ALL
	      SELECT MIN(ts) AS t FROM bandwidth_usage     UNION ALL
	      SELECT MIN(ts) AS t FROM connectivity_events UNION ALL
	      SELECT MIN(fired_at) AS t FROM alert_events
	    )`).Scan(&oldest); err != nil {
		return DBStats{ByType: map[string]int{}, ByRouter: []RouterRows{}}, err
	}
	// `oldest || null` — the live expression turns a ZERO into null too. Kept,
	// because a millisecond epoch of 0 is 1970 and would render as history
	// reaching back fifty years.
	if oldest != nil && *oldest == 0 {
		oldest = nil
	}
	out.OldestTS = oldest

	for id, rows := range perRouter {
		out.ByRouter = append(out.ByRouter, RouterRows{RouterID: id, Rows: rows})
	}
	// DESCENDING BY ROWS, then by id so the order is STABLE — a Go map iterates
	// randomly, and two routers with equal counts would otherwise swap places
	// between requests. The live side gets stability for free from insertion
	// order; here it has to be asked for.
	sort.Slice(out.ByRouter, func(i, j int) bool {
		if out.ByRouter[i].Rows != out.ByRouter[j].Rows {
			return out.ByRouter[i].Rows > out.ByRouter[j].Rows
		}
		return out.ByRouter[i].RouterID < out.ByRouter[j].RouterID
	})

	out.Bytes = d.fileSize()
	return out, nil
}

// fileSize is `_fileSize()`: the database file on disk.
func (d *DB) fileSize() int64 {
	if d == nil || d.path == "" {
		return 0
	}
	fi, err := os.Stat(d.path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// Vacuum reclaims the space a purge freed.
//
// ── A DELETE ALONE NEVER SHRINKS THE FILE ───────────────────────────────────
//
// The live comment: "SQLite keeps freed pages inside the file, so a delete alone
// never shrinks it on disk." And the part that is easy to miss: this runs in WAL
// mode, so the freed pages sit in the `-wal` until a checkpoint — VACUUM on its
// own would find nothing to reclaim and the file would not shrink at all.
//
// So it is checkpoint, VACUUM, checkpoint: fold the WAL in, rewrite, then fold
// again so the size the caller measures is the rewritten file.
//
// ── UNDER THIS DRIVER ONLY THE TRAILING CHECKPOINT DOES ANYTHING ────────────
//
// Measured on 2026-08-28 against `modernc.org/sqlite`, on a WAL database with
// 5,000 rows purged:
//
//	after the purge                            126976 bytes
//	after a LEADING checkpoint                 126976
//	after VACUUM, no trailing checkpoint       126976
//	after the TRAILING checkpoint               28672
//
// VACUUM alone reclaimed nothing, and the leading checkpoint changed nothing in
// either ordering. So deleting the FIRST checkpoint is an EQUIVALENT MUTANT
// here, and it survives `TestVacuumShrinksTheFileAfterAPurge` — recorded rather
// than chased.
//
// It stays for two reasons. The live app uses `better-sqlite3`, whose own
// comment says the leading checkpoint is what lets VACUUM find anything to
// reclaim — so this is a difference between two SQLite BINDINGS rather than a
// line that does nothing. And the ordering is cheap: a checkpoint on an
// already-checkpointed WAL is a no-op.
//
// It CANNOT go inside Purge's transaction — VACUUM is not allowed in one — which
// is why the caller runs it afterwards rather than this being part of the purge.
func (d *DB) Vacuum() (before, after int64, err error) {
	if d == nil || d.sql == nil {
		return 0, 0, errors.New("db not open")
	}
	d.vacuums.Add(1)
	before = d.fileSize()
	if _, err := d.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return before, before, fmt.Errorf("db: checkpoint before vacuum: %w", err)
	}
	if _, err := d.sql.Exec("VACUUM"); err != nil {
		return before, before, fmt.Errorf("db: vacuum: %w", err)
	}
	if _, err := d.sql.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return before, d.fileSize(), fmt.Errorf("db: checkpoint after vacuum: %w", err)
	}
	return before, d.fileSize(), nil
}

// PurgeWhereForTest exposes the predicate to the corpus test.
//
// The clause and its params are what the corpus records, and they are not
// otherwise observable — `CountPurge` and `Purge` answer with numbers, so a port
// building the wrong WHERE would agree with the live one on any database where
// both happened to match the same rows.
func PurgeWhereForTest(o PurgeOpts, tsCol string, now int64) (string, []any) {
	return purgeWhere(o, tsCol, now)
}

// PurgeTargetsForTest exposes the table resolution to the corpus test.
func PurgeTargetsForTest(types []string) []struct{ Table, TS string } {
	src := purgeTargets(types)
	out := make([]struct{ Table, TS string }, 0, len(src))
	for _, t := range src {
		out = append(out, struct{ Table, TS string }{t.Table, t.TS})
	}
	return out
}

// VacuumCountForTest is how many times `Vacuum` has been entered.
//
// ── WHY A COUNTER AND NOT AN ASSERTION ON THE FILE ─────────────────────────
//
// The caller's rule is "vacuum only when something was deleted", and BOTH
// obvious observables were measured and neither can see it:
//
//	the byte counts   a vacuum of an already-compact database returns the size
//	                  it started with. Measured: 77824 -> 77824.
//	the file's mtime  changes on EVERY purge, because the DELETE statements run
//	                  whether or not they match a row.
//
// A mutant flipping the guard to `deleted >= 0` survived both. The property is
// about work NOT done — the expensive full-file rewrite a purge that matched
// nothing has no reason to perform — and work not done leaves no trace in the
// result. Counting the entry is the only thing that sees it.
//
// Named like `PurgeWhereForTest` above, and for the same reason: the package
// already exposes what a caller cannot otherwise observe rather than letting a
// gate go unwritten.
func (d *DB) VacuumCountForTest() int64 { return d.vacuums.Load() }

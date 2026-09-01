package db

// The purge predicate and the table resolution, against the live originals.
//
// The corpus is the purge corpus, which lifts `_purgeWhere` and
// `_purgeTargets` out of `src/db.js` by content anchor and runs them with the
// clock FROZEN — the live function calls `Date.now()` inline, so without that
// the recorded params would differ on every run and nothing could be compared.
//
// ── WHY THE CLAUSE IS COMPARED AND NOT JUST THE COUNTS ─────────────────────
//
// `CountPurge` and `Purge` answer with numbers. A port that built the wrong
// WHERE would agree with the live one on any database where both happened to
// match the same rows — which is most small fixtures. The clause and its params
// are the thing that actually differs, so they are what is compared.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type purgeCorpus struct {
	Now   int64 `json:"now"`
	Where []struct {
		Why  string `json:"why"`
		Opts struct {
			RouterID    *string `json:"routerId"`
			OlderThanMs *int64  `json:"olderThanMs"`
		} `json:"opts"`
		TSCol  string `json:"tsCol"`
		SQL    string `json:"sql"`
		Params []any  `json:"params"`
	} `json:"where"`
	Targets []struct {
		Why string `json:"why"`
		// `any`, NOT []string. The corpus deliberately carries a STRING and a
		// NULL here, because the live guard is `Array.isArray(types)` and both
		// fall back to every type. A []string field cannot decode them, and the
		// first version of this test failed to parse the corpus rather than
		// testing the fallback — which is the better failure, but still a
		// failure to model the rule.
		Types   any `json:"types"`
		Targets []struct {
			Table string `json:"table"`
			TS    string `json:"ts"`
		} `json:"targets"`
	} `json:"targets"`
}

func loadPurgeCorpus(t *testing.T) purgeCorpus {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "purge-cases.json"))
	if err != nil {
		t.Fatalf("no corpus — run: node tools/purge-cases.js: %v", err)
	}
	var c purgeCorpus
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Where) == 0 || len(c.Targets) == 0 {
		t.Fatal("the corpus is empty")
	}
	return c
}

func TestPurgeWhereMatchesTheLivePredicate(t *testing.T) {
	c := loadPurgeCorpus(t)

	// THE CORPUS MUST SEPARATE the empty predicate from the narrowing ones. One
	// where every case produced a clause would pass a port that never widens;
	// one where every case was empty would pass a port that never narrows.
	empty, narrowed := 0, 0
	for _, w := range c.Where {
		if w.SQL == "" {
			empty++
		} else {
			narrowed++
		}
	}
	if empty == 0 || narrowed == 0 {
		t.Fatalf("%d empty and %d narrowing predicates; the corpus does not separate them",
			empty, narrowed)
	}

	for _, w := range c.Where {
		t.Run(w.Why, func(t *testing.T) {
			var o PurgeOpts
			if w.Opts.RouterID != nil {
				o.RouterID = *w.Opts.RouterID
			}
			if w.Opts.OlderThanMs != nil {
				o.OlderThanMs = *w.Opts.OlderThanMs
			}

			gotSQL, gotParams := purgeWhere(o, w.TSCol, c.Now)
			if gotSQL != w.SQL {
				t.Errorf("clause = %q, live = %q", gotSQL, w.SQL)
			}
			if len(gotParams) != len(w.Params) {
				t.Fatalf("%d param(s), live had %d: %v vs %v",
					len(gotParams), len(w.Params), gotParams, w.Params)
			}
			for i := range w.Params {
				// The corpus is JSON, so a number arrives as float64 and a
				// string as string. Compared through their text so the two
				// shapes meet.
				if fmt.Sprint(gotParams[i]) != fmt.Sprint(numOrString(w.Params[i])) {
					t.Errorf("param %d = %v, live = %v", i, gotParams[i], w.Params[i])
				}
			}
		})
	}
}

// typeList is `Array.isArray(types) ? types : undefined`.
//
// A STRING or a NULL is not a list, and the live `_purgeTargets` falls back to
// every type for both. Go's parameter is a slice, so "not a list" is nil — which
// is the same fallback expressed in the type system rather than at run time.
func typeList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// numOrString turns a JSON number back into an integer, so `1.6999999e+12` does
// not compare against `1699999914000`.
func numOrString(v any) any {
	if f, ok := v.(float64); ok {
		return int64(f)
	}
	return v
}

func TestPurgeTargetsMatchLive(t *testing.T) {
	c := loadPurgeCorpus(t)

	// A corpus where every case resolved to the same tables would not separate
	// "one type" from "all types".
	sizes := map[int]bool{}
	for _, tc := range c.Targets {
		sizes[len(tc.Targets)] = true
	}
	if len(sizes) < 3 {
		t.Fatalf("the corpus produces only %d distinct target-set sizes; it does not separate "+
			"one type from all types from none", len(sizes))
	}

	for _, tc := range c.Targets {
		t.Run(tc.Why, func(t *testing.T) {
			// A NON-ARRAY `types` in the live call falls back to every type. In
			// Go the parameter is already a slice, so the corpus's string and
			// null cases arrive as nil — which is the same fallback.
			got := PurgeTargetsForTest(typeList(tc.Types))
			if len(got) != len(tc.Targets) {
				t.Fatalf("%d table(s), live had %d: %v vs %v",
					len(got), len(tc.Targets), got, tc.Targets)
			}
			for i := range tc.Targets {
				if got[i].Table != tc.Targets[i].Table || got[i].TS != tc.Targets[i].TS {
					t.Errorf("table %d = %+v, live = %+v", i, got[i], tc.Targets[i])
				}
			}
		})
	}
}

// ── The two that actually touch a database ─────────────────────────────────

const purgeTestDDL = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version VALUES (14, 0);
CREATE TABLE ping_samples        (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE traffic_samples     (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE bandwidth_usage     (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE connectivity_events (router_id TEXT NOT NULL, ts INTEGER NOT NULL);
CREATE TABLE alert_events        (router_id TEXT NOT NULL, fired_at INTEGER NOT NULL);
`

// seedPurgeDB builds a database whose rows straddle every boundary the predicate
// can draw: two routers, and rows both sides of a one-day cutoff.
func seedPurgeDB(t *testing.T, now int64) *DB {
	t.Helper()
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(purgeTestDDL); err != nil {
		t.Fatal(err)
	}
	// WAL MODE, as the real database is. `db.Open`'s DSN does not set it — the
	// file is already in WAL because Node put it there — so a fixture that
	// skipped this ran in the default journal mode, where the checkpoint is a
	// no-op and VACUUM alone shrinks the file. The mutation that deletes the
	// checkpoint SURVIVED against such a fixture, which is the fixture being
	// wrong rather than the mutant being equivalent.
	if _, err := h.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	day := int64(86400000)
	// OLD is two days back, NEW is one hour back. A one-day cutoff separates
	// them; a zero cutoff must take both.
	for _, r := range []struct {
		table, col string
	}{
		{"ping_samples", "ts"}, {"traffic_samples", "ts"}, {"bandwidth_usage", "ts"},
		{"connectivity_events", "ts"}, {"alert_events", "fired_at"},
	} {
		// FOUR ROWS PER TABLE. `rtr-2` has more than `rtr-1`, so sorting by row
		// count DESCENDING and sorting by id ASCENDING give different answers —
		// without that, dropping the row-count comparison entirely survives,
		// because the id tiebreak happens to produce the same order.
		for _, row := range []struct {
			router string
			at     int64
		}{
			{"rtr-1", now - 2*day},
			{"rtr-2", now - 2*day}, {"rtr-2", now - 2*day}, {"rtr-2", now - 3600000},
		} {
			if _, err := h.Exec(
				"INSERT INTO "+r.table+" (router_id, "+r.col+") VALUES (?, ?)",
				row.router, row.at); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestCountPurgeAndPurgeAgree(t *testing.T) {
	now := int64(1700000000000)
	day := int64(86400000)

	for _, c := range []struct {
		why  string
		opts PurgeOpts
		want int64
	}{
		// TWENTY ROWS in total: five tables, four rows each — one for rtr-1 and
		// three for rtr-2, of which one is recent.
		{"everything", PurgeOpts{}, 20},
		{"one router", PurgeOpts{RouterID: "rtr-1"}, 5},
		{"the other router", PurgeOpts{RouterID: "rtr-2"}, 15},
		{"older than a day", PurgeOpts{OlderThanMs: day}, 15},
		{"one router older than a day", PurgeOpts{RouterID: "rtr-2", OlderThanMs: day}, 10},
		{"one type", PurgeOpts{Types: []string{"ping"}}, 4},
		// `events` IS TWO TABLES, so it removes eight where a single type removes
		// four. A port modelling one table per type would report four.
		{"events is two tables", PurgeOpts{Types: []string{"events"}}, 8},
		// A ZERO age is NOT a condition — this must take everything, not nothing.
		{"a zero age takes everything", PurgeOpts{OlderThanMs: 0}, 20},
		{"a router that has no rows", PurgeOpts{RouterID: "rtr-nope"}, 0},
		{"an age nothing is older than", PurgeOpts{OlderThanMs: 3650 * day}, 0},
		// AN UNKNOWN TYPE removes nothing AND must not appear in byType — the
		// card renders a row per key, so a key for a type that does not exist is
		// a row for a type that does not exist.
		{"an unknown type", PurgeOpts{Types: []string{"nosuchtype"}}, 0},
	} {
		t.Run(c.why, func(t *testing.T) {
			d := seedPurgeDB(t, now)

			// THE PREVIEW FIRST, then the delete, and they must agree exactly.
			// That is the property the shared predicate exists for.
			counts, err := d.CountPurge(c.opts, now)
			if err != nil {
				t.Fatalf("CountPurge: %v", err)
			}
			if int64(counts.Total) != c.want {
				t.Errorf("preview said %d, want %d", counts.Total, c.want)
			}

			deleted, err := d.Purge(c.opts, now)
			if err != nil {
				t.Fatalf("Purge: %v", err)
			}
			if deleted != c.want {
				t.Errorf("deleted %d, want %d", deleted, c.want)
			}
			if int64(counts.Total) != deleted {
				t.Errorf("the preview said %d and the delete removed %d — the two must run the "+
					"SAME predicate, which is the whole reason purgeWhere is one function",
					counts.Total, deleted)
			}

			// AND WHAT SURVIVED IS WHAT SHOULD HAVE.
			after, err := d.CountPurge(PurgeOpts{}, now)
			if err != nil {
				t.Fatal(err)
			}
			if int64(after.Total) != 20-c.want {
				t.Errorf("%d row(s) left, want %d", after.Total, 20-c.want)
			}

			// AN UNKNOWN TYPE MUST NOT APPEAR IN byType. `if (!PURGE_TABLES[type])
			// continue` skips it entirely rather than recording a zero, and the
			// card renders one row per key — so a zero here is a row for a data
			// type the app does not have.
			for _, typ := range c.opts.Types {
				if _, known := purgeTables[typ]; known {
					continue
				}
				if _, present := counts.ByType[typ]; present {
					t.Errorf("byType carries a key for %q, which is not a purge type", typ)
				}
			}
		})
	}
}

func TestStatsCountsAndOrdersByRouter(t *testing.T) {
	now := int64(1700000000000)
	d := seedPurgeDB(t, now)

	s, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.Total != 20 {
		t.Errorf("total = %d, want 20", s.Total)
	}
	// `events` is two tables, so it holds eight of the twenty.
	if s.ByType["events"] != 8 {
		t.Errorf("byType[events] = %d, want 8 — it is two tables", s.ByType["events"])
	}
	if s.ByType["ping"] != 4 {
		t.Errorf("byType[ping] = %d, want 4", s.ByType["ping"])
	}
	// DESCENDING BY ROWS: rtr-2 has fifteen and rtr-1 has five, so the row order
	// and the ID order DISAGREE. That is deliberate — with rtr-1 in front on both
	// counts, dropping the row-count comparison entirely survives, because the
	// id tiebreak produces the same answer.
	if len(s.ByRouter) != 2 {
		t.Fatalf("%d router(s), want 2", len(s.ByRouter))
	}
	if s.ByRouter[0].RouterID != "rtr-2" || s.ByRouter[0].Rows != 15 {
		t.Errorf("first row is %+v; the router with the MOST history comes first, because that "+
			"is the one an operator is usually looking for — and it is NOT the one that sorts "+
			"first by id", s.ByRouter[0])
	}
	if s.ByRouter[1].RouterID != "rtr-1" || s.ByRouter[1].Rows != 5 {
		t.Errorf("second row is %+v", s.ByRouter[1])
	}
	// THE OLDEST ROW is the two-day-old one, across every table.
	if s.OldestTS == nil {
		t.Fatal("oldestTs is nil with fifteen rows in the database")
	}
	if *s.OldestTS != now-2*86400000 {
		t.Errorf("oldestTs = %d, want %d", *s.OldestTS, now-2*86400000)
	}
	if s.Bytes <= 0 {
		t.Errorf("bytes = %d; the file is on disk and has rows in it", s.Bytes)
	}
}

// TestStatsOnAnEmptyDatabaseReportsNoOldest.
//
// `oldest || null` — the live expression turns BOTH a NULL and a zero into null.
// A port answering `0` would render as history reaching back to 1970.
func TestStatsOnAnEmptyDatabaseReportsNoOldest(t *testing.T) {
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(purgeTestDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	s, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.OldestTS != nil {
		t.Errorf("oldestTs = %d on an empty database; it must be null, or the card renders "+
			"history reaching back to 1970", *s.OldestTS)
	}
	if s.Total != 0 {
		t.Errorf("total = %d on an empty database", s.Total)
	}
	// `byRouter` is an EMPTY LIST, not nil — the payload must be `[]` and not
	// `null`, which is the same rule `PublicRouters` follows.
	if s.ByRouter == nil {
		t.Error("byRouter is nil; it must marshal as [] rather than null")
	}
}

func TestVacuumShrinksTheFileAfterAPurge(t *testing.T) {
	now := int64(1700000000000)
	d := seedPurgeDB(t, now)

	// Enough rows for the file to be worth reclaiming — three is not.
	for i := 0; i < 5000; i++ {
		if _, err := d.sql.Exec(
			"INSERT INTO ping_samples (router_id, ts) VALUES (?, ?)", "rtr-1", now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Purge(PurgeOpts{}, now); err != nil {
		t.Fatal(err)
	}

	before, after, err := d.Vacuum()
	if err != nil {
		t.Fatalf("Vacuum: %v", err)
	}
	if after >= before {
		t.Errorf("the file went %d -> %d bytes. A delete alone never shrinks it: the freed pages "+
			"sit in the -wal until a checkpoint, so VACUUM without one finds nothing to reclaim.",
			before, after)
	}
}

// TestAZeroTimestampReportsNoOldestRatherThan1970.
//
// ── THE `|| null` THAT LOOKS LIKE A NULL CHECK AND IS NOT ──────────────────
//
// `return { …, oldestTs: oldest || null }`. In JavaScript that turns BOTH a
// NULL and a ZERO into null, and the second is the one a port drops: a Go
// `*int64` already models "no rows" as nil, so the zero case looks handled and
// is not.
//
// A millisecond epoch of 0 is 1 January 1970, and the card renders `oldestTs` as
// "history back to …". A row whose timestamp never got written — a bug upstream,
// or a fixture — would make the card claim fifty-five years of history and make
// every age filter look broken.
//
// The empty-database test cannot see this: it has no rows at all, so the query
// answers NULL and the nil check alone is enough.
func TestAZeroTimestampReportsNoOldest(t *testing.T) {
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(purgeTestDDL); err != nil {
		t.Fatal(err)
	}
	// ONE ROW, timestamped ZERO.
	if _, err := h.Exec(
		"INSERT INTO ping_samples (router_id, ts) VALUES (?, 0)", "rtr-1"); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	s, err := d.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if s.OldestTS != nil {
		t.Errorf("oldestTs = %d for a row timestamped zero. `oldest || null` turns a ZERO into "+
			"null as well as a NULL, and a Go *int64 makes the zero case look handled when it "+
			"is not — the card would claim history reaching back to 1970.", *s.OldestTS)
	}
	// AND THE ROW IS STILL COUNTED. Reporting no oldest timestamp must not mean
	// reporting no rows.
	if s.Total != 1 {
		t.Errorf("total = %d, want 1 — the row exists, only its timestamp is unusable", s.Total)
	}
}

package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type pruneCorpus struct {
	Cutoffs map[string]struct {
		Arg          string `json:"arg"`
		FallbackDays int    `json:"fallbackDays"`
		MsPerDay     int64  `json:"msPerDay"`
	} `json:"cutoffs"`
	Tables []struct {
		Table  string `json:"table"`
		Column string `json:"column"`
		Cutoff string `json:"cutoff"`
	} `json:"tables"`
	IntervalMs      int64 `json:"intervalMs"`
	RunsImmediately bool  `json:"runsImmediately"`
}

func loadPruneCorpus(t *testing.T) pruneCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/db-prune-cases.json")
	if err != nil {
		t.Fatalf("reading the lifted prune mapping: %v", err)
	}
	var c pruneCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Tables) == 0 {
		t.Fatal("the corpus is empty, so this test measures nothing")
	}
	return c
}

// THE PORT DELETES FROM THE SAME TABLES, ON THE SAME COLUMNS, AT THE SAME AGE.
//
// A behavioural test cannot say this. A port that pruned six tables of its own
// invention, on columns of its own choosing, would satisfy every "rows older
// than the cutoff are gone" assertion it wrote for itself. So the mapping is
// lifted from `src/db.js` and compared here.
func TestThePruneMappingMatchesLive(t *testing.T) {
	c := loadPruneCorpus(t)

	if len(pruneRules) != len(c.Tables) {
		t.Fatalf("the port prunes %d tables, live prunes %d", len(pruneRules), len(c.Tables))
	}
	// The cutoff each lifted name resolves to, so "which policy" is compared
	// rather than just "which table".
	days := PruneDays{Metric: 11, Alert: 22, Audit: 33}
	byArg := map[string]int{}
	for name, cut := range c.Cutoffs {
		switch cut.Arg {
		case "retentionDays":
			byArg[name] = days.Metric
		case "alertRetentionDays":
			byArg[name] = days.Alert
		case "auditRetentionDays":
			byArg[name] = days.Audit
		default:
			t.Fatalf("the corpus names a cutoff argument this test does not know: %q", cut.Arg)
		}
	}

	for i, want := range c.Tables {
		got := pruneRules[i]
		if got.table != want.Table {
			t.Errorf("rule %d prunes %q, live prunes %q", i, got.table, want.Table)
			continue
		}
		if got.column != want.Column {
			t.Errorf("%s ages on %q here and %q in live. `alert_events` keys on "+
				"fired_at while the other five use ts, and getting that wrong deletes "+
				"either everything or nothing", want.Table, got.column, want.Column)
		}
		if n := got.days(days); n != byArg[want.Cutoff] {
			t.Errorf("%s uses a %d-day policy here; live uses %s (%d days). "+
				"connectivity_events shares the ALERT retention despite its column "+
				"being ts — aging it with the metrics throws away a year of outage "+
				"history under a 90-day metric policy",
				want.Table, n, want.Cutoff, byArg[want.Cutoff])
		}
	}

	// The fallbacks, which are a DELETE path's most dangerous detail.
	for name, cut := range c.Cutoffs {
		var got int
		switch cut.Arg {
		case "retentionDays":
			got = PruneDays{}.MetricDays()
		case "alertRetentionDays":
			got = PruneDays{}.AlertDays()
		case "auditRetentionDays":
			got = PruneDays{}.AuditDays()
		}
		if got != cut.FallbackDays {
			t.Errorf("%s falls back to %d days here and %d in live", name, got, cut.FallbackDays)
		}
	}
	if msPerDay != c.Cutoffs["metricCutoff"].MsPerDay {
		t.Errorf("a day is %d ms here and %d in live", msPerDay, c.Cutoffs["metricCutoff"].MsPerDay)
	}
}

// ZERO IS NOT "KEEP NOTHING". The live expression is `x || 90`, so a settings
// file that has never had the field written keeps ninety days rather than
// deleting the entire history. On a delete path this is the difference between
// a no-op and an unrecoverable one.
func TestZeroAndNegativeRetentionTakeTheDefault(t *testing.T) {
	for _, p := range []PruneDays{{}, {Metric: 0, Alert: 0, Audit: 0}, {Metric: -1, Alert: -7, Audit: -365}} {
		if p.MetricDays() != 90 || p.AlertDays() != 365 || p.AuditDays() != 365 {
			t.Errorf("%+v resolved to %d/%d/%d, want 90/365/365",
				p, p.MetricDays(), p.AlertDays(), p.AuditDays())
		}
	}
	if (PruneDays{Metric: 7}).MetricDays() != 7 {
		t.Error("a real policy was overridden by the default")
	}
}

// AND IT ACTUALLY DELETES — against a real database, with rows either side of
// every cutoff, and each table checked separately so one working rule cannot
// cover for five broken ones.
//
// The three policies are deliberately FAR APART (10 / 100 / 200 days) so a rule
// reaching for the wrong cutoff deletes visibly too much or too little rather
// than landing inside the tolerance of a neighbouring one. That is what catches
// `connectivity_events` being aged with the metrics.
func TestPruneDeletesOnlyWhatIsOlderThanItsOwnPolicy(t *testing.T) {
	d := openTestDB(t)
	const now int64 = 1_800_000_000_000
	day := int64(msPerDay)
	p := PruneDays{Metric: 10, Alert: 100, Audit: 200}

	// Each table with the column the sweep ages it on, the policy that governs
	// it, and a full INSERT — the real schema has NOT NULL columns, so a
	// ts-only insert would fail rather than test anything.
	seeds := []struct {
		table  string
		days   int64
		insert string
		args   []any
	}{
		{"ping_samples", 10,
			`INSERT INTO ping_samples (router_id, target, loss_pct, ts) VALUES ('r1','198.51.100.1',0,?)`, nil},
		{"traffic_samples", 10,
			`INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts) VALUES ('r1','ether1',1,2,?)`, nil},
		{"bandwidth_usage", 10,
			`INSERT INTO bandwidth_usage (router_id, interface, rx_mb, tx_mb, ts) VALUES ('r1','ether1',1,2,?)`, nil},
		{"alert_events", 100,
			`INSERT INTO alert_events (router_id, alert_type, fired_at) VALUES ('r1','cpu',?)`, nil},
		{"connectivity_events", 100,
			`INSERT INTO connectivity_events (router_id, connected, ts) VALUES ('r1',1,?)`, nil},
		{"audit_events", 200,
			`INSERT INTO audit_events (ts, actor_id, actor_name, action, scope, outcome)
			 VALUES (?, 'u1', 'someone', 'test.action', 'app', 'ok')`, nil},
	}

	for _, s := range seeds {
		keep := now - (s.days-1)*day // inside its own window
		drop := now - (s.days+1)*day // outside it
		for _, ts := range []int64{keep, drop} {
			if _, err := d.sql.Exec(s.insert, ts); err != nil {
				t.Fatalf("seeding %s: %v", s.table, err)
			}
		}
	}

	n, err := d.Prune(p, now)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != len(seeds) {
		t.Errorf("pruned %d rows, want %d — exactly one per table", n, len(seeds))
	}
	for _, s := range seeds {
		var left int
		if err := d.sql.QueryRow("SELECT COUNT(*) FROM " + s.table).Scan(&left); err != nil {
			t.Fatalf("counting %s: %v", s.table, err)
		}
		if left != 1 {
			t.Errorf("%s has %d rows left, want 1 — the row inside its own policy", s.table, left)
		}
	}

	// A SECOND SWEEP DELETES NOTHING. The daily timer runs against an unchanged
	// database most days, and a sweep that kept finding work would mean the
	// cutoff was moving under it.
	again, err := d.Prune(p, now)
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Errorf("a second identical sweep deleted %d more rows", again)
	}
}

// openTestDB is an empty database with this port's own schema applied.
//
// `Open` creates the tables, so the sweep runs against the real DDL rather than
// a hand-written approximation of it — which matters here, because the test
// inserts into six tables by name and a column that had been renamed would show
// up as a seeding failure rather than as a passing test on a fictional schema.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	// `historyDDL` and `schemaDDL` are this package's EXISTING fixture schemas,
	// reused rather than copied: the schema audit validates every fixture
	// DDL against the real one, and a seventh copy of these tables is a seventh
	// thing that can drift from it.
	dir := newDB(t, MinSchema, true)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(historyDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	return openTest(t, dir)
}

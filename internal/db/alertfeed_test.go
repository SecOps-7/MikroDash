package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The table as the live migration creates it.
const alertEventsDDL = `
CREATE TABLE alert_events (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  router_id       TEXT    NOT NULL,
  alert_type      TEXT    NOT NULL,
  subject         TEXT,
  detail          TEXT,
  fired_at        INTEGER NOT NULL,
  resolved_at     INTEGER,
  acknowledged_at INTEGER,
  acknowledged_by TEXT
);`

type liveAlertRow struct {
	AlertType      string  `json:"alert_type"`
	Subject        *string `json:"subject"`
	Detail         *string `json:"detail"`
	FiredAt        int64   `json:"fired_at"`
	ResolvedAt     *int64  `json:"resolved_at"`
	AcknowledgedAt *int64  `json:"acknowledged_at"`
	AcknowledgedBy *string `json:"acknowledged_by"`
}

type alertFeedCorpus struct {
	Seed []struct {
		Router   string  `json:"router"`
		Type     string  `json:"type"`
		Subject  *string `json:"subject"`
		Detail   *string `json:"detail"`
		Fired    int64   `json:"fired"`
		Resolved *int64  `json:"resolved"`
	} `json:"seed"`
	Since int64 `json:"since"`
	Cases map[string]struct {
		Router string         `json:"router"`
		Since  *int64         `json:"since"`
		Limit  *int           `json:"limit"`
		Rows   []liveAlertRow `json:"rows"`
	} `json:"cases"`
}

func alertFeedDB(t *testing.T) (*DB, alertFeedCorpus) {
	t.Helper()
	b, err := os.ReadFile("../../testdata/alertfeed-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c alertFeedCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Seed) == 0 || len(c.Cases) == 0 {
		t.Fatal("corpus is empty -- these tests would pass against nothing")
	}

	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	if _, err := h.Exec(alertEventsDDL); err != nil {
		t.Fatal(err)
	}
	// THE SAME ROWS the live queries were run against, in the same order — the
	// ids are assigned by insertion, and an ORDER BY tie would otherwise resolve
	// differently here.
	for _, r := range c.Seed {
		// `subject || null, detail || null` — the coercion the LIVE
		// `insertAlertEvent` applies, reproduced here because the corpus records
		// what the generator PASSED and the table holds what the insert STORED.
		// An empty detail is a null in the database, so seeding the empty string
		// would build a table the live queries never ran against.
		if _, err := h.Exec(`
      INSERT INTO alert_events (router_id, alert_type, subject, detail, fired_at, resolved_at)
      VALUES (?, ?, ?, ?, ?, ?)`,
			r.Router, r.Type, emptyToNull(r.Subject), emptyToNull(r.Detail),
			r.Fired, r.Resolved); err != nil {
			t.Fatal(err)
		}
	}

	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, c
}

func TestTheAlertFeedsMatchLive(t *testing.T) {
	d, c := alertFeedDB(t)

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			limit := 0
			if tc.Limit != nil {
				limit = *tc.Limit
			}
			var got []AlertRow
			var err error
			if strings.HasPrefix(name, "recent") {
				since := int64(0)
				if tc.Since != nil {
					since = *tc.Since
				}
				got, err = d.RecentAlerts(tc.Router, since, limit)
			} else {
				got, err = d.OpenAlerts(tc.Router, limit)
			}
			if err != nil {
				t.Fatal(err)
			}

			if len(got) != len(tc.Rows) {
				t.Fatalf("%d rows, live returned %d\n  got  %v\n  live %v",
					len(got), len(tc.Rows), typesOf(got), liveTypesOf(tc.Rows))
			}
			for i, want := range tc.Rows {
				g := got[i]
				if g.AlertType != want.AlertType {
					t.Errorf("row %d is %q, live %q (order: %v vs %v)",
						i, g.AlertType, want.AlertType, typesOf(got), liveTypesOf(tc.Rows))
				}
				if g.FiredAt != want.FiredAt {
					t.Errorf("%s: fired_at %d, live %d", g.AlertType, g.FiredAt, want.FiredAt)
				}
				if !sameI64(g.ResolvedAt, want.ResolvedAt) {
					t.Errorf("%s: resolved_at %v, live %v", g.AlertType, g.ResolvedAt, want.ResolvedAt)
				}
				if !sameStr(g.Subject, want.Subject) {
					t.Errorf("%s: subject %v, live %v", g.AlertType, g.Subject, want.Subject)
				}
				if !sameStr(g.Detail, want.Detail) {
					t.Errorf("%s: detail %v, live %v", g.AlertType, g.Detail, want.Detail)
				}
				// An unacknowledged alert must come back with NULLS, not zeroes:
				// a zero timestamp renders as "seen at the epoch" rather than unseen.
				if !sameI64(g.AcknowledgedAt, want.AcknowledgedAt) {
					t.Errorf("%s: acknowledged_at %v, live %v",
						g.AlertType, g.AcknowledgedAt, want.AcknowledgedAt)
				}
				if !sameStr(g.AcknowledgedBy, want.AcknowledgedBy) {
					t.Errorf("%s: acknowledged_by %v, live %v",
						g.AlertType, g.AcknowledgedBy, want.AcknowledgedBy)
				}
			}
		})
	}
}

// TestTheTwoFeedsNeverOverlap, stated independently of the corpus.
//
// "Recent" means RESOLVED. An alert that is still open belongs in one feed and
// one only — if it appeared in both, the bell would count it twice and the
// operator would see a number that does not match the list.
func TestTheTwoFeedsNeverOverlap(t *testing.T) {
	d, _ := alertFeedDB(t)

	open, err := d.OpenAlerts("router-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	recent, err := d.RecentAlerts("router-a", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) == 0 || len(recent) == 0 {
		t.Fatalf("one feed is empty (%d open, %d recent) -- this proves nothing",
			len(open), len(recent))
	}

	seen := map[int64]bool{}
	for _, r := range open {
		if r.ResolvedAt != nil {
			t.Errorf("a RESOLVED alert (%s) is in the open feed", r.AlertType)
		}
		seen[r.ID] = true
	}
	for _, r := range recent {
		if r.ResolvedAt == nil {
			t.Errorf("an OPEN alert (%s) is in the recent feed", r.AlertType)
		}
		if seen[r.ID] {
			t.Errorf("alert %d (%s) is in BOTH feeds", r.ID, r.AlertType)
		}
	}
}

// TestTheFeedsSortOnDifferentColumns.
//
// The open feed is newest-FIRED, the recent feed newest-RESOLVED. They disagree
// whenever an alert fires early and resolves late — sorting the recent feed on
// `fired_at` produces a list that looks plausible and is backwards.
func TestTheFeedsSortOnDifferentColumns(t *testing.T) {
	d, _ := alertFeedDB(t)

	open, _ := d.OpenAlerts("router-a", 0)
	for i := 1; i < len(open); i++ {
		if open[i-1].FiredAt < open[i].FiredAt {
			t.Errorf("the open feed is not newest-fired-first at index %d", i)
		}
	}

	recent, _ := d.RecentAlerts("router-a", 0, 0)
	for i := 1; i < len(recent); i++ {
		if *recent[i-1].ResolvedAt < *recent[i].ResolvedAt {
			t.Errorf("the recent feed is not newest-resolved-first at index %d", i)
		}
	}
	// ...and the two orders must actually differ on this data, or the assertion
	// above would hold for a feed sorted the wrong way.
	byFired := true
	for i := 1; i < len(recent); i++ {
		if recent[i-1].FiredAt < recent[i].FiredAt {
			byFired = false
			break
		}
	}
	if byFired {
		t.Error("the recent feed happens to be in fired_at order too, so this test " +
			"cannot tell the two sort columns apart")
	}
}

// TestAZeroLimitTakesTheDefault. `limit || 200` is falsy on zero, so a caller
// passing 0 gets the default — not an empty feed that looks like "no alerts".
func TestAZeroLimitTakesTheDefault(t *testing.T) {
	d, _ := alertFeedDB(t)

	full, _ := d.OpenAlerts("router-a", 0)
	if len(full) == 0 {
		t.Fatal("a zero limit returned nothing -- the bell would show an empty feed")
	}
	if n, _ := d.OpenAlerts("router-a", -5); len(n) != len(full) {
		t.Errorf("a negative limit returned %d rows, want %d", len(n), len(full))
	}
	if n, _ := d.OpenAlerts("router-a", 1); len(n) != 1 {
		t.Errorf("a limit of 1 returned %d rows", len(n))
	}

	fullRecent, _ := d.RecentAlerts("router-a", 0, 0)
	if len(fullRecent) == 0 {
		t.Fatal("a zero recent limit returned nothing")
	}
	if n, _ := d.RecentAlerts("router-a", 0, 1); len(n) != 1 {
		t.Errorf("a recent limit of 1 returned %d rows", len(n))
	}
}

// TestTheSinceBoundaryIsInclusive.
func TestTheSinceBoundaryIsInclusive(t *testing.T) {
	d, c := alertFeedDB(t)
	rows, err := d.RecentAlerts("router-a", c.Since, 0)
	if err != nil {
		t.Fatal(err)
	}
	var onBoundary, before bool
	for _, r := range rows {
		switch r.AlertType {
		case "exactly-at-since":
			onBoundary = true
		case "before-since":
			before = true
		}
	}
	if !onBoundary {
		t.Error("a row resolved exactly at `since` was excluded -- the comparison is >=")
	}
	if before {
		t.Error("a row resolved one millisecond before `since` was included")
	}
}

func typesOf(rows []AlertRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.AlertType)
	}
	return out
}

func liveTypesOf(rows []liveAlertRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.AlertType)
	}
	return out
}

// emptyToNull is JavaScript's `x || null` for a nullable text column.
func emptyToNull(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

func sameI64(a, b *int64) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func sameStr(a, b *string) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

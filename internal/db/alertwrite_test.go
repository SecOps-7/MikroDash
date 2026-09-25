package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

// The corpus, as the alertwrite corpus writes it.
//
// Timestamps are LABELS, not numbers: every one of these functions stamps the
// wall clock, so the literal values differ on every run. 'seeded' means the
// column still holds what the seed put there, 'fresh' means the call under test
// changed it, 'earlier' means a previous call in the sequence did. Reproducing
// the sequence is therefore part of the test, not scaffolding around it.

type alertWriteCorpus struct {
	SeededAckAt int64  `json:"seededAckAt"`
	SeededAckBy string `json:"seededAckBy"`
	Seed        []struct {
		Key      string `json:"key"`
		Router   string `json:"router"`
		Resolved *int64 `json:"resolved"`
		AckedAt  *int64 `json:"ackedAt"`
	} `json:"seed"`
	Cases map[string]struct {
		Result json.RawMessage            `json:"result"`
		State  map[string]alertWriteState `json:"state"`
	} `json:"cases"`
}

type alertWriteState struct {
	ResolvedAt     *string `json:"resolvedAt"`
	AcknowledgedAt *string `json:"acknowledgedAt"`
	AcknowledgedBy *string `json:"acknowledgedBy"`
}

// alertWriteFixture seeds a database identical to the one the generator ran
// against and returns it with the key→id map.
func alertWriteFixture(t *testing.T) (*DB, alertWriteCorpus, map[string]int64) {
	t.Helper()
	b, err := os.ReadFile("../../testdata/alertwrite-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c alertWriteCorpus
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
	if _, err := h.Exec(alertEventsDDL); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, r := range c.Seed {
		var ackBy any
		if r.AckedAt != nil {
			ackBy = c.SeededAckBy
		}
		res, err := h.Exec(`
      INSERT INTO alert_events
        (router_id, alert_type, subject, detail, fired_at, resolved_at,
         acknowledged_at, acknowledged_by)
      VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			r.Router, r.Key, "subj-"+r.Key, "detail", int64(1), r.Resolved, r.AckedAt, ackBy)
		if err != nil {
			t.Fatal(err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		ids[r.Key] = id
	}
	_ = h.Close()

	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, c, ids
}

// labeller reproduces the generator's classifier: a column that CHANGED since
// the last snapshot is 'fresh'; one that did not keeps its label, except that
// last call's 'fresh' becomes 'earlier'.
//
// It compares against the previous OBSERVED value rather than against a clock
// window. The generator's first version used a window and was not
// deterministic — two calls in the same millisecond made an earlier write fall
// inside this call's band, and the same row was labelled either way depending on
// machine speed.
type labeller struct {
	t    *testing.T
	d    *DB
	ids  map[string]int64
	keys []string
	seed map[string]int64 // seeded constants, by column-agnostic value
	last map[string][2]*int64
	lab  map[string][2]*string
}

func newLabeller(t *testing.T, d *DB, c alertWriteCorpus, ids map[string]int64) *labeller {
	l := &labeller{t: t, d: d, ids: ids,
		last: map[string][2]*int64{}, lab: map[string][2]*string{}}
	seeded := "seeded"
	for _, r := range c.Seed {
		l.keys = append(l.keys, r.Key)
		row := l.read(r.Key)
		l.last[r.Key] = [2]*int64{row.ResolvedAt, row.AcknowledgedAt}
		var lr, la *string
		if row.ResolvedAt != nil {
			lr = &seeded
		}
		if row.AcknowledgedAt != nil {
			la = &seeded
		}
		l.lab[r.Key] = [2]*string{lr, la}
	}
	return l
}

func (l *labeller) read(key string) AlertRow {
	l.t.Helper()
	rows, err := l.d.scanAlerts(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events WHERE id = ?`, l.ids[key])
	if err != nil {
		l.t.Fatal(err)
	}
	if len(rows) != 1 {
		l.t.Fatalf("%s: %d rows -- the seed is not what the generator ran against", key, len(rows))
	}
	return rows[0]
}

func (l *labeller) snapshot() map[string]alertWriteState {
	fresh, earlier := "fresh", "earlier"
	out := map[string]alertWriteState{}
	for _, key := range l.keys {
		row := l.read(key)
		prev, lab := l.last[key], l.lab[key]
		now := [2]*int64{row.ResolvedAt, row.AcknowledgedAt}
		for i := 0; i < 2; i++ {
			if !sameI64(now[i], prev[i]) {
				if now[i] == nil {
					lab[i] = nil
				} else {
					lab[i] = &fresh
				}
			} else if lab[i] != nil && *lab[i] == "fresh" {
				lab[i] = &earlier
			}
		}
		l.last[key], l.lab[key] = now, lab
		out[key] = alertWriteState{
			ResolvedAt: lab[0], AcknowledgedAt: lab[1], AcknowledgedBy: row.AcknowledgedBy,
		}
	}
	return out
}

func (l *labeller) check(name string, c alertWriteCorpus) {
	l.t.Helper()
	want := c.Cases[name].State
	if len(want) == 0 {
		l.t.Fatalf("%s: the corpus records no state, so this case asserts nothing", name)
	}
	got := l.snapshot()
	for _, key := range l.keys {
		if !reflect.DeepEqual(got[key], want[key]) {
			l.t.Errorf("%s / %s:\n  got  %s\n  live %s",
				name, key, showState(got[key]), showState(want[key]))
		}
	}
}

func showState(s alertWriteState) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestTheAlertWritesMatchLive drives the same call sequence the generator drove.
func TestTheAlertWritesMatchLive(t *testing.T) {
	d, c, ids := alertWriteFixture(t)
	l := newLabeller(t, d, c, ids)

	var maxID int64
	for _, id := range ids {
		if id > maxID {
			maxID = id
		}
	}
	unknown := maxID + 1000

	// ---- the scope lookup ----
	for name, id := range map[string]int64{
		"scopeKnown":       ids["open-unacked"],
		"scopeOtherRouter": ids["other-open"],
		"scopeUnknown":     unknown,
		"scopeZero":        0,
	} {
		got, err := d.AlertRouterID(id)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var want string
		if err := json.Unmarshal(c.Cases[name].Result, &want); err != nil {
			want = "" // the corpus records null for "no such alert"
		}
		if got != want {
			t.Errorf("%s: AlertRouterID = %q, live %q", name, got, want)
		}
	}

	// ---- acknowledge ----
	acks := []struct {
		name, user string
		id         int64
	}{
		{"ackOpen", "alice", ids["open-unacked"]},
		{"ackClosed", "bob", ids["closed-unacked"]},
		{"ackAlreadyAcked", "carol", ids["open-acked"]},
		{"ackUnknown", "dave", unknown},
		{"ackEmptyUser", "", ids["open-second-ack"]},
	}
	for _, a := range acks {
		row, err := d.AcknowledgeAlert(a.id, a.user)
		if err != nil {
			t.Fatalf("%s: %v", a.name, err)
		}
		var live map[string]any
		if err := json.Unmarshal(c.Cases[a.name].Result, &live); err != nil {
			live = nil
		}
		if (row == nil) != (live == nil) {
			t.Errorf("%s: returned %v, live returned %v", a.name, row != nil, live != nil)
		}
		if row != nil && live != nil {
			if got, want := float64(row.ID), live["id"].(float64); got != want {
				t.Errorf("%s: id %v, live %v", a.name, got, want)
			}
			if v, ok := live["routerId"]; ok && row.RouterID != v.(string) {
				t.Errorf("%s: routerId %q, live %q", a.name, row.RouterID, v)
			}
			if v, ok := live["alertType"]; ok && row.AlertType != v.(string) {
				t.Errorf("%s: alertType %q, live %q", a.name, row.AlertType, v)
			}
			// ackEmptyUser records this, and it is the whole case: `username ||
			// null` stores NULL, not "".
			if v, ok := live["acknowledgedBy"]; ok {
				if v == nil && row.AcknowledgedBy != nil {
					t.Errorf("%s: acknowledgedBy %q, live NULL -- an empty username was "+
						"stored as a name", a.name, *row.AcknowledgedBy)
				}
			}
		}
		l.check(a.name, c)
	}

	// ---- clear all ----
	clears := []struct{ name, router, user string }{
		{"clearAll", "router-a", "eve"},
		{"clearAllAgain", "router-a", "frank"},
		{"clearUnknownRouter", "nope", "grace"},
	}
	for _, cl := range clears {
		got, err := d.ResolveAllAlerts(cl.router, cl.user)
		if err != nil {
			t.Fatalf("%s: %v", cl.name, err)
		}
		var live []string
		if err := json.Unmarshal(c.Cases[cl.name].Result, &live); err != nil {
			t.Fatalf("%s: the corpus result is not a list of keys: %v", cl.name, err)
		}
		keys := make([]string, 0, len(got))
		for _, id := range got {
			k := ""
			for key, v := range ids {
				if v == id {
					k = key
				}
			}
			if k == "" {
				t.Fatalf("%s: returned id %d, which the seed never minted", cl.name, id)
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sort.Strings(live)
		if !reflect.DeepEqual(keys, live) {
			t.Errorf("%s: cleared %v, live cleared %v", cl.name, keys, live)
		}
		l.check(cl.name, c)
	}
}

// TestClearAllStampsOneInstant.
//
// The two UPDATEs share one `now`, so a row clear-all both acknowledged and
// resolved carries the SAME instant in both columns.
//
// The within-statement half is asserted too and is nearly free: SQLite evaluates
// a parameter once per statement, so it holds however the clock is written. It
// stays because it states the intent, and because it is the half a reader
// assumes is the point — the ACROSS-statement assertion below is the one that
// kills an inlined clock, and without it a mutation inlining it survives.
func TestClearAllStampsOneInstant(t *testing.T) {
	d, _, ids := alertWriteFixture(t)

	got, err := d.ResolveAllAlerts("router-a", "eve")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("only %d rows were cleared -- one instant is trivially true", len(got))
	}
	rows, err := d.scanAlerts(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events WHERE router_id = 'router-a' AND resolved_at IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	for _, r := range rows {
		// The seeded rows carry the seed's own constant; only the cleared ones
		// are under test.
		for _, id := range got {
			if r.ID == id {
				seen[*r.ResolvedAt] = true
			}
		}
	}
	if len(seen) != 1 {
		t.Errorf("%d distinct instants across %d cleared rows", len(seen), len(got))
	}

	// The row nothing had touched before: clear-all acknowledged AND resolved it,
	// so both columns must hold the same number. This is what an inlined clock
	// breaks.
	row, err := d.scanAlerts(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events WHERE id = ?`, ids["open-untouched"])
	if err != nil {
		t.Fatal(err)
	}
	if len(row) != 1 || row[0].ResolvedAt == nil || row[0].AcknowledgedAt == nil {
		t.Fatalf("open-untouched came back %+v -- clear-all left a column unset, so this "+
			"test cannot compare them", row)
	}
	if *row[0].AcknowledgedAt != *row[0].ResolvedAt {
		t.Errorf("acknowledged at %d and resolved at %d -- one `now` shared by both "+
			"statements is what keeps them equal",
			*row[0].AcknowledgedAt, *row[0].ResolvedAt)
	}
}

// TestNothingIsDeleted. Reports and the CSV export still show what happened;
// deleting is a separate deliberate act in Settings → Database.
func TestNothingIsDeleted(t *testing.T) {
	d, c, _ := alertWriteFixture(t)
	if _, err := d.ResolveAllAlerts("router-a", "eve"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM alert_events`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(c.Seed) {
		t.Errorf("%d of %d rows remain -- clear-all deleted history", n, len(c.Seed))
	}
}

package db

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The three sample tables, copied from the src/db.js migrations. The audit
// helper in db_test.go creates its own table and not these, because a helper
// that created every table would let a test pass while naming none of them.
const historyDDL = `
CREATE TABLE ping_samples (
  id        INTEGER PRIMARY KEY,
  router_id TEXT    NOT NULL,
  target    TEXT    NOT NULL,
  rtt_ms    REAL,
  loss_pct  REAL    NOT NULL,
  ts        INTEGER NOT NULL
);
CREATE INDEX idx_ping_router_ts ON ping_samples(router_id, ts);
CREATE TABLE traffic_samples (
  id        INTEGER PRIMARY KEY,
  router_id TEXT    NOT NULL,
  interface TEXT    NOT NULL,
  rx_mbps   REAL    NOT NULL,
  tx_mbps   REAL    NOT NULL,
  ts        INTEGER NOT NULL
);
CREATE INDEX idx_traffic_router_iface_ts ON traffic_samples(router_id, interface, ts);
CREATE TABLE bandwidth_usage (
  id        INTEGER PRIMARY KEY,
  router_id TEXT    NOT NULL,
  interface TEXT    NOT NULL,
  rx_mb     REAL    NOT NULL,
  tx_mb     REAL    NOT NULL,
  ts        INTEGER NOT NULL
);
CREATE INDEX idx_bw_router_iface_ts ON bandwidth_usage(router_id, interface, ts);
CREATE TABLE alert_events (
  id              INTEGER PRIMARY KEY,
  router_id       TEXT    NOT NULL,
  alert_type      TEXT    NOT NULL,
  subject         TEXT,
  detail          TEXT,
  fired_at        INTEGER NOT NULL,
  resolved_at     INTEGER,
  acknowledged_at INTEGER,
  acknowledged_by TEXT
);
CREATE INDEX idx_alert_router_ts ON alert_events(router_id, fired_at);
CREATE TABLE connectivity_events (
  id        INTEGER PRIMARY KEY,
  router_id TEXT    NOT NULL,
  connected INTEGER NOT NULL,
  ts        INTEGER NOT NULL
);
CREATE INDEX idx_conn_router_ts ON connectivity_events(router_id, ts);
CREATE TABLE report_schedules (
  id              TEXT PRIMARY KEY,
  router_id       TEXT    NOT NULL,
  name            TEXT    NOT NULL,
  sections        TEXT    NOT NULL,
  interface       TEXT,
  aggregate       TEXT    NOT NULL DEFAULT '',
  recipients      TEXT    NOT NULL,
  frequency       TEXT    NOT NULL CHECK (frequency IN ('daily','weekly','monthly')),
  send_hour       INTEGER NOT NULL DEFAULT 7,
  enabled         INTEGER NOT NULL DEFAULT 1,
  disabled_reason TEXT,
  created_by      TEXT,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX idx_report_schedules_router ON report_schedules (router_id);
CREATE TABLE report_runs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  schedule_id  TEXT    NOT NULL REFERENCES report_schedules(id) ON DELETE CASCADE,
  ran_at       INTEGER NOT NULL,
  period_from  INTEGER NOT NULL,
  period_to    INTEGER NOT NULL,
  outcome      TEXT    NOT NULL,
  source       TEXT    NOT NULL DEFAULT 'schedule',
  actor        TEXT,
  recipients_n INTEGER NOT NULL DEFAULT 0,
  bytes        INTEGER NOT NULL DEFAULT 0,
  rows_n       INTEGER NOT NULL DEFAULT 0,
  ms           INTEGER NOT NULL DEFAULT 0,
  error        TEXT
);
CREATE INDEX idx_report_runs_sched ON report_runs (schedule_id, ran_at DESC);
`

type historyCases struct {
	Base int64 `json:"base"`
	End  int64 `json:"end"`
	Seed struct {
		Ping []struct {
			RouterID string   `json:"router_id"`
			Target   string   `json:"target"`
			RttMs    *float64 `json:"rtt_ms"`
			LossPct  float64  `json:"loss_pct"`
			TS       int64    `json:"ts"`
		} `json:"ping"`
		Traffic []struct {
			RouterID  string  `json:"router_id"`
			Interface string  `json:"interface"`
			RxMbps    float64 `json:"rx_mbps"`
			TxMbps    float64 `json:"tx_mbps"`
			TS        int64   `json:"ts"`
		} `json:"traffic"`
		Bandwidth []struct {
			RouterID  string  `json:"router_id"`
			Interface string  `json:"interface"`
			RxMb      float64 `json:"rx_mb"`
			TxMb      float64 `json:"tx_mb"`
			TS        int64   `json:"ts"`
		} `json:"bandwidth"`
		Alerts []struct {
			RouterID       string  `json:"router_id"`
			AlertType      string  `json:"alert_type"`
			Subject        *string `json:"subject"`
			Detail         *string `json:"detail"`
			FiredAt        int64   `json:"fired_at"`
			ResolvedAt     *int64  `json:"resolved_at"`
			AcknowledgedAt *int64  `json:"acknowledged_at"`
			AcknowledgedBy *string `json:"acknowledged_by"`
		} `json:"alerts"`
		Connectivity []struct {
			RouterID  string `json:"router_id"`
			Connected int    `json:"connected"`
			TS        int64  `json:"ts"`
		} `json:"connectivity"`
		Schedules []ReportSchedule `json:"schedules"`
		Runs      []struct {
			ScheduleID string  `json:"schedule_id"`
			RanAt      int64   `json:"ran_at"`
			PeriodFrom int64   `json:"period_from"`
			PeriodTo   int64   `json:"period_to"`
			Outcome    string  `json:"outcome"`
			Source     string  `json:"source"`
			Actor      *string `json:"actor"`
			Recipients int     `json:"recipients_n"`
			Bytes      int64   `json:"bytes"`
			Rows       int     `json:"rows_n"`
			Ms         int64   `json:"ms"`
			Error      *string `json:"error"`
		} `json:"runs"`
	} `json:"seed"`
	Queries []struct {
		Name string          `json:"name"`
		Fn   string          `json:"fn"`
		Args []any           `json:"args"`
		Rows json.RawMessage `json:"rows"`
	} `json:"queries"`
}

// seededDB rebuilds the generator's database from the recorded seed.
//
// THE ROWS ARE THE CONTRACT, NOT A .db FILE. Shipping the database itself would
// have been smaller and would have hidden every input behind a binary nobody
// reviews; recording what was inserted means the edges — the all-timeout hour,
// the sample one millisecond before a boundary — are readable in the diff.
func seededDB(t *testing.T, c historyCases) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := h.Exec(historyDDL); err != nil {
		t.Fatal(err)
	}
	for _, r := range c.Seed.Ping {
		if _, err := h.Exec(`INSERT INTO ping_samples (router_id, target, rtt_ms, loss_pct, ts) VALUES (?,?,?,?,?)`,
			r.RouterID, r.Target, r.RttMs, r.LossPct, r.TS); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range c.Seed.Traffic {
		if _, err := h.Exec(`INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts) VALUES (?,?,?,?,?)`,
			r.RouterID, r.Interface, r.RxMbps, r.TxMbps, r.TS); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range c.Seed.Bandwidth {
		if _, err := h.Exec(`INSERT INTO bandwidth_usage (router_id, interface, rx_mb, tx_mb, ts) VALUES (?,?,?,?,?)`,
			r.RouterID, r.Interface, r.RxMb, r.TxMb, r.TS); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range c.Seed.Alerts {
		if _, err := h.Exec(`INSERT INTO alert_events
			(router_id, alert_type, subject, detail, fired_at, resolved_at, acknowledged_at, acknowledged_by)
			VALUES (?,?,?,?,?,?,?,?)`,
			r.RouterID, r.AlertType, r.Subject, r.Detail, r.FiredAt, r.ResolvedAt,
			r.AcknowledgedAt, r.AcknowledgedBy); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range c.Seed.Connectivity {
		if _, err := h.Exec(`INSERT INTO connectivity_events (router_id, connected, ts) VALUES (?,?,?)`,
			r.RouterID, r.Connected, r.TS); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range c.Seed.Schedules {
		if _, err := h.Exec(`INSERT INTO report_schedules
			(id, router_id, name, sections, interface, aggregate, recipients, frequency,
			 send_hour, enabled, disabled_reason, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.ID, r.RouterID, r.Name, r.Sections, r.Interface, r.Aggregate, r.Recipients,
			r.Frequency, r.SendHour, r.Enabled, r.DisabledReason, r.CreatedBy,
			r.CreatedAt, r.UpdatedAt); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range c.Seed.Runs {
		if _, err := h.Exec(`INSERT INTO report_runs
			(schedule_id, ran_at, period_from, period_to, outcome, source, actor,
			 recipients_n, bytes, rows_n, ms, error)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			r.ScheduleID, r.RanAt, r.PeriodFrom, r.PeriodTo, r.Outcome, r.Source,
			r.Actor, r.Recipients, r.Bytes, r.Rows, r.Ms, r.Error); err != nil {
			t.Fatal(err)
		}
	}
	return openTest(t, dir)
}

func loadHistory(t *testing.T) historyCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "report-history-cases.json"))
	if err != nil {
		t.Skipf("report-history-cases.json missing — regenerate it in the app container: %v", err)
	}
	var c historyCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parsing the cases: %v", err)
	}
	if len(c.Queries) == 0 {
		t.Fatal("no queries — regenerate with tools/report-history-cases.js")
	}
	return c
}

func argStr(t *testing.T, args []any, i int) string {
	t.Helper()
	s, ok := args[i].(string)
	if !ok {
		t.Fatalf("arg %d is %T, want string", i, args[i])
	}
	return s
}

func argFloat(t *testing.T, args []any, i int) float64 {
	t.Helper()
	f, ok := args[i].(float64)
	if !ok {
		t.Fatalf("arg %d is %T, want number", i, args[i])
	}
	return f
}

func argInt(t *testing.T, args []any, i int) int64 {
	t.Helper()
	f, ok := args[i].(float64)
	if !ok {
		t.Fatalf("arg %d is %T, want number", i, args[i])
	}
	return int64(f)
}

// TestHistoryQueriesMatchLive runs each recorded query and compares the rows.
//
// The comparison is on DECODED JSON rather than the encoded text, because the
// two sides order their keys differently — the live aggregate lists target
// before rtt_ms and the Go struct does not — and a byte comparison would fail on
// that alone, which says nothing about the numbers.
func TestHistoryQueriesMatchLive(t *testing.T) {
	c := loadHistory(t)
	d := seededDB(t, c)

	for _, q := range c.Queries {
		var got any
		var err error
		switch q.Fn {
		case "PingSamples":
			got, err = d.PingSamples(argStr(t, q.Args, 0), argInt(t, q.Args, 1), argInt(t, q.Args, 2))
		case "PingSamplesAgg":
			got, err = d.PingSamplesAgg(argStr(t, q.Args, 0), argInt(t, q.Args, 1), argInt(t, q.Args, 2), argStr(t, q.Args, 3))
		case "TrafficSamples":
			got, err = d.TrafficSamples(argStr(t, q.Args, 0), argStr(t, q.Args, 1), argInt(t, q.Args, 2), argInt(t, q.Args, 3))
		case "TrafficSamplesAgg":
			got, err = d.TrafficSamplesAgg(argStr(t, q.Args, 0), argStr(t, q.Args, 1), argInt(t, q.Args, 2), argInt(t, q.Args, 3), argStr(t, q.Args, 4))
		case "BandwidthSamples":
			got, err = d.BandwidthSamples(argStr(t, q.Args, 0), argStr(t, q.Args, 1), argInt(t, q.Args, 2), argInt(t, q.Args, 3))
		case "BandwidthSamplesAgg":
			got, err = d.BandwidthSamplesAgg(argStr(t, q.Args, 0), argStr(t, q.Args, 1), argInt(t, q.Args, 2), argInt(t, q.Args, 3), argStr(t, q.Args, 4))
		case "TrafficInterfaces":
			got, err = d.TrafficInterfaces(argStr(t, q.Args, 0))
		case "BandwidthInterfaces":
			got, err = d.BandwidthInterfaces(argStr(t, q.Args, 0))
		case "AlertEvents":
			got, err = d.AlertEvents(argStr(t, q.Args, 0), argInt(t, q.Args, 1), argInt(t, q.Args, 2))
		case "ConnectivityEvents":
			got, err = d.ConnectivityEvents(argStr(t, q.Args, 0), argInt(t, q.Args, 1), argInt(t, q.Args, 2))
		case "ConnectivityEventsAgg":
			got, err = d.ConnectivityEventsAgg(argStr(t, q.Args, 0), argInt(t, q.Args, 1), argInt(t, q.Args, 2), argStr(t, q.Args, 3))
		case "TrafficSummary":
			got, err = d.TrafficSummary(argStr(t, q.Args, 0), argStr(t, q.Args, 1),
				argInt(t, q.Args, 2), argInt(t, q.Args, 3), argFloat(t, q.Args, 4))
		case "ReportSchedulesFor":
			got, err = d.ReportSchedulesFor(argStr(t, q.Args, 0))
		case "ReportRuns":
			got, err = d.ReportRuns(argStr(t, q.Args, 0), int(argInt(t, q.Args, 1)))
		case "BandwidthSummary":
			got, err = d.BandwidthSummary(argStr(t, q.Args, 0), argStr(t, q.Args, 1),
				argInt(t, q.Args, 2), argInt(t, q.Args, 3))
		default:
			t.Fatalf("%s: no Go function mapped for %q — the generator gained a query "+
				"this test does not run, which would look like coverage and is not", q.Name, q.Fn)
		}
		if err != nil {
			t.Errorf("%s: %v", q.Name, err)
			continue
		}

		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("%s: marshalling the result: %v", q.Name, err)
		}
		var gotAny, wantAny any
		if err := json.Unmarshal(gotJSON, &gotAny); err != nil {
			t.Fatalf("%s: %v", q.Name, err)
		}
		if err := json.Unmarshal(q.Rows, &wantAny); err != nil {
			t.Fatalf("%s: %v", q.Name, err)
		}
		if !reflect.DeepEqual(gotAny, wantAny) {
			t.Errorf("%s:\n  go   %s\n  node %s", q.Name, gotJSON, q.Rows)
		}
	}
}

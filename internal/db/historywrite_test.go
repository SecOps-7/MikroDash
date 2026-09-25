package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"mikrodash/internal/history"
)

// The four history inserts.
//
// Every case here is a way for a chart to be plausibly wrong rather than
// visibly broken: a rate written into a volume column, a lost minute recorded as
// a perfect zero, a bool stored as text that matches no existing row. None of
// them raises an error at write time; all of them are read back by Reports.

// The four history tables, created BEFORE `db.Open` — the pooled-connection
// schema-cache trap this package already documents: DDL applied afterwards
// leaves pooled connections resolving against a schema that predates it.
//
// `historyDDL` is `history_test.go`'s, reused rather than re-typed: a second
// copy of these four CREATE TABLEs is a second place for a column name to drift
// from the one the live inserts use.
func historyDB(t *testing.T) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(historyDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func count(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	if err := d.sql.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEachRowTypeReachesItsOwnTable(t *testing.T) {
	d := historyDB(t)
	rows := []history.Row{
		{Table: "traffic", RouterID: "r-1", Name: "ether1", RxOrRTT: 12.5, TxOrLoss: 3.25, TS: 1699996400000},
		{Table: "bandwidth", RouterID: "r-1", Name: "ether1", RxOrRTT: 90, TxOrLoss: 23, TS: 1699996400000},
		{Table: "ping", RouterID: "r-1", Name: "8.8.8.8", RxOrRTT: 14.5, HasRTT: true, TxOrLoss: 0, TS: 1699996400000},
		{Table: "connectivity", RouterID: "r-1", Connected: true, TS: 1699996400000},
	}
	n, err := d.PersistHistory(rows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("wrote %d of 4", n)
	}
	for table, want := range map[string]int{
		"traffic_samples": 1, "bandwidth_usage": 1, "ping_samples": 1, "connectivity_events": 1,
	} {
		if got := count(t, d, table); got != want {
			t.Errorf("%s has %d rows, want %d", table, got, want)
		}
	}
}

// THE RATE AND THE VOLUME GO TO DIFFERENT COLUMNS.
//
// `traffic_samples.rx_mbps` and `bandwidth_usage.rx_mb` differ by a factor of the
// sampling interval. A swap writes without error and renders as a chart with
// wrong numbers, which is why the values here are distinct and read back by name.
func TestTheRateAndTheVolumeDoNotSwap(t *testing.T) {
	d := historyDB(t)
	if _, err := d.PersistHistory([]history.Row{
		{Table: "traffic", RouterID: "r-1", Name: "ether1", RxOrRTT: 12.5, TxOrLoss: 3.25, TS: 1},
		{Table: "bandwidth", RouterID: "r-1", Name: "ether1", RxOrRTT: 90, TxOrLoss: 23, TS: 1},
	}); err != nil {
		t.Fatal(err)
	}
	var rxMbps, txMbps, rxMb, txMb float64
	if err := d.sql.QueryRow(
		`SELECT rx_mbps, tx_mbps FROM traffic_samples WHERE router_id='r-1'`).Scan(&rxMbps, &txMbps); err != nil {
		t.Fatal(err)
	}
	if err := d.sql.QueryRow(
		`SELECT rx_mb, tx_mb FROM bandwidth_usage WHERE router_id='r-1'`).Scan(&rxMb, &txMb); err != nil {
		t.Fatal(err)
	}
	if rxMbps != 12.5 || txMbps != 3.25 {
		t.Errorf("traffic row is %v/%v, want 12.5/3.25", rxMbps, txMbps)
	}
	if rxMb != 90 || txMb != 23 {
		t.Errorf("bandwidth row is %v/%v, want 90/23", rxMb, txMb)
	}
}

// A MINUTE WHERE EVERY PING WAS LOST HAS A NULL RTT, NOT A ZERO.
//
// Zero renders as a perfect 0 ms round trip on the chart an operator reads to
// see an outage, and drags any average through it.
func TestALostMinuteWritesNullRTT(t *testing.T) {
	d := historyDB(t)
	if _, err := d.PersistHistory([]history.Row{
		{Table: "ping", RouterID: "r-1", Name: "8.8.8.8", RxOrRTT: 0, HasRTT: false, TxOrLoss: 100, TS: 1},
		{Table: "ping", RouterID: "r-1", Name: "1.1.1.1", RxOrRTT: 9.5, HasRTT: true, TxOrLoss: 0, TS: 1},
	}); err != nil {
		t.Fatal(err)
	}
	var rtt *float64
	var loss float64
	if err := d.sql.QueryRow(
		`SELECT rtt_ms, loss_pct FROM ping_samples WHERE target='8.8.8.8'`).Scan(&rtt, &loss); err != nil {
		t.Fatal(err)
	}
	if rtt != nil {
		t.Errorf("a fully lost minute stored rtt_ms=%v, want NULL — zero reads as a perfect "+
			"round trip on the chart that shows the outage", *rtt)
	}
	if loss != 100 {
		t.Errorf("loss_pct = %v, want 100", loss)
	}
	// And the answering target still carries its measurement.
	var ok *float64
	if err := d.sql.QueryRow(
		`SELECT rtt_ms FROM ping_samples WHERE target='1.1.1.1'`).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	if ok == nil || *ok != 9.5 {
		t.Errorf("the answering target stored %v, want 9.5", ok)
	}
}

// `connected` IS AN INTEGER 0/1, matching the live insert and every existing row.
func TestConnectivityStoresAnInteger(t *testing.T) {
	d := historyDB(t)
	if _, err := d.PersistHistory([]history.Row{
		{Table: "connectivity", RouterID: "r-1", Connected: true, TS: 1},
		{Table: "connectivity", RouterID: "r-1", Connected: false, TS: 2},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.sql.Query(`SELECT connected FROM connectivity_events ORDER BY ts`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []int
	for rows.Next() {
		var c int
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("connected did not scan as an integer: %v", err)
		}
		got = append(got, c)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Errorf("connected column holds %v, want [1 0]", got)
	}
}

// AN UNKNOWN TABLE IS AN ERROR, and the batch does not half-commit.
func TestAnUnknownTableFailsTheBatch(t *testing.T) {
	d := historyDB(t)
	n, err := d.PersistHistory([]history.Row{
		{Table: "traffic", RouterID: "r-1", Name: "ether1", RxOrRTT: 1, TxOrLoss: 1, TS: 1},
		{Table: "invented", RouterID: "r-1", TS: 1},
	})
	if err == nil {
		t.Fatal("an unknown table was accepted — a row type the bucketer grew would be " +
			"dropped silently for as long as it took someone to notice a missing chart")
	}
	_ = n
	// THE FIRST ROW WENT WITH IT. The batch is one transaction, so a partial
	// minute cannot reach a reader that aggregates by minute.
	if got := count(t, d, "traffic_samples"); got != 0 {
		t.Errorf("%d traffic rows survived a failed batch — it half-committed", got)
	}
}

// A ROW WITH NO ROUTER is dropped, not written: every query in this schema
// filters on router_id, so such a row is unreadable by construction.
func TestARowWithNoRouterIsDropped(t *testing.T) {
	d := historyDB(t)
	n, err := d.PersistHistory([]history.Row{
		{Table: "traffic", RouterID: "", Name: "ether1", RxOrRTT: 1, TxOrLoss: 1, TS: 1},
		{Table: "traffic", RouterID: "r-1", Name: "ether1", RxOrRTT: 2, TxOrLoss: 2, TS: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reported %d rows written, want 1 — the dropped row was counted", n)
	}
	if got := count(t, d, "traffic_samples"); got != 1 {
		t.Errorf("%d traffic rows, want 1", got)
	}
}

// An empty batch and a nil database are both no-ops rather than errors: the
// drain loop runs on a timer and will meet both.
func TestTheEmptyCasesAreQuiet(t *testing.T) {
	d := historyDB(t)
	if n, err := d.PersistHistory(nil); n != 0 || err != nil {
		t.Errorf("nil batch: %d, %v", n, err)
	}
	var nilDB *DB
	if n, err := nilDB.PersistHistory([]history.Row{{Table: "traffic", RouterID: "r-1"}}); n != 0 || err != nil {
		t.Errorf("nil database: %d, %v", n, err)
	}
	if n := nilDB.PersistHistoryLogged([]history.Row{{Table: "traffic", RouterID: "r-1"}}); n != 0 {
		t.Errorf("nil database, logged form: %d", n)
	}
}

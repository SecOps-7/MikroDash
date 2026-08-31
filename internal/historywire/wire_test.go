package historywire

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/history"
)

// A recording store: what WOULD have been written, in order.
type fakeStore struct{ rows []history.Row }

func (f *fakeStore) PersistHistoryLogged(rows []history.Row) int {
	f.rows = append(f.rows, rows...)
	return len(rows)
}

func on(t *testing.T) (*Wire, *fakeStore) {
	t.Helper()
	s := &fakeStore{}
	return New(true, s), s
}

// Two samples in the same minute produce NOTHING; the third, in the next
// minute, rolls the first minute over. That is the bucketer's contract and the
// reason this wire exists at all rather than inserting per sample.
const min1 = int64(1699996800000) // a minute boundary
const min2 = min1 + 60_000

func TestTrafficRollsOverOnTheMinute(t *testing.T) {
	w, s := on(t)
	w.Record("r-1", "traffic:update", &collect.TrafficSample{
		IfName: "ether1", RxMbps: 8, TxMbps: 4, TS: min1})
	w.Record("r-1", "traffic:update", &collect.TrafficSample{
		IfName: "ether1", RxMbps: 16, TxMbps: 8, TS: min1 + 1000})
	if len(s.rows) != 0 {
		t.Fatalf("samples inside one minute wrote %d rows", len(s.rows))
	}

	w.Record("r-1", "traffic:update", &collect.TrafficSample{
		IfName: "ether1", RxMbps: 1, TxMbps: 1, TS: min2})
	if len(s.rows) == 0 {
		t.Fatal("crossing the minute wrote nothing")
	}
	var tables []string
	for _, r := range s.rows {
		tables = append(tables, r.Table)
		if r.RouterID != "r-1" || r.Name != "ether1" {
			t.Errorf("row %+v has the wrong router or interface", r)
		}
		// THE MIDPOINT OF THE MINUTE, not its floor — `minuteTS + 30000`, in
		// both `history.Writer` and the live `db-writer.js`. A minute's mean
		// belongs at the middle of the interval it describes; stamping it at the
		// edge shifts every point half a minute against the live rows already in
		// the table, which a chart draws as a step where the two meet.
		//
		// My first expectation here was the floor. Checked against
		// `bucket.go:114` and `db-writer.js:44` rather than changed in the code.
		if r.TS != min1+30_000 {
			t.Errorf("row stamped %d, want the minute's midpoint (%d)", r.TS, min1+30_000)
		}
	}
	// THE ROLLOVER PRODUCES BOTH: a rate row and a volume row, from one set of
	// samples. A wire that forwarded only one would leave Bandwidth empty while
	// Traffic looked fine.
	seen := map[string]bool{}
	for _, tb := range tables {
		seen[tb] = true
	}
	if !seen["traffic"] || !seen["bandwidth"] {
		t.Errorf("rollover produced %v, want both traffic and bandwidth", tables)
	}

	// ── AND THE VALUES, WHICH THIS TEST DID NOT CHECK AT FIRST ─────────────
	//
	// It asserted the table, the router, the interface and the timestamp — and a
	// mutant SWAPPING rx and tx on the way into the bucketer survived all four.
	// Rx and tx are the two numbers a throughput chart is entirely made of.
	//
	// Two samples of 8 and 16 Mbps rx, 4 and 8 tx: the rate row carries the MEAN
	// (12 and 6), the volume row the megabytes (24/8 = 3 and 12/8 = 1.5).
	for _, r := range s.rows {
		switch r.Table {
		case "traffic":
			if r.RxOrRTT != 12 || r.TxOrLoss != 6 {
				t.Errorf("rate row is rx=%v tx=%v, want 12 and 6", r.RxOrRTT, r.TxOrLoss)
			}
		case "bandwidth":
			if r.RxOrRTT != 3 || r.TxOrLoss != 1.5 {
				t.Errorf("volume row is rx=%v tx=%v MB, want 3 and 1.5", r.RxOrRTT, r.TxOrLoss)
			}
		}
	}
}

// A PING PAYLOAD WITH NO LOSS READING IS NOT A ZERO-LOSS SAMPLE.
//
// The live guard is `typeof data.loss === 'number'`. A collector that could not
// run emits a payload with no loss, and recording it as 0% writes a minute of
// perfect connectivity for a router that was unreachable.
func TestAPingPayloadWithNoLossIsIgnored(t *testing.T) {
	w, s := on(t)
	w.Record("r-1", "ping:update", &collect.PingPayload{Target: "8.8.8.8", Loss: nil, TS: min1})
	w.Record("r-1", "ping:update", &collect.PingPayload{Target: "8.8.8.8", Loss: nil, TS: min2})
	if len(s.rows) != 0 {
		t.Errorf("a payload with no loss reading wrote %+v", s.rows)
	}
}

// A LOST MINUTE has a loss figure and no round trip, and the row must say so.
func TestALostPingKeepsItsLossAndDropsItsRTT(t *testing.T) {
	w, s := on(t)
	lost, rtt := 100, 12.5
	w.Record("r-1", "ping:update", &collect.PingPayload{
		Target: "8.8.8.8", Loss: &lost, RTT: nil, TS: min1})
	w.Record("r-1", "ping:update", &collect.PingPayload{
		Target: "8.8.8.8", Loss: &lost, RTT: &rtt, TS: min2})

	if len(s.rows) != 1 {
		t.Fatalf("wrote %d rows, want 1", len(s.rows))
	}
	r := s.rows[0]
	if r.Table != "ping" || r.Name != "8.8.8.8" {
		t.Fatalf("wrong row: %+v", r)
	}
	if r.HasRTT {
		t.Error("a minute where every ping was lost reported an RTT — the column would " +
			"store a plausible 0ms on the chart that shows the outage")
	}
	if r.TxOrLoss != 100 {
		t.Errorf("loss = %v, want 100", r.TxOrLoss)
	}
}

// TWO ROUTERS DO NOT SHARE A BUCKET. A shared one would sum one router's
// throughput into another's chart.
func TestRoutersAreBucketedSeparately(t *testing.T) {
	w, s := on(t)
	for _, id := range []string{"r-a", "r-b"} {
		w.Record(id, "traffic:update", &collect.TrafficSample{
			IfName: "ether1", RxMbps: 10, TxMbps: 5, TS: min1})
	}
	w.Flush("r-a")
	if len(s.rows) == 0 {
		t.Fatal("flush wrote nothing")
	}
	for _, r := range s.rows {
		if r.RouterID != "r-a" {
			t.Errorf("flushing r-a wrote a row for %s", r.RouterID)
		}
	}
}

// FLUSH IS WHAT SAVES THE LAST MINUTE. A bucket rolls over only when the NEXT
// minute's first sample arrives, so a session ending mid-minute loses it
// otherwise.
func TestFlushWritesTheOpenMinute(t *testing.T) {
	w, s := on(t)
	w.Record("r-1", "traffic:update", &collect.TrafficSample{
		IfName: "ether1", RxMbps: 10, TxMbps: 5, TS: min1})
	if len(s.rows) != 0 {
		t.Fatal("an open minute was written early")
	}
	w.Flush("r-1")
	if len(s.rows) == 0 {
		t.Error("the open minute was lost on teardown")
	}
}

// ── the switch ──────────────────────────────────────────────────────────────

// A DISABLED WIRE RECORDS NOTHING, not even into its own buckets.
//
// The second half matters: if it bucketed while disabled, enabling it would
// flush a backlog of samples that were never meant to be written — and during
// coexistence Node has already written that same minute.
func TestADisabledWireBucketsNothing(t *testing.T) {
	s := &fakeStore{}
	w := New(false, s)
	for i := 0; i < 3; i++ {
		w.Record("r-1", "traffic:update", &collect.TrafficSample{
			IfName: "ether1", RxMbps: 10, TxMbps: 5, TS: min1 + int64(i)*1000})
	}
	w.Record("r-1", "traffic:update", &collect.TrafficSample{
		IfName: "ether1", RxMbps: 10, TxMbps: 5, TS: min2})
	w.Flush("r-1")
	if len(s.rows) != 0 {
		t.Errorf("a disabled wire wrote %+v", s.rows)
	}
	if w.Enabled() {
		t.Error("Enabled() is true on a disabled wire")
	}
}

// ── TWO EQUIVALENT MUTANTS, recorded rather than chased ───────────────────
//
// Both survived the suite on 2026-08-29 and neither is a missing test:
//
//   Flush's `!w.enabled` guard   A disabled wire never RECORDS, so its buckets
//                                are empty and flushing them yields nothing.
//                                `enabled` is set at construction and cannot be
//                                toggled, so there is no state where the guard
//                                changes the outcome. It stays because it states
//                                the rule at the point a reader looks for it.
//
//   Record's `routerID == ""`    `history.Writer.RecordTraffic` and
//                                `RecordPing` both return nil for an empty
//                                router id already, so the wire's own check is
//                                the second of two. Kept for the same reason,
//                                and because it also skips the type switch.
//
// Contriving a test for either would be testing the mutant rather than the
// behaviour.

func TestANilWireIsInert(t *testing.T) {
	var w *Wire
	w.Record("r-1", "traffic:update", &collect.TrafficSample{IfName: "e", TS: min1})
	w.Flush("r-1")
	if w.Enabled() {
		t.Error("a nil wire reports enabled")
	}
}

// ── routing ─────────────────────────────────────────────────────────────────

// An event with no history rule, and a payload of the wrong type under a known
// name, both record nothing. The type is the guard.
func TestOnlyTheTwoSampleEventsAreRecorded(t *testing.T) {
	w, s := on(t)
	w.Record("r-1", "dns:update", &collect.TrafficSample{IfName: "ether1", TS: min1})
	w.Record("r-1", "traffic:update", &collect.PingPayload{Target: "x", TS: min1})
	w.Record("r-1", "traffic:update", "not a payload")
	w.Record("", "traffic:update", &collect.TrafficSample{IfName: "ether1", TS: min1})
	w.Flush("r-1")
	if len(s.rows) != 0 {
		t.Errorf("wrote %+v", s.rows)
	}
}

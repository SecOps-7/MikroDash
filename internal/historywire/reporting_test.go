package historywire

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/history"
)

// Per-router reporting: whether TRAFFIC and PING history is written for a router.
// Connectivity is not gated by it any more - see conn.go and the two tests below
// that say so.
//
// ── THE HOLDS ARE NOT ENOUGH ───────────────────────────────────────────────
//
// A router with reporting off has no `history` hold, so normally its traffic
// and ping collectors are not running and it produces nothing to record. This
// gate exists for the path the hold does not own: the INTERACTIVE session
// records for any router a browser has open, through its own emit seam.
// Without this, opening a reporting-off router would write rows for as long as
// somebody looked at it.

func TestAReportingOffRouterWritesNoTraffic(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", false)
	samples(w, "r-1", "ether1")
	if len(s.rows) != 0 {
		t.Errorf("wrote %d traffic row(s) for a router with reporting off", len(s.rows))
	}
}

func TestAReportingOffRouterWritesNoPing(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", false)
	rtt, loss := 5.0, 0
	w.Record("r-1", "ping:update", collect.PingPayload{
		Target: "1.1.1.1", RTT: &rtt, Loss: &loss, TS: min1})
	w.Record("r-1", "ping:update", collect.PingPayload{
		Target: "1.1.1.1", RTT: &rtt, Loss: &loss, TS: min2})
	if len(s.rows) != 0 {
		t.Errorf("wrote %d ping row(s) for a router with reporting off", len(s.rows))
	}
}

// connRow is what `internal/connstate` hands to `RecordConn`. Built here rather
// than driving the tracker, because what this file is about is the REPORTING
// GATE — whether a row that has already been decided gets written.
func connRow(routerID string, connected bool, ts int64) history.Row {
	return history.Row{Table: "connectivity", RouterID: routerID, Connected: connected, TS: ts}
}

// TestAReportingOffRouterStillWritesConnectivity — RE-AIMED 2026-10-01, and the
// inversion is the change, not a regression.
//
// This was `TestAReportingOffRouterWritesNoConnectivity`. The Devices page's
// connectivity strip needs every router's reachability, and three of four
// routers on the dev install had written no row in seven days because their
// reporting was off. A connectivity row is one per state CHANGE, not a minute
// series, so recording it for every router costs only the outages themselves.
//
// `TestAPendingDebounceIsNotWrittenAfterReportingIsTurnedOff` was DELETED with
// this, deliberately: it guarded a reporting-off router's armed outage from
// landing after the toggle changed, and there is no longer a toggle for it to
// respect.
func TestAReportingOffRouterStillWritesConnectivity(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", false)
	w.RecordConn([]history.Row{connRow("r-1", true, min1), connRow("r-1", false, min1+1000)})
	if len(s.rows) != 2 {
		t.Errorf("wrote %d connectivity row(s) for a router with reporting off, want 2 - "+
			"its strip on the Devices page would be empty", len(s.rows))
	}
}

// TestConnectivityIgnoresReporting — was `TestConnectivityIsGatedPerRouter`.
// `TickAll` hands the whole fleet's rows over in one call, so the old filter was
// per row; now there is no filter, and BOTH routers' outages must land whatever
// their reporting says. The two settings differ on purpose: a write that still
// consulted the flag would keep exactly one.
func TestConnectivityIgnoresReporting(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-off", false)
	w.SetReporting("r-on", true)
	w.RecordConn([]history.Row{
		connRow("r-off", false, min1), connRow("r-on", false, min1),
	})
	if len(s.rows) != 2 {
		t.Errorf("wrote %v, want both routers' rows - reporting no longer decides "+
			"whether reachability is recorded", s.rows)
	}
}

// TestADisabledWireRecordsNoConnectivity — `-history` off writes nothing, and
// the DEBOUNCE still runs: it lives in `internal/connstate` precisely so the
// badge and the alert do not depend on this flag.
func TestADisabledWireRecordsNoConnectivity(t *testing.T) {
	s := &fakeStore{}
	New(false, s).RecordConn([]history.Row{connRow("r-1", false, min1)})
	if len(s.rows) != 0 {
		t.Errorf("a disabled wire wrote %d rows", len(s.rows))
	}
}

// TestReportingOnStillWrites — the other direction, or every test above passes
// against a recorder that writes nothing at all.
func TestReportingOnStillWrites(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", true)
	samples(w, "r-1", "ether1")
	if len(s.rows) == 0 {
		t.Error("a router with reporting ON wrote nothing")
	}
}

// TestAnUndeclaredRouterStillReports — the safe default, and not a detail: this
// is set from the fleet syncs, so a router seen before the first sync must keep
// the old behaviour rather than go dark.
func TestAnUndeclaredRouterStillReports(t *testing.T) {
	w, s := on(t)
	samples(w, "r-never-declared", "ether1")
	if len(s.rows) == 0 {
		t.Error("a router nothing has declared recorded nothing")
	}
}

// TestReportingIsPerRouter — one router's OFF must not silence another's.
func TestReportingIsPerRouter(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", false)
	w.SetReporting("r-2", true)
	samples(w, "r-2", "ether1")
	if len(s.rows) == 0 {
		t.Fatal("r-2 recorded nothing")
	}
	before := len(s.rows)
	samples(w, "r-1", "ether1")
	if len(s.rows) != before {
		t.Error("r-1 recorded despite having reporting off")
	}
}

package historywire

import (
	"testing"

	"mikrodash/internal/collect"
	"mikrodash/internal/history"
)

// Per-router reporting: whether ANY history is written for a router.
//
// ── THE POOLS ARE NOT ENOUGH ───────────────────────────────────────────────
//
// A router with reporting off has no traffic or ping collector built, so
// normally it produces nothing to record. This gate exists for the path the
// pools do not own: the INTERACTIVE session records for any router a browser
// has open, through its own emit seam, and has never been gated by the
// history-router set. Without this, opening a reporting-off router would write
// rows for as long as somebody looked at it.

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

// TestAReportingOffRouterWritesNoConnectivity — connectivity is report data
// too. Its live Online/Offline status is unaffected: that is the debounce's
// verdict and the `router:status` frame, not this table.
func TestAReportingOffRouterWritesNoConnectivity(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", false)
	w.RecordConn([]history.Row{connRow("r-1", true, min1), connRow("r-1", false, min1+1000)})
	if len(s.rows) != 0 {
		t.Errorf("wrote %d connectivity row(s) for a router with reporting off", len(s.rows))
	}
}

// TestAPendingDebounceIsNotWrittenAfterReportingIsTurnedOff — the gate used to
// live inside the state machine's entry points, so `TickAll` needed its own
// copy of it. Checked at WRITE time there is one gate, and an outage that armed
// while recording was on still must not land after the operator turned it off.
func TestAPendingDebounceIsNotWrittenAfterReportingIsTurnedOff(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", false)
	// The row the debounce produced when it expired, carrying the observed
	// moment from before the setting changed.
	w.RecordConn([]history.Row{connRow("r-1", false, min1+1000)})
	if len(s.rows) != 0 {
		t.Errorf("a debounce armed before reporting was turned off still wrote %d row(s)",
			len(s.rows))
	}
}

// TestConnectivityIsGatedPerRouter — one router with reporting off must not
// silence another's outage in the same sweep. `TickAll` hands the whole fleet's
// rows over in one call, so the filter has to be per row rather than per call.
func TestConnectivityIsGatedPerRouter(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-off", false)
	w.SetReporting("r-on", true)
	w.RecordConn([]history.Row{
		connRow("r-off", false, min1), connRow("r-on", false, min1),
	})
	if len(s.rows) != 1 || s.rows[0].RouterID != "r-on" {
		t.Errorf("wrote %v, want only r-on's row", s.rows)
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

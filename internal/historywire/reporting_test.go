package historywire

import (
	"testing"

	"mikrodash/internal/collect"
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
	w.Record("r-1", "ping:update", &collect.PingPayload{
		Target: "1.1.1.1", RTT: &rtt, Loss: &loss, TS: min1})
	w.Record("r-1", "ping:update", &collect.PingPayload{
		Target: "1.1.1.1", RTT: &rtt, Loss: &loss, TS: min2})
	if len(s.rows) != 0 {
		t.Errorf("wrote %d ping row(s) for a router with reporting off", len(s.rows))
	}
}

// TestAReportingOffRouterWritesNoConnectivity — connectivity is report data
// too. Its live Online/Offline status is unaffected: that comes from the pool's
// status hook and the `router:status` frame, not from this table.
func TestAReportingOffRouterWritesNoConnectivity(t *testing.T) {
	w, s := on(t)
	w.SetReporting("r-1", false)
	w.Connected("r-1", 0, min1)
	w.Disconnected("r-1", 0, min1+1000)
	w.TickAll(min1 + 60_000)
	if len(s.rows) != 0 {
		t.Errorf("wrote %d connectivity row(s) for a router with reporting off", len(s.rows))
	}
}

// TestAPendingDebounceIsNotWrittenAfterReportingIsTurnedOff — `TickAll` does
// not go through `apply`, so it needs its own check. An outage that armed while
// recording was on must not land after the operator turned it off.
func TestAPendingDebounceIsNotWrittenAfterReportingIsTurnedOff(t *testing.T) {
	w, s := on(t)
	w.Connected("r-1", 30_000, min1)
	before := len(s.rows)
	w.Disconnected("r-1", 30_000, min1+1000) // arms the debounce

	w.SetReporting("r-1", false)
	w.TickAll(min1 + 60_000)
	if len(s.rows) != before {
		t.Errorf("a debounce armed before reporting was turned off still wrote %d row(s)",
			len(s.rows)-before)
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

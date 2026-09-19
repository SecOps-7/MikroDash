package session

import (
	"testing"

	"mikrodash/internal/alert"
	"mikrodash/internal/collection"
)

// ALERT MONITORING SAVED IN THE DEVICE DIALOG REACHES THE LIVE SESSION.
//
// `alertsEnabled` was read once, when the session was built, so turning
// alerting on for a router somebody was watching saved the record and took the
// `alerts` hold while the session went on evaluating with the old answer until
// the process restarted. Reported 2026-09-20 as "no netwatch alerts", and
// reproduced live: the rules stayed silent on a test router until a restart.
//
// The reverse matters as much: a router whose alerting is turned OFF must stop
// evaluating at once, not keep notifying until a restart.
func TestSavedAlertMonitoringReachesTheLiveSession(t *testing.T) {
	s := &Session{RouterID: "r1"}
	s.eff.Store(&collection.Resolved{Enabled: map[string]bool{}, Poll: map[string]int{}})
	m := &Manager{live: map[string]*Session{"r1": s}}

	if m.ApplyAlerts("r1", false) {
		t.Error("saving an unchanged switch reported a change, so every router save would log one")
	}
	if !m.ApplyAlerts("r1", true) {
		t.Fatal("turning alert monitoring on reported no change")
	}
	if !s.alertsEnabled.Load() {
		t.Error("the live session still has alerting off after it was turned on")
	}
	// THE READER THAT DECIDES WHETHER A RULE RUNS AT ALL, asked the way the emit
	// closure asks it: a flag nothing consults would pass the check above.
	if !alertRouterOf(s).AlertsEnabled {
		t.Error("the evaluator would still be told this router has alerting off")
	}
	if !s.NeededForAlerts("netwatch") {
		t.Error("netwatch is not kept running for alerting after the switch went on")
	}

	if !m.ApplyAlerts("r1", false) {
		t.Fatal("turning alert monitoring off reported no change")
	}
	if alertRouterOf(s).AlertsEnabled || s.NeededForAlerts("netwatch") {
		t.Error("a router whose alerting was turned off is still evaluated")
	}

	if m.ApplyAlerts("unknown-router", true) {
		t.Error("a router with no live session reported a change")
	}
}

// alertRouterOf is what the emit closure passes to the evaluator, in one place
// so the test asks the same question the running code does.
func alertRouterOf(s *Session) alert.Router {
	return alert.Router{ID: s.RouterID, AlertsEnabled: s.alertsEnabled.Load()}
}

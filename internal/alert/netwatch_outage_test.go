package alert

import (
	"strings"
	"testing"
)

// AN OUTAGE SHORTER THAN ONE READING IS STILL AN OUTAGE (2026-09-20).
//
// The netwatch table is read once a minute. On the operator's hAP AX3 two hosts
// went down and came back in THIRTY SECONDS each: the readings either side both
// said "up", so the status comparison had nothing to report, while the router's
// own on-down script had already told Home Assistant twice. The router stamps
// `since` when a host changes state, and that moving is the evidence.
//
// It is reported as the pair it was, a down and then its recovery, in that
// order: `emit` only reports an "up" when a row was actually closed, so a
// recovery sent first would be swallowed and the operator would see nothing.
func TestAnOutageBetweenTwoReadingsIsCaught(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{NotifNetwatch: true}, store)
	r := Router{ID: "r1", AlertsEnabled: true}
	host := func(status, since string) []NetwatchHost {
		return []NetwatchHost{{ID: "*2", Host: "84.200.70.40", Name: "VPN2", Status: status, Since: since}}
	}

	// The first reading is the baseline, as it is for the status rule.
	if n := len(ev.NetwatchUpdate(r, host("up", "2026-09-19 23:00:00"))); n != 0 {
		t.Fatalf("the first reading fired %d alerts", n)
	}
	// An unchanged host is silent: `since` is what moves, not the clock.
	if n := len(ev.NetwatchUpdate(r, host("up", "2026-09-19 23:00:00"))); n != 0 {
		t.Fatalf("an unchanged host fired %d alerts, so every reading would notify", n)
	}

	// Down and back up between two readings: the router moved `since`.
	fired := ev.NetwatchUpdate(r, host("up", "2026-09-20 01:36:59"))
	if len(fired) != 2 {
		t.Fatalf("a 30-second outage fired %d alerts, want the down and its recovery: %+v", len(fired), fired)
	}
	if fired[0].Up || !strings.Contains(fired[0].Detail, "VPN2") ||
		!strings.Contains(fired[0].Detail, "unreachable") {
		t.Errorf("the first alert is not the outage: %+v", fired[0])
	}
	if !fired[1].Up || !strings.Contains(fired[1].Detail, "reachable again") ||
		!strings.Contains(fired[1].Detail, "2026-09-20 01:36:59") {
		t.Errorf("the second alert is not the recovery, with when the router saw it return: %+v", fired[1])
	}
	// AND THE ROW IS CLOSED. A pair that left the down row open would show an
	// outstanding alert for a host that is up, and would suppress the next one.
	if store.HasOpen("r1", "host_down", "VPN2") {
		t.Error("the recovery did not close the row it raised")
	}

	// The same reading again is silent: `since` has not moved since.
	if n := len(ev.NetwatchUpdate(r, host("up", "2026-09-20 01:36:59"))); n != 0 {
		t.Errorf("the reading after an outage fired %d alerts, so one flap would notify for ever", n)
	}
}

// A HOST THAT IS STILL DOWN IS NOT RE-RAISED ON EVERY FLAP, and a plain
// transition is unaffected by the new rule.
func TestTheOutageRuleLeavesTheOrdinaryTransitionsAlone(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{NotifNetwatch: true}, store)
	r := Router{ID: "r1", AlertsEnabled: true}
	host := func(status, since string) []NetwatchHost {
		return []NetwatchHost{{ID: "*1", Host: "10.255.255.1", Name: "SA VPN", Status: status, Since: since}}
	}

	ev.NetwatchUpdate(r, host("up", "a")) // baseline
	if n := len(ev.NetwatchUpdate(r, host("down", "b"))); n != 1 {
		t.Fatalf("a host going down fired %d alerts, want 1", n)
	}
	// Still down, and `since` moved: it came back and went again. The open row
	// stays open and nothing is re-raised.
	if n := len(ev.NetwatchUpdate(r, host("down", "c"))); n != 0 {
		t.Errorf("a host that is still down fired %d alerts on a flap", n)
	}
	if n := len(ev.NetwatchUpdate(r, host("up", "d"))); n != 1 {
		t.Errorf("the recovery fired %d alerts, want 1", n)
	}
}

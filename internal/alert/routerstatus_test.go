package alert

import (
	"strings"
	"testing"
)

// THE ROUTER'S OWN REACHABILITY, which had no rule at all until 2026-09-20.
//
// `notifRouterStatus` has been a toggle in Settings → Alerts since the Node app
// and no Go code read it, so an operator who turned it on got nothing. These
// drive the rule the way the connectivity debounce drives it: one verdict in,
// one decision out.
func TestARouterGoingOfflineAndComingBack(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{}, store)
	r := Router{ID: "r1", AlertsEnabled: true}

	fired := ev.RouterStatus(r, false)
	if len(fired) != 1 {
		t.Fatalf("an offline router fired %d alerts, want 1: %+v", len(fired), fired)
	}
	if fired[0].Up || !strings.Contains(fired[0].Detail, "not responding") {
		t.Errorf("the alert is not an outage: %+v", fired[0])
	}
	if !store.HasOpen("r1", "router_offline", "") {
		t.Error("nothing was filed; the Alerts page would show no open row")
	}

	// STILL DOWN. The tracker reports a verdict on every connect, so a rule
	// that re-raised would ring on every reconnect attempt of a dead router.
	if n := len(ev.RouterStatus(r, false)); n != 0 {
		t.Errorf("a second offline verdict fired %d alerts", n)
	}

	fired = ev.RouterStatus(r, true)
	if len(fired) != 1 || !fired[0].Up {
		t.Fatalf("the recovery fired %+v, want one resolution", fired)
	}
	if store.HasOpen("r1", "router_offline", "") {
		t.Error("the recovery did not close the row it resolved")
	}
}

// A RECOVERY FOR AN OUTAGE NOBODY WAS TOLD ABOUT IS SILENT.
//
// `history.Connectivity` reports status on EVERY connect, including the
// reconnects that write no row, so this arrives constantly on a healthy router.
// `emit` reports an "up" only when a row was actually closed, and this is the
// case that matters: without it every poll of a perfectly happy fleet would
// notify.
func TestAnUnpairedRecoveryIsSilent(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{}, store)
	r := Router{ID: "r1", AlertsEnabled: true}

	if n := len(ev.RouterStatus(r, true)); n != 0 {
		t.Errorf("a connect with nothing open fired %d alerts — every reconnect "+
			"on a healthy router would notify", n)
	}
}

// THE ROUTER'S OWN SWITCH, which is the one that is left.
//
// This was "both switches": the router's `alertsEnabled` and the install-wide
// `notifRouterStatus`. The second is gone — every alert type is recorded now and
// a notification channel decides what it delivers — so what remains is the
// per-router one, which still means "do not monitor this device at all".
func TestTheRouterStatusRuleHonoursTheRouterSwitch(t *testing.T) {
	unmonitored := NewEvaluator(Settings{}, &memStore{})
	if n := len(unmonitored.RouterStatus(Router{ID: "r1"}, false)); n != 0 {
		t.Errorf("a router with alert monitoring off fired %d alerts", n)
	}
	if n := len(unmonitored.RouterStatus(Router{AlertsEnabled: true}, false)); n != 0 {
		t.Errorf("a router with no id fired %d alerts", n)
	}
}

// ROUTERS ARE INDEPENDENT: one router's outage must not suppress another's.
// The subject is empty for every one of these, so the router id is the ONLY
// thing keeping them apart — which is exactly the shape that goes wrong.
func TestOneRoutersOutageDoesNotSuppressAnothers(t *testing.T) {
	store := &memStore{}
	ev := NewEvaluator(Settings{}, store)

	if n := len(ev.RouterStatus(Router{ID: "r1", AlertsEnabled: true}, false)); n != 1 {
		t.Fatalf("r1 fired %d", n)
	}
	if n := len(ev.RouterStatus(Router{ID: "r2", AlertsEnabled: true}, false)); n != 1 {
		t.Errorf("r2's outage fired %d alerts; it was deduplicated against r1's, "+
			"so a whole site going down would report one router", n)
	}
	// And r1's recovery leaves r2 open.
	ev.RouterStatus(Router{ID: "r1", AlertsEnabled: true}, true)
	if !store.HasOpen("r2", "router_offline", "") {
		t.Error("r1's recovery closed r2's row")
	}
}

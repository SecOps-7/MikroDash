package alert

// THE ROUTER ITSELF GOING OFFLINE, which for most of this app's life was the one
// thing it could not tell you about.
//
// ── THE SETTING EXISTED AND NOTHING READ IT ────────────────────────────────
//
// Settings → Alerts has had a "Router Offline / Online" toggle since the Node
// app; it writes `notifRouterStatus`, and no Go code has ever read that key.
// Every other switch on that panel reaches a rule. This one reached nothing, so
// an operator who turned it on was told about interfaces, hosts, peers and CPU
// on a router that had stopped answering, and never about the router.
//
// ── IT IS NOT DRIVEN BY A COLLECTOR PAYLOAD, AND CANNOT BE ─────────────────
//
// Every other rule here reads rows a collector fetched, which presupposes a
// working connection — the one condition this rule is about. Its input is the
// DEBOUNCED verdict from `internal/connstate`: the router's own "Offline
// threshold", thirty seconds by default, so a six-second blip in a dial loop is
// not an alert. That is also why there is no first-sighting guard of the kind
// NetwatchUpdate needs: the tracker has already decided that a state changed,
// and this evaluator is not the thing remembering it.
//
// ── DEDUPLICATION IS THE STORE'S, NOT A FIELD HERE ────────────────────────
//
// `emit` refuses to re-raise an alert of the same type and subject while one is
// open, and reports an "up" only when a row was actually closed. So a router
// that flaps across a restart — the evaluator rebuilt, its memory gone — does
// not ring twice, and a recovery for an outage nobody was told about is silent.
// That is the same guarantee every other family here gets, and it is why this
// rule carries no state of its own.

// RouterStatus evaluates one debounced connectivity verdict.
//
// THE SUBJECT IS EMPTY, deliberately. `alert_events` rows already carry the
// router id, one router has exactly one connection, and a subject holding the
// label would break its own resolution the first time somebody renamed the
// device: the alert key and the resolution key are the same key.
func (e *Evaluator) RouterStatus(r Router, up bool) []Fired {
	// THE SAME GUARD EVERY RULE HERE OPENS WITH. A router with alert monitoring
	// off evaluates nothing, and an empty id emits nothing — see Router.ID.
	if r.ID == "" || !r.AlertsEnabled {
		return nil
	}
	if up {
		return e.emit(r, Fired{
			Up:          true,
			AlertType:   "Router Online",
			ResolveType: "router_offline",
			Detail:      "The router is reachable again",
		})
	}
	return e.emit(r, Fired{
		AlertType: "Router Offline",
		Detail:    "The router is not responding",
	})
}

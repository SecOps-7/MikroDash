// Package alertpool is the port of `src/alertSessions.js`: a background pool
// that holds a connection to every router NOBODY IS WATCHING, so the app knows
// their Online/Offline state and evaluates their alerts.
//
// ── WHY IT EXISTS, AND WHAT ITS ABSENCE COST ───────────────────────────────
//
// The live app runs TWO background pools and this port had ported one:
//
//	overviewSessions   gated on the Devices page (`_routersPageSockets`).
//	                   `internal/routers.Pool` is its port, and the gating is
//	                   faithful.
//	alertSessions      ALWAYS ON. One session per non-disabled, non-active
//	                   router, whatever page anyone is looking at.
//
// Two consequences, both found on 2026-08-29 and the second reported by the
// operator before it was understood:
//
//  1. ALERTS ONLY FIRED FOR THE ROUTER BEING WATCHED. `alertwire.Evaluate` is
//     reached from exactly one place — the emit closure in `session.go` — and
//     sessions exist only for routers a browser has open. With `-alert-dispatch`
//     on, an operator would believe the fleet was covered while every router but
//     the one on screen was silent.
//  2. NON-ACTIVE ROUTERS READ OFFLINE AT STARTUP, and came online a few seconds
//     after the Devices page was opened — because the only pool that connects to
//     them is the Devices-gated one.
//
// ── THE SPLIT THIS PACKAGE KEEPS ───────────────────────────────────────────
//
// `alertsEnabled` decides whether a session runs COLLECTORS. The live comment
// is explicit that a status-only session needs none: "the ROS connection events
// alone provide Online/Offline state" (`alertSessions.js:97`). So a router with
// alerts off costs one TCP connection and nothing else — which matters on the
// small hardware #105 exists for, and is why this is not simply "run the
// overview pool always".
package alertpool

// Router is what the pool needs to know about one router. A subset of
// `store.Router`, so this package does not depend on the store.
type Router struct {
	ID          string
	Label       string
	Host        string
	Port        int
	TLS         bool
	InsecureTLS bool
	Username    string
	Password    string
	PingTarget  string
	// DefaultIf is carried ONLY for continuous history's traffic collector, and
	// only the router named by `SetHistoryRouter` uses it. Same field the page
	// session passes to `NewTraffic`, so a pooled recording and a page-driven
	// one measure the same interface rather than producing two histories.
	DefaultIf     string
	AlertsEnabled bool
	Disabled      bool
	// Collection is the router's own #105 block, as stored (raw JSON). Resolved
	// against the install settings when the session is built.
	Collection []byte
}

// Plan is what a Sync would do, as data.
//
// ── SEPARATED FROM DOING IT, DELIBERATELY ──────────────────────────────────
//
// The decision — which sessions to build, drop and rebuild — is the part with
// rules in it, and it is the part a test can drive without a router. Every
// verdict below is checkable against `alertSessions.js:28-47` by reading; none
// of it needs a socket.
type Plan struct {
	// Build is the routers with no session that should have one.
	Build []Router
	// Drop is the ids whose session must go: the router is gone, disabled, has
	// become the active one, or is now owned by the overview pool.
	Drop []string
	// Rebuild is the ids whose `alertsEnabled` changed. A flag change cannot be
	// applied in place because it decides whether the session HAS collectors, so
	// the session is torn down and built again — which is what the live loop
	// does by dropping it and letting the second loop recreate it.
	Rebuild []string
}

// Live is the pool's current state, as the planner needs it: id → whether that
// session was built with alerts enabled.
type Live map[string]bool

// PlanSync is `syncSessions`, as a pure function.
//
// ── THE EXCLUSIONS ARE THE WHOLE RULE ──────────────────────────────────────
//
// A session is wanted for every router that is NOT:
//
//	disabled   — "a disabled router is not connected to at all"
//	active     — the interactive session already has it, and a second connection
//	             to the same router is the cost this pool exists to avoid
//	pool-owned — `excluded`, the ids the overview pool holds. The live call site
//	             passes `_poolOwnedIds()` for exactly this.
//
// AN EMPTY activeID IS NOT A WILDCARD. A fresh install has no active router, and
// reading "" as "exclude the router whose id is empty" is correct only because
// no router has an empty id — asserted by a test rather than left to luck.
// sameConnection reports whether two records reach the same router the same way.
//
// The six fields that decide the CONNECTION. `AlertsEnabled` is deliberately
// absent: `PlanSync` already rebuilds on it, and it changes what a session DOES
// rather than where it dials. `Collection` is absent for the opposite reason —
// it is resolved when the session is built, so it needs a rebuild this
// comparison would not give it. That gap is recorded on `Session.eff` in
// internal/session rather than half-closed here.
func sameConnection(a, b Router) bool {
	return a.Host == b.Host && a.Port == b.Port &&
		a.Username == b.Username && a.Password == b.Password &&
		a.TLS == b.TLS && a.InsecureTLS == b.InsecureTLS
}

func PlanSync(all []Router, activeID string, excluded map[string]bool) func(Live) Plan {
	return func(live Live) Plan {
		want := map[string]Router{}
		for _, r := range all {
			if r.Disabled || r.ID == "" || r.ID == activeID || excluded[r.ID] {
				continue
			}
			want[r.ID] = r
		}

		p := Plan{}
		for id, hadAlerts := range live {
			r, ok := want[id]
			if !ok {
				p.Drop = append(p.Drop, id)
				continue
			}
			if hadAlerts != r.AlertsEnabled {
				p.Rebuild = append(p.Rebuild, id)
			}
		}
		for id, r := range want {
			if _, running := live[id]; !running {
				p.Build = append(p.Build, r)
			}
		}
		sortPlan(&p)
		return p
	}
}

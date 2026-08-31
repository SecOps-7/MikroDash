package server

import (
	"log"
	"os"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertpool"
	"mikrodash/internal/collection"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// The always-on pool's wiring — the port of `_syncAlertSessions`.
//
// ── WHAT WAS MISSING, AND WHY IT WAS NOT OBVIOUS ───────────────────────────
//
// The live app runs TWO background pools and this port had ported one.
// `internal/routers.Pool` is `overviewSessions`, gated on the Devices page, and
// that gating is faithful. `alertSessions` is the other, and it is ALWAYS ON:
// one session per non-disabled, non-active router, whatever anyone is looking
// at. Its absence cost two things:
//
//	alerts    `alertwire.Evaluate` is reached from ONE place — the emit closure
//	          in session.go — so with `-alert-dispatch` on, alerts fired only for
//	          the router on screen. An operator would believe the fleet covered.
//	status    non-active routers read Offline until the Devices page was opened,
//	          which is how the operator noticed on 2026-08-29.
//
// ── IT SHARES `-no-pool` WITH THE OVERVIEW POOL, DELIBERATELY ──────────────
//
// Live gates the two separately, because one is bound to a page and the other is
// not. Here they share a switch because the switch means one thing — "do not
// hold background connections to routers nobody is watching" — and somebody
// passing it wants exactly that from both. Documented rather than assumed; if
// finer control is ever needed, splitting the flag is a small change and this
// comment is where to start.
func (s *Server) buildAlertPool(enabled bool) *alertpool.Pool {
	if !enabled {
		log.Printf("[alertpool] off; routers nobody is watching are neither connected " +
			"to nor alerted on (pass -no-pool to keep it that way)")
		return nil
	}
	// The install's merged settings, for resolving each router's #105 config.
	// UNREADABLE IS NOT FATAL: `collection.Resolve(nil, …)` gives every collector
	// its own default, which is what an installation that changed nothing gets.
	var settings map[string]any
	if s.store != nil {
		if raw, err := s.store.Settings(); err == nil {
			merged, _ := store.Merge(raw, os.LookupEnv, s.store)
			settings = merged
		} else {
			log.Printf("[alertpool] settings unreadable (%v); using collector defaults", err)
		}
	}
	return alertpool.New(dialForAlertPool, poolRetry, s.alertPoolStatus, s.alertPoolEvent, settings)
}

func dialForAlertPool(cfg routeros.Config) (alertpool.Conn, error) {
	c, err := routeros.Dial(cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// alertPoolStatus turns a pooled router's Online/Offline change into the frame
// the pages already listen for.
//
// ── THE SAME EVENT THE INTERACTIVE SESSION SENDS ──────────────────────────
//
// `router:status` with `{routerId, connected}` is what `main.ts` records into
// `routerStatus` and what `updateRouterStatusBadge` paints. Inventing a second
// event for pooled routers would mean a second reader, and the Settings table
// would show one kind of router and not the other.
func (s *Server) alertPoolStatus(routerID string, connected bool) {
	s.hub.Broadcast("router-"+routerID, "router:status",
		map[string]any{"routerId": routerID, "connected": connected})
	// The fleet-wide room too: the Settings and Devices tables show every router,
	// not only the one whose room a browser happens to be in.
	s.hub.BroadcastAll("router:status",
		map[string]any{"routerId": routerID, "connected": connected})
}

// alertPoolEvent hands a pooled collector's payload to the alert rules.
//
// The live shim is a `stubIo` whose `emit` forwards to the evaluator and
// discards everything else — no browser is listening to these payloads, and
// nothing renders them. This is that, typed.
func (s *Server) alertPoolEvent(r alertpool.Router, event string, payload any) {
	if s.alerts == nil {
		return
	}
	// THE RETURN VALUE IS THE ALERT. It was discarded here until 2026-08-30,
	// which is why nothing was ever sent — LOOP.md 0k.
	fired := s.alerts.Evaluate(alert.Router{ID: r.ID, AlertsEnabled: r.AlertsEnabled}, event, payload)
	s.dispatchFired(r.ID, r.Label, fired)
}

// syncAlertPool is `_syncAlertSessions()`: every non-disabled router except the
// active one and those the overview pool holds.
//
// Called from the same places as `syncPool`, because the two answer the same
// question — "who is watching what" — and a change that affects one affects the
// other. `excluded` is derived on every call rather than tracked, for the reason
// `syncPool` gives: a second record of who is watching what drifts from the
// first.
func (s *Server) syncAlertPool() {
	if s.alertPool == nil || s.store == nil {
		return
	}
	all, errs := s.store.Routers()
	for _, e := range errs {
		log.Printf("[alertpool] reading the fleet: %v", e)
	}

	// ── THE ACTIVE ROUTER IS *NOT* EXCLUDED HERE, AND THAT IS A DELIBERATE
	//    DIVERGENCE ─────────────────────────────────────────────────────────
	//
	// `_syncAlertSessions` passes `activeRouterId` and skips it, because the live
	// app ALWAYS holds a session for the active router — `_routerSessions` has
	// one whether or not a browser is open, so excluding it costs nothing.
	//
	// THIS PORT HAS NO SUCH SESSION. `session.Manager.Acquire` is ref-counted:
	// the session exists while somebody is looking and is torn down when the last
	// viewer leaves. Excluding the active router by id therefore leaves it
	// covered by NOTHING the moment the last browser closes — no status, no alert
	// evaluation, on the one router the install is pointed at.
	//
	// Measured 2026-08-29: with no browser open, `/healthz` reported the active
	// router down because neither the session nor the pool held it.
	//
	// So the exclusion is by LIVE SESSION rather than by id, below. The behaviour
	// is live's — every router is always covered — and the mechanism differs
	// because the mechanism it relied on is not here. `PlanSync` still takes an
	// activeID and still honours it; passing "" is this caller's decision, and
	// the rule stays tested for the day a persistent session exists.
	const activeID = ""

	// The overview pool's routers, so the two do not both connect to one device.
	excluded := map[string]bool{}
	if s.pool != nil {
		for _, sum := range s.pool.Summaries() {
			excluded[sum.RouterID] = true
		}
	}
	// ...and every router with an interactive session, which already has one.
	if s.sessions != nil {
		for id := range s.sessions.Live() {
			excluded[id] = true
		}
	}

	out := make([]alertpool.Router, 0, len(all))
	for _, r := range all {
		out = append(out, alertpool.Router{
			ID: r.ID, Label: r.Label, Host: r.Host, Port: r.Port,
			TLS: r.TLS, InsecureTLS: r.TLSInsecure,
			Username: r.Username, Password: r.Password,
			PingTarget: r.PingTarget, DefaultIf: r.DefaultIf,
			AlertsEnabled: r.AlertsEnabled,
			Disabled:      r.Disabled, Collection: collectionRaw(r),
		})
	}
	// ── THE HISTORY TARGET IS THE REAL ACTIVE ROUTER, NOT `activeID` ──────
	//
	// `activeID` above is the constant "" ON PURPOSE: passing the real id would
	// make `PlanSync` EXCLUDE the active router from this pool, which is the
	// opposite of what is wanted — it is the router that most needs covering
	// when no browser is open.
	//
	// History needs the actual id, and reusing that constant set the recorded
	// router to "none". Measured: with the two conflated, four minutes with no
	// browser produced zero rows while the pool was connected to all three
	// routers. Two different questions, two different values.
	s.alertPool.SetHistoryRouter(s.activeRouterID())
	s.alertPool.Sync(out, activeID, excluded)
}

// collectionRaw is the router's #105 block as stored, or nil.
func collectionRaw(r store.Router) []byte {
	if len(r.Collection) == 0 {
		return nil
	}
	return r.Collection
}

// Unused-import guard: `collection` is referenced by the doc above and by the
// package this file wires. Kept explicit so a reader looking for where #105 is
// applied finds it named here rather than only inside alertpool.
var _ = collection.Resolve

// activeRouterID reads the install's active router.
//
// Its own function because two callers now need it for two unrelated reasons —
// the history target here, and `syncHistoryRouter` for the overview pool — and a
// second inline settings read is a second thing to get wrong.
func (s *Server) activeRouterID() string {
	if s.store == nil {
		return ""
	}
	cfg, err := s.store.Settings()
	if err != nil {
		return ""
	}
	id, _ := cfg["activeRouterId"].(string)
	return id
}

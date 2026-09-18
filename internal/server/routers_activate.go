package server

import (
	"log"
	"net/http"

	"mikrodash/internal/audit"
)

// `POST /api/routers/{id}/activate` — promote a router to the install-wide
// default.
//
// ── THIS IS THE LAST THING BETWEEN THE FIRST-RUN OVERLAY AND BEING USABLE ──
//
// `web/src/pages/setup-overlay-wire.ts` is complete and gated and deliberately
// unmounted, for one reason: its Connect button adds a router and then activates
// it, and this route was a 404. A first run would have ended with a router in
// the file that nothing had selected.
//
// ── "ALREADY ACTIVE" IS A SUCCESS, NOT A NO-OP TO BE TIDIED AWAY ───────────
//
// The live route answers `{ok:true, alreadyActive:true}` and does nothing else:
// no switch, no audit row, no broadcast. That matters because the overlay and
// the router picker both call this, and re-activating the current router must
// not tear down every session to arrive back where it started.
//
// ── AND THE ANSWER GOES OUT BEFORE THE SWITCH ─────────────────────────────
//
// The live comment: "respond before the async switch". Tearing down sessions and
// building new ones takes seconds against a real router; a client left waiting
// on the response would time out and report a failure for a switch that
// succeeded. So the reply is `{ok:true, switching:true}` — which the overlay
// treats as success, and which is why `if (!d.ok && !d.switching)` is the guard
// there rather than `if (!d.ok)`.
func (s *Server) registerRouterActivate(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/routers/{id}/activate", s.routerActivate)
}

func (s *Server) routerActivate(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	// GLOBAL ADMIN, matching the live `Rbac.requireGlobalAdmin` — and NOT
	// `router:manage` on the target. Activating changes what every OTHER session
	// following the default is looking at, so the permission is about the
	// install, not about the one router.
	if !s.isGlobalAdmin(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "router id is required")
		return
	}

	// UNKNOWN ROUTERS ARE REFUSED, and the live route does not check.
	//
	// A DELIBERATE DIVERGENCE, recorded rather than slipped in: the live handler
	// takes the id straight into `switchRouter`, which fails asynchronously
	// AFTER the 200 has gone out — so a typo produces a cheerful
	// `{ok:true, switching:true}` and then a `router:switch-error` on a socket
	// the caller may not be listening to. The overlay's Connect would report
	// success and leave the operator on a blank dashboard.
	//
	// Refusing up front is possible here only because this route can answer
	// before it commits to anything. It cannot change the outcome of a VALID
	// request, and it turns a silent failure into a 404.
	// `Routers()` returns a SLICE of errors, one per unreadable record, and a
	// partial list alongside them. A record this port cannot decode must not make
	// the whole fleet unactivatable, so the list is used and the errors are
	// logged — the same judgement `routersList` makes.
	all, errs := s.store.Routers()
	for _, e := range errs {
		log.Printf("[routers] activate: reading the fleet: %v", e)
	}
	found := false
	for _, rec := range all {
		if rec.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeJSONErr(w, http.StatusNotFound, "no such router")
		return
	}

	// ── ALREADY ACTIVE ──────────────────────────────────────────────────
	cfg, err := s.store.Settings()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the settings")
		return
	}
	wasActive, _ := cfg["activeRouterId"].(string)
	if wasActive == id {
		writeJSON(w, map[string]any{"ok": true, "alreadyActive": true})
		return
	}

	// ── THE SWITCH ──────────────────────────────────────────────────────
	if err := s.setActiveRouter(id); err != nil {
		// BEFORE THE RESPONSE, because this one is synchronous and cheap: it is
		// a settings write, not a router connection. Reporting `switching:true`
		// over a failed write would tell the overlay to close on an activation
		// that never happened.
		log.Printf("[routers] activate %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not record the active router")
		return
	}

	// RECORDED AFTER THE WRITE and before the moves, so the trail shows the
	// activation even if a session teardown goes wrong afterwards.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "router.activate", TargetType: "router", TargetID: id, RouterID: id,
	})

	writeJSON(w, map[string]any{"ok": true, "switching": true})

	// ── AND THE SESSIONS FOLLOW ─────────────────────────────────────────
	//
	// Only connections that were following the OLD default move. A session
	// pinned to another router by `router:switch` keeps its own view — the live
	// comment says why a global emit would be wrong here: it "would wrongly flip
	// their selector to a router whose data they aren't receiving".
	s.tellFollowers(wasActive, id)
	// The pool's history pair follows the active router. Without this the port
	// would keep recording the OLD router's traffic and ping after a switch, and
	// write nothing for the new one until a restart.
	// `syncHistoryRouter` used to live here: activating a router moved the
	// single history target onto it. Recording is each router's own setting now,
	// and `syncPool` applies a changed flag to live sessions, so an activation
	// no longer decides who records.
	s.broadcastRouterList()
	EvRouterActive.Broadcast(s.hub, "router-"+id, map[string]any{"activeId": id})
}

// RouterFollowPayload tells a browser that was following the default router
// which router is the default now. The browser then selects it itself.
type RouterFollowPayload struct {
	ActiveID string `json:"activeId"`
}

// tellFollowers asks every connection sitting on `from` to follow the default
// to `to`. It does NOT move them.
//
// ── THE BROWSER SELECTS, BECAUSE A SELECT IS WHAT CHECKS ────────────────────
//
// This was `moveFollowers`, and it re-roomed each follower from the HTTP
// handler: `cn.routerID = to`, the new router's room joined. It never asked
// whether the follower may read `to`, never moved the session reference, and
// left `cn.rsession` on the old router (or on a session the DELETE path had
// just closed). A viewer with no grant on the new router received its
// broadcasts; one with write on the new router and read on the old wrote to the
// old while permission was checked against the new (review 2026-09-19). And it
// wrote `conn` fields from a goroutine that does not own them.
//
// `router:select` already does all of it on the connection's own goroutine:
// the grant, the release and acquire, the rooms, the pages and the cards. So a
// follower is TOLD, and one that may not read `to` is not told at all: it stays
// on the router it was showing.
//
// ── IT TAKES `from` ─────────────────────────────────────────────────────────
//
// The two callers ask related but distinct questions:
//
//	DELETE    the sockets that were on the router just removed
//	ACTIVATE  the sockets that were following the OLD DEFAULT
//
// and neither means "everyone not already here". A session pinned to a third
// router by `router:switch` keeps its own view: the live comment on the
// activate route says a global emit "would wrongly flip their selector to a
// router whose data they aren't receiving". An empty `from` (a first
// activation) tells nobody, or every socket that has not picked a router yet
// would be swept up by "" matching "".
func (s *Server) tellFollowers(from, to string) {
	if from == "" || from == to {
		return
	}
	for _, cn := range s.connections() {
		// A snapshot: this runs on an HTTP handler, not the connection's loop.
		sc := cn.scope()
		if sc.routerID != from || sc.sess == nil || !sc.sess.CanReadRouter(to) {
			continue
		}
		EvRouterFollow.Send(s.hub, cn.c, RouterFollowPayload{ActiveID: to})
	}
}

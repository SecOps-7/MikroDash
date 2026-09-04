package server

import (
	"log"
	"net/http"
	"strings"

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
	s.moveFollowers(wasActive, id)
	// The pool's history pair follows the active router. Without this the port
	// would keep recording the OLD router's traffic and ping after a switch, and
	// write nothing for the new one until a restart.
	// `syncHistoryRouter` used to live here: activating a router moved the
	// single history target onto it. Recording is each router's own setting now,
	// and `syncPool` applies a changed flag to live sessions, so an activation
	// no longer decides who records.
	s.broadcastRouterList()
	s.hub.Broadcast("router-"+id, "router:active", map[string]any{"activeId": id})
}

// moveFollowers re-rooms the connections sitting on `from` onto `to`.
//
// ── IT TAKES `from`, AND THE FIRST VERSION DID NOT ─────────────────────────
//
// This was extracted from the DELETE route's promote-a-survivor path, and the
// first extraction moved every connection whose router was not the target —
// which is a DIFFERENT and much wider move. Those two callers ask related but
// distinct questions:
//
//	DELETE    move the sockets that were on the router just removed
//	ACTIVATE  move the sockets that were following the OLD DEFAULT
//
// and neither means "everyone not already here". A session pinned to a third
// router by `router:switch` matches that wider test and must not move: the live
// comment on the activate route says a global emit "would wrongly flip their
// selector to a router whose data they aren't receiving", and dragging their
// rooms across does worse — it changes what they receive.
//
// The de-duplication was still worth doing; the parameter is what makes it
// honest. Two copies of "which rooms does this connection leave" is one prefix
// away from a socket still receiving a router it is no longer looking at.
func (s *Server) moveFollowers(from, to string) {
	if from == "" || from == to {
		return
	}
	for _, cn := range s.connections() {
		if cn.routerID != from {
			continue
		}
		for _, room := range cn.c.Rooms() {
			if strings.HasPrefix(room, "router-"+from) {
				s.hub.Leave(cn.c, room)
			}
		}
		cn.routerID = to
		s.hub.Join(cn.c, "router-"+to)
	}
}

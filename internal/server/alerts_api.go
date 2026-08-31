package server

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/audit"
)

// The notification bell's two write actions.
//
// ── THEY ARE HTTP, NOT SOCKET ACTIONS, AND THAT WAS A CORRECTION ────────────
//
// An earlier draft of `web/src/pages/notifications.ts` wired these buttons to
// `alert:ack` and `alerts:clear-all` over the WebSocket. That protocol was
// INVENTED — `src/index.js` has no such inbound action, and the live page POSTs.
// `inbound-audit` refused the change in one line ("this port EMITS it and ws.go
// does not answer it"), and the emits were removed rather than a receiver built
// for them. These are the routes that were meant all along.
//
// ── ONE LIMITER FOR BOTH, MATCHING THE LIVE `ackLimiter` ────────────────────
//
// 120/minute. Both are cheap local writes with no outbound reach, which is why
// they share a budget where `user-notify` deliberately splits one — there the
// test endpoint makes this server connect to a host the USER chose.

// The live route validates the router id with this before it reaches RBAC or a
// query. Anchored, so it rejects rather than matching a prefix.
var alertRouterIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *Server) registerAlerts(mux *http.ServeMux) {
	ack := newRateLimiter(120, time.Minute).limit
	mux.HandleFunc("POST /api/alerts/{id}/ack", ack(s.alertAck))
	mux.HandleFunc("POST /api/alerts/clear-all", ack(s.alertClearAll))
}

// alertAck acknowledges one alert.
//
// ── THE SCOPE CHECK HAPPENS BEFORE THE WRITE, AND IT HAS TO ─────────────────
//
// The caller supplies an alert ID and nothing else, so there is no router to
// check a permission against until the alert has been looked up. The live
// comment says why this is inline rather than route middleware: the report
// routes take `?routerId` and can be gated by `requirePerm`, and this one
// cannot. Without it, a user restricted to one router could acknowledge alerts
// on every other one.
//
// 404 for an unknown alert comes BEFORE the permission check, deliberately
// matching the live order. It leaks that an id is unused, which is not
// information worth hiding — every alert id is a small integer and the bell
// shows the caller their own.
func (s *Server) alertAck(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.alertSession(w, r)
	if !ok {
		return
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "alert store unavailable")
		return
	}

	// `parseInt` then `Number.isFinite(id) && id > 0`: a zero, a negative and a
	// trailing-garbage id are all rejected before the store is touched.
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONErr(w, http.StatusBadRequest, "bad alert id")
		return
	}

	owner, err := s.auditDB.AlertRouterID(id)
	if err != nil {
		log.Printf("[alerts] ack lookup %d: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the alert")
		return
	}
	if owner == "" {
		writeJSONErr(w, http.StatusNotFound, "no such alert")
		return
	}
	if !s.mayAck(sess, owner) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	who := ""
	if sess != nil {
		who = sess.Username
	}
	row, err := s.auditDB.AcknowledgeAlert(id, who)
	if err != nil {
		log.Printf("[alerts] ack %d: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not acknowledge the alert")
		return
	}

	// RECORDED BEFORE THE 404, exactly as the live route does it. An attempt on a
	// row that vanished between the lookup and the write is still an attempt, and
	// the trail is the only place it appears.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "alert.ack", TargetType: "alert", TargetID: strconv.FormatInt(id, 10),
		RouterID: owner,
	})
	if row == nil {
		writeJSONErr(w, http.StatusNotFound, "no such alert")
		return
	}

	payload := alert.MakeRow(*row, s.routerNames(row.RouterID))
	// EVERY browser on that router, so two people looking at the same alert do
	// not each have to acknowledge it.
	s.hub.Broadcast("router-"+row.RouterID, "alert:acked", payload)
	writeJSON(w, map[string]any{"ok": true, "alert": payload})
}

// alertClearAll resolves and acknowledges every open alert on one router.
//
// ── "CLEAR" RESOLVES, AND THAT IS THE WHOLE POINT OF IT ─────────────────────
//
// The Routers page counts OPEN alerts. An acknowledge-only version — which is
// what this used to be on the live side — emptied the bell and left the router
// reading "Alerting" forever, with no way out for an alert whose condition went
// away without the evaluator ever seeing it clear.
//
// Still gated on `router:ack`: clearing the list is the same operator act as
// acknowledging one row, and it destroys nothing.
func (s *Server) alertClearAll(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.alertSession(w, r)
	if !ok {
		return
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "alert store unavailable")
		return
	}

	var body struct {
		RouterID string `json:"routerId"`
	}
	// A missing or unreadable body is a 400 by the same route as a malformed id:
	// `String((req.body && req.body.routerId) || '')` gives "" for all of them,
	// and "" fails the pattern.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body)
	if !alertRouterIDRe.MatchString(body.RouterID) {
		writeJSONErr(w, http.StatusBadRequest, "bad router id")
		return
	}
	if !s.mayAck(sess, body.RouterID) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	who := ""
	if sess != nil {
		who = sess.Username
	}
	ids, err := s.auditDB.ResolveAllAlerts(body.RouterID, who)
	if err != nil {
		log.Printf("[alerts] clear-all %s: %v", body.RouterID, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not clear the alerts")
		return
	}

	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "alert.clear", TargetType: "alert", RouterID: body.RouterID,
		Extra: []audit.KV{{Key: "cleared", Value: len(ids)}},
	})

	// NO EMIT WHEN NOTHING CHANGED. A clear that found an empty list would
	// otherwise tell every browser to re-render for no reason, and — worse —
	// `alerts:cleared-all` with an empty `ids` is indistinguishable at the
	// receiver from one whose ids it does not hold.
	if len(ids) > 0 {
		var byWho *string
		if who != "" {
			byWho = &who
		}
		s.hub.Broadcast("router-"+body.RouterID, "alerts:cleared-all", map[string]any{
			"routerId": body.RouterID, "ids": ids,
			"clearedAt": nowMillis(), "clearedBy": byWho,
		})
	}
	writeJSON(w, map[string]any{"ok": true, "count": len(ids)})
}

// alertSession is the signed-in check both routes share.
func (s *Server) alertSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	return sess, true
}

// mayAck is `Rbac.can(req.authSession, 'router:ack', routerId)`.
//
// The three short circuits are the ones every permission check in this package
// takes, and they are here rather than folded into `rbac.Can` for the reason the
// live `rbac.js` keeps its own: `authMode: none` has no identity to grant
// anything to, and an RBAC resolver that could not open its tables is an
// install-wide condition reported at startup — turning it into a silent refusal
// would lock every operator out of their own bell.
func (s *Server) mayAck(sess *Session, routerID string) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if s.rbac == nil || !s.rbac.Available() {
		return true // the documented gap, reported at startup
	}
	ok, err := s.rbac.Can(s.userIDFor(sess.Username), "router:ack", routerID)
	if err != nil {
		log.Printf("[rbac] router:ack on %s: %v", routerID, err)
		return false
	}
	return ok
}

// allRouterNames is every router's label, for a payload carrying rows from more
// than one — or many rows from one, where the per-row alternative is a file read
// each. `alerts:open` is the caller.
func (s *Server) allRouterNames() map[string]string {
	if s.store == nil {
		return nil
	}
	routers, _ := s.store.Routers()
	out := make(map[string]string, len(routers))
	for _, rt := range routers {
		if rt.Label != "" {
			out[rt.ID] = rt.Label
		} else {
			out[rt.ID] = rt.Host
		}
	}
	return out
}

// routerNames is the routerID → label map `alert.MakeRow` wants.
//
// ONE ROUTER'S WORTH, because the two callers here emit about one router. The
// connect payload builds the whole map once for up to 250 rows; doing that here
// would read the router file to answer about a single alert.
func (s *Server) routerNames(routerID string) map[string]string {
	if s.store == nil || routerID == "" {
		return nil
	}
	routers, _ := s.store.Routers()
	for _, rt := range routers {
		if rt.ID != routerID {
			continue
		}
		// `_r.label || _r.host`: an unlabelled router shows its address rather
		// than an empty name.
		if rt.Label != "" {
			return map[string]string{routerID: rt.Label}
		}
		return map[string]string{routerID: rt.Host}
	}
	return nil
}

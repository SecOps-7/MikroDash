package server

// ── COEXISTENCE: THESE WRITES CANNOT RUN WHILE NODE IS RUNNING ─────────────
//
// `src/routers.js:loadAll()` is `if (_cache) return _cache;` — no watcher, no
// mtime check — and every Node write does `_cache = routers; _writeFile(routers)`,
// rebuilding the whole file from its own stale in-memory list.
//
// So a write from HERE is invisible to the running Node app, and is silently
// REVERTED by its next save. That is the settings blocker (`src/settings.js:366`)
// applied to a second file; it was found on 2026-08-26 and is recorded in
// the port record as the fourth cutover blocker.
//
// The routes are complete and tested. What they wait on is CUTOVER, not code.

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/rbac"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

// `PUT /api/routers/:id` — router administration.
//
// ── THE PRIVILEGED-FIELD RULE WAS ALREADY HERE ──────────────────────────────
//
// `rbac.StripPrivilegedRouterFields` was ported ahead of this route on purpose,
// with its own tests: "a privilege check that has to be REMEMBERED at the call
// site is the kind that gets forgotten". This is the call site it was waiting
// for, and it is the only thing in this file that is not mechanical.
//
// Site membership is an AUTHORIZATION decision, not router config. This route is
// gated on `router:manage` for the TARGET router — which write access to the
// Devices page confers and which is not global-only — so without the strip a
// non-administrator could inject a device into any scope, repeatably and
// invisibly, with every site id enumerable from an ungated `GET /api/sites`.
//
// The fields are DROPPED, not the request refused: the rest of the edit is
// legitimate and the live route applies it.

// routersPrefix is the route family, for readers. The registration below spells
// its pattern out in full; see the note there.
const routersPrefix = "/api/routers"

func (s *Server) registerRouters(mux *http.ServeMux) {
	rw := newRateLimiter(60, time.Minute).limit
	// A LITERAL PATTERN, not `"PUT "+routersPrefix+"/{id}"`. That is not style:
	// `TestARouterWriteRouteMustStripPrivilegedFields` scans this package's
	// source for router write routes and fails if one exists without a call to
	// `rbac.StripPrivilegedRouterFields`. A concatenated pattern is INVISIBLE to
	// it, so registering the route that way would have hidden it from the only
	// check that guards the escalation — and the guard would have kept passing.
	// ── THE READ, WITHOUT WHICH THE APP CANNOT START ────────────────────
	//
	// `main.ts:loadRouters` calls this before anything else and throws
	// "cannot list routers" if it fails. Go registered POST, PUT and DELETE and
	// NOT the GET — invisible to `endpoint-audit`, which compared PATHS and
	// discarded the verb, so `/api/routers` read as served. Found by running
	// this server in standalone mode against the live /data: the SPA failed to
	// start with a 502. The audit now compares methods too.
	mux.HandleFunc("GET /api/routers", s.routersList)
	mux.HandleFunc("POST /api/routers", rw(s.routerCreate))
	mux.HandleFunc("PUT /api/routers/{id}", rw(s.routerUpdate))
	mux.HandleFunc("DELETE /api/routers/{id}", rw(s.routerDelete))
	// The hot-swap. Registered here rather than in its own block so the four
	// write routes on this path are declared together and `endpoint-audit` sees
	// one list — the audit compares METHOD and PATH, and a route registered
	// somewhere else is exactly how `GET /api/routers` went missing.
	s.registerRouterActivate(mux)
}

// routerCreate is `POST /api/routers`.
//
// ── GLOBAL ADMINISTRATOR, NOT `router:manage` ───────────────────────────────
//
// The PUT is gated per-router because there is a router to gate on. There is no
// such thing for a create — the device does not exist yet — so `router:manage`
// would have nothing to scope to, and granting it fleet-wide is the same as
// granting administration. The live route is `requireGlobalAdmin` for that
// reason.
// routerWriteSession is the signed-in-and-have-a-store check the three write
// routes share.
func (s *Server) routerWriteSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "router store unavailable")
		return nil, false
	}
	return sess, true
}

func (s *Server) routerCreate(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if !s.mayManagePrincipals(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "router store unavailable")
		return
	}

	var body map[string]any
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	// The live route checks this BEFORE reaching the normaliser, and answers 400
	// where the normaliser's own refusals become a 500. Same message.
	if strings.TrimSpace(jsStringOf(body["host"])) == "" {
		writeJSONErr(w, http.StatusBadRequest, "host is required")
		return
	}

	rec, err := s.store.AddRouter(body)
	if err != nil {
		// A validator's refusal — a bad host, port, interface or ping target.
		// These are the caller's fault, so 400 rather than the live 500.
		log.Printf("[routers] create: %v", err)
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}

	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "router.create", TargetType: "router", TargetID: rec.ID,
		TargetName: firstNonEmpty(rec.Label, rec.Host), RouterID: rec.ID,
	})
	s.hub.BroadcastAll("perms:changed", map[string]any{})
	s.broadcastRouterList()
	s.syncPool()
	s.syncAlertPool()

	// THE PASSWORD IS MASKED, not sent back. The live route returns the record
	// with `password: '••••••••'` when one is set, so the form can show the field
	// as configured without receiving it.
	out := routerAuditView(rec)
	out["password"] = ""
	if rec.Password != "" {
		out["password"] = store.Mask
	}
	writeJSON(w, map[string]any{"ok": true, "router": out})
}

// routersList is `GET /api/routers` — the fleet this principal may read, plus
// the active router id.
//
// ── RBAC-FILTERED, AND THROUGH THE SAME PROJECTION THE SOCKET USES ──────────
//
// `routerListForSocket` already applies `visibleRouters`, already strips the
// credential, and — since `a4ac96e` upstream — already withholds the WAN address
// from a principal without `system:settings`. It is what `routers:update` sends
// over the socket. Reusing it is the whole point: a second projection is one
// that can disagree, and the two fields it would disagree about are a router
// password and a WAN address.
//
// This route USED to call a separate unstripped shape, reproducing a live defect
// deliberately. See the header of `devices.go` for why that was right at the
// time and why it is over.
//
// The live route filters on `effectiveRouterIds` rather than the legacy
// `allowedRouterIds`, "because the legacy field cannot express a grant held via
// a group or a site". `visibleRouters` resolves the same way.
func (s *Server) routersList(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	active := ""
	if s.store != nil {
		if cfg, cerr := s.store.Settings(); cerr == nil {
			active, _ = cfg["activeRouterId"].(string)
		}
	}
	// `activeId`, not `activeRouterId` — the live route renames it on the way
	// out and `main.ts` reads the short name.
	writeJSON(w, map[string]any{"routers": s.routerListForSocket(sess), "activeId": active})
}

// jsStringOf is `String(x || ”)` for a decoded JSON value.
func jsStringOf(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func (s *Server) routerUpdate(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "router store unavailable")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "no router id")
		return
	}
	// PER-ROUTER, not global: `router:manage` on THIS router. A principal who may
	// manage one device must not be able to edit another.
	if !s.mayManageRouter(sess, id) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	var body map[string]any
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}

	// ── TYPED FIRST, AND BOTH REASONS MATTER ──────────────────────────────
	//
	// `store.UpdateRouter` writes what it is given, and `Routers()` decodes the
	// whole file in one Unmarshal — so a string where a bool belongs returns ZERO
	// routers, not one bad record. Measured: `{"disabled":"false"}` left the fleet
	// unreadable. See CoerceRouterPatch.
	//
	// It also fixes the guard below, which asserted `.(bool)` on the RAW value: a
	// string "true" failed the assertion, so disabling the active router by
	// sending the string was never refused — while the live app, coercing with
	// `!!`, refuses it.
	body = store.CoerceRouterPatch(body)

	// DISABLING THE ROUTER SOMEBODY IS LOOKING AT IS REFUSED, before anything is
	// written. The live message names the remedy rather than the rule, because an
	// operator who hits this needs to know what to do next.
	if disabled, _ := body["disabled"].(bool); disabled && s.isActiveRouter(id) {
		writeJSONErr(w, http.StatusBadRequest,
			"Switch to another router before disabling this one.")
		return
	}

	// THE STRIP. See the file header and `rbac.StripPrivilegedRouterFields`.
	dropped := rbac.StripPrivilegedRouterFields(body, s.mayManagePrincipals(sess))

	before := s.routerRecord(id)
	if before == nil {
		writeJSONErr(w, http.StatusNotFound, "Router not found")
		return
	}
	if err := s.store.UpdateRouter(id, body); err != nil {
		log.Printf("[routers] update %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the router")
		return
	}
	after := s.routerRecord(id)

	name := id
	if after != nil && after.Label != "" {
		name = after.Label
	} else if after != nil && after.Host != "" {
		name = after.Host
	}
	ev := audit.Event{
		Action: "router.update", TargetType: "router", TargetID: id,
		TargetName: name, RouterID: id,
		Before: routerAuditView(before), After: body,
	}
	// AN ATTEMPT TO SET A PRIVILEGED FIELD IS RECORDED, not silently swallowed.
	// The live route drops the keys and says nothing; this port records the
	// attempt because the trail is the only place a repeated probe would show,
	// and `StripPrivilegedRouterFields` returns the dropped keys for exactly this.
	if len(dropped) > 0 {
		ev.Extra = []audit.KV{{Key: "droppedPrivilegedFields", Value: dropped}}
	}
	s.httpRecorder(r, sess).Record(ev)

	// DISABLING TEARS THE SESSION DOWN. A disabled router must stop being polled
	// at once rather than at the next idle sweep, and the browsers watching it
	// have to be told — they are in its room and would otherwise sit on a page
	// that never updates again.
	//
	// `CloseNow`, NOT `Release`: this said Release when Release WAS a teardown,
	// and the 2026-09-01 idle grace turned it into a two-minute delay against a
	// router the operator had just switched off. `Release` now means "one viewer
	// left" and grants the grace; this is an administrative fact and takes
	// effect at once.
	if disabled, _ := body["disabled"].(bool); disabled {
		if s.sessions != nil {
			s.sessions.CloseNow(id)
		}
		s.hub.Broadcast("router-"+id, "router:disabled", map[string]any{"routerId": id})
	}

	// A MEMBERSHIP CHANGE ALTERS WHO CAN REACH THIS ROUTER, so every cached
	// authorization view in a browser is stale. Easy to miss: it reads as router
	// config.
	//
	// There is no `Rbac.bump()` counterpart here, and that is a property of the
	// port rather than an omission: `internal/rbac` HOLDS NO CACHE, deliberately,
	// because the live app makes these mutations and this process would never see
	// them. Nothing server-side needs invalidating; the browsers do.
	s.hub.BroadcastAll("perms:changed", map[string]any{})
	// PER SOCKET, because each list is filtered for its own principal. See
	// `broadcastRouterList` — `BroadcastAll` here would hand a restricted viewer
	// the whole fleet because somebody else made an edit.
	s.broadcastRouterList()
	s.syncPool()
	s.syncAlertPool()
	writeJSON(w, map[string]any{"ok": true})
}

// routerDelete removes a router and everything that only made sense with it.
//
// ── THE ORDER IS THE LIVE ONE, AND TWO STEPS IN IT ARE NOT OBVIOUS ──────────
//
//  1. The AUDIT IS RECORDED BEFORE THE 404, as it is on the acknowledge route.
//     An attempt on a router that vanished between the read and the write is
//     still an attempt, and the trail is the only place it appears.
//  2. GRANTS GO BEFORE THE SESSION TEARDOWN. A grant naming a removed router is
//     a permission with no visible subject; removing it first means no window in
//     which the device is gone and the access is not.
//
// And what is NOT here: `config_backups` and `audit_events` survive. See
// `internal/db/purge.go` — a restore point is not time-series data, and the trail
// must still say who removed the router.
func (s *Server) routerDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.routerWriteSession(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSONErr(w, http.StatusBadRequest, "no router id")
		return
	}
	if !s.mayManageRouter(sess, id) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	wasActive := s.isActiveRouter(id)
	before := s.routerRecord(id)
	name := id
	if before != nil {
		name = firstNonEmpty(before.Label, before.Host)
	}

	removed, err := s.store.RemoveRouter(id)
	if err != nil {
		log.Printf("[routers] remove %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not remove the router")
		return
	}

	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "router.delete", TargetType: "router", TargetID: id,
		TargetName: name, RouterID: id,
		Note: "router-scoped grants and all stored history for this router were deleted with it",
	})
	if !removed {
		writeJSONErr(w, http.StatusNotFound, "Router not found")
		return
	}

	if s.auditDB != nil {
		if _, err := s.auditDB.DeleteGrantsForScope("router", id); err != nil {
			log.Printf("[routers] grants for %s: %v", id, err)
		}
		// A schedule for a router that no longer exists cannot run, and left
		// behind it is a live outbound email loop.
		if _, err := s.auditDB.DeleteReportSchedulesForRouter(id); err != nil {
			log.Printf("[routers] schedules for %s: %v", id, err)
		}
	}
	s.hub.BroadcastAll("perms:changed", map[string]any{})

	// `CloseNow` for the same reason as the disable path above: a deleted router
	// must stop being polled now, not when a grace meant for page refreshes
	// happens to expire.
	if s.sessions != nil {
		s.sessions.CloseNow(id)
	}
	if s.auditDB != nil {
		if err := s.auditDB.DeleteRouterData(id); err != nil {
			log.Printf("[routers] purge %s: %v", id, err)
		}
	}

	if wasActive {
		s.promoteAfterRemoval(id)
	}
	s.broadcastRouterList()
	s.syncPool()
	s.syncAlertPool()
	writeJSON(w, map[string]any{"ok": true})
}

// promoteAfterRemoval moves on from the router that was active.
//
// ── EVERY SOCKET WATCHING IT HAS TO BE RELOCATED ────────────────────────────
//
// They are in `router-<removed>` rooms that nothing will ever broadcast to
// again, so leaving them there is a page that silently stops updating. The live
// route moves them itself rather than waiting for the browser to notice.
//
// WITH NO ROUTERS LEFT the app is in setup mode: `setup:required` and an EMPTY
// `routers:update`, so a dropdown built from the last payload does not keep
// offering a device that is gone.
func (s *Server) promoteAfterRemoval(removedID string) {
	all, _ := s.store.Routers()
	if len(all) == 0 {
		s.hub.BroadcastAll("setup:required", map[string]any{})
		// REDUNDANT HERE, AND KEPT — recorded rather than counted as a kill.
		// `broadcastRouterList` runs at the end of the request and sends every
		// connection its own list, which with no routers left is also empty. So
		// removing this line survives the suite.
		//
		// It stays because the two have different audiences in principle: this
		// one is the live `io.emit`, reaching every socket including any with no
		// session, while `broadcastRouterList` builds a list PER PRINCIPAL. They
		// coincide only because an empty fleet filters to the same empty list for
		// everybody — which is a fact about this branch, not about the two calls.
		s.hub.BroadcastAll("routers:update", []map[string]any{})
		return
	}

	next := all[0].ID
	if err := s.setActiveRouter(next); err != nil {
		log.Printf("[routers] promote %s: %v", next, err)
	}
	// FROM the removed router, TO the survivor. Shared with the activate route
	// via `moveFollowers`, which takes both ids precisely because the two callers
	// select different connections — see its header.
	s.moveFollowers(removedID, next)
	s.hub.Broadcast("router-"+next, "router:active", map[string]any{"activeId": next})
}

// setActiveRouter records the promotion in the settings file.
func (s *Server) setActiveRouter(id string) error {
	raw, err := s.store.Settings()
	if err != nil {
		return err
	}
	merged, kept := store.Merge(raw, os.LookupEnv, s.store)
	updates := store.Settings{"activeRouterId": id}
	merged["activeRouterId"] = id
	return store.SaveSettings(s.store.Dir, merged, updates, kept, s.store)
}

// mayManageRouter is `Rbac.requirePerm('router:manage', fromParam('id'))`.
func (s *Server) mayManageRouter(sess *Session, routerID string) bool {
	if sess == nil || routerID == "" {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if s.rbac == nil || !s.rbac.Available() {
		return false // editing a router is not in the class of things that fail open
	}
	ok, err := s.rbac.Can(s.userIDFor(sess.Username), "router:manage", routerID)
	if err != nil {
		log.Printf("[rbac] router:manage on %s: %v", routerID, err)
		return false
	}
	return ok
}

// mayManagePrincipals decides whether the privileged fields survive.
func (s *Server) mayManagePrincipals(sess *Session) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if s.rbac == nil || !s.rbac.Available() {
		return false
	}
	ok, err := s.rbac.Can(s.userIDFor(sess.Username), "system:principals", "")
	if err != nil {
		log.Printf("[rbac] system:principals: %v", err)
		return false
	}
	return ok
}

// isActiveRouter reports whether this is the router the settings name as active.
func (s *Server) isActiveRouter(id string) bool {
	cfg, err := s.store.Settings()
	if err != nil {
		return false
	}
	merged, _ := store.Merge(cfg, os.LookupEnv, s.store)
	active, _ := merged["activeRouterId"].(string)
	return active != "" && active == id
}

// routerRecord finds one router, or nil.
func (s *Server) routerRecord(id string) *store.Router {
	all, _ := s.store.Routers()
	for i := range all {
		if all[i].ID == id {
			return &all[i]
		}
	}
	return nil
}

// routerAuditView is the record as the trail may hold it.
//
// THE PASSWORD NEVER REACHES THE ROW. `audit.Diff` masks a field it recognises
// as a credential, and it would recognise this one — but the decrypted plaintext
// is on the struct, and relying on a name-matching pattern to catch it is one
// rename away from writing a router password into a table that is deliberately
// hard to purge. Omitted here instead, so the guarantee is structural.
//
// DEFENCE IN DEPTH, AND THEREFORE UNKILLABLE — recorded rather than counted.
// Adding the field back to this map survives the suite, because `audit.Diff`
// masks it anyway: the name matches `CRED_PATTERN`, and the diff walks AFTER
// keys, so the value is replaced with a marker on both sides. The mutation
// becomes observable only once that pattern stops matching — which is precisely
// the day this omission earns its place.
func routerAuditView(r *store.Router) map[string]any {
	if r == nil {
		return nil
	}
	return map[string]any{
		"id": r.ID, "label": r.Label, "host": r.Host, "port": r.Port,
		"tls": r.TLS, "tlsInsecure": r.TLSInsecure, "username": r.Username,
		"disabled": r.Disabled, "siteIds": store.RouterSiteIDs(*r),
	}
}

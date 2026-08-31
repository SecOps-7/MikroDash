package server

// `PUT /api/sites/:id/routers` — a site's member devices.
//
// ── THIS IS AN AUTHORIZATION WRITE WEARING ROUTER CONFIG'S CLOTHES ──────────
//
// A router's site decides who can reach it through a site-scoped grant, so
// changing the membership list changes who can see and manage a device. The live
// route is `requireGlobalAdmin` for exactly that reason, and `routers_api.go`
// cites this route when explaining why `PUT /api/routers/:id` has to STRIP the
// site fields for anyone who cannot manage principals: the two are the same
// decision, and only one of them may be reachable without administration.
//
// The DECISION is `routers.SiteMembership`, pinned against the live loop by
// `tools/site-membership-cases.js`. What is here is the parts that are not the
// decision: the gate, the 404, the writes, the audit rows and the broadcasts.

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
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/routers"
	"mikrodash/internal/safe"
	"mikrodash/internal/sites"
	"mikrodash/internal/store"
)

func (s *Server) registerSites(mux *http.ServeMux) {
	rw := newRateLimiter(60, time.Minute).limit
	mux.HandleFunc("GET /api/sites", rw(s.sitesList))
	mux.HandleFunc("POST /api/sites", rw(s.siteCreate))
	mux.HandleFunc("PUT /api/sites/{id}", rw(s.siteUpdate))
	mux.HandleFunc("DELETE /api/sites/{id}", rw(s.siteDelete))
	mux.HandleFunc("PUT /api/sites/{id}/routers", rw(s.siteRoutersSet))
}

// siteAdmin is `Rbac.requireGlobalAdmin` plus the store checks the four write
// routes share. GET is deliberately NOT here — see `sitesList`.
func (s *Server) siteAdmin(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, false
	}
	if !s.mayManagePrincipals(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return nil, false
	}
	// `auditDB` is the WHOLE database, not only the trail — the name predates the
	// sites table living in it.
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "database unavailable")
		return nil, false
	}
	return sess, true
}

// broadcastSites is `io.emit('sites:update', db.listSites())`.
//
// THE WHOLE LIST, not a delta. Another administrator adding or removing a site
// must not leave this tab stale, and the list is small enough that a delta would
// buy nothing but a way for two clients to disagree.
func (s *Server) broadcastSites() {
	list, err := s.auditDB.ListSites()
	if err != nil {
		// LOGGED AND DROPPED. The write already succeeded; failing the request
		// here would tell the operator their save did not happen.
		log.Printf("[sites] list for broadcast: %v", err)
		return
	}
	s.hub.BroadcastAll("sites:update", list)
}

// sitesList is `GET /api/sites`.
//
// ── UNGATED BEYOND BEING SIGNED IN, AND THAT IS THE LIVE DESIGN ─────────────
//
// The live route carries no `Rbac` middleware at all. `routers_api.go` names the
// consequence where it strips the site fields from a non-administrator's router
// write: "every site id enumerable from an ungated GET /api/sites". So the ids
// ARE enumerable, and the defence is that knowing an id buys nothing — the two
// routes that act on one require administration. Reproduced rather than
// tightened: a port is not the place to change who can read a page, and the
// Sites card is drawn from this for every viewer of the Settings page.
func (s *Server) sitesList(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	list, err := s.auditDB.ListSites()
	if err != nil {
		log.Printf("[sites] list failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "site list failed")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "sites": list})
}

// siteCreate is `POST /api/sites`.
func (s *Server) siteCreate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.siteAdmin(w, r)
	if !ok {
		return
	}
	var body map[string]any
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body)

	patch, err := sites.ParseSiteBody(body, false)
	if err != nil {
		// `safe.Message` even though every message this validator produces is a
		// fixed string today: `TestNoRawErrorReachesAnHttpBody` refuses a raw
		// `err.Error()` on principle, and it is right to — the day one of those
		// messages starts quoting the value it rejected is the day a path or an
		// address reaches the browser, and nobody would think to revisit here.
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	site, err := s.auditDB.CreateSite(patch.Columns())
	if err != nil {
		// THE UNIQUE INDEX IS THE ENFORCEMENT, not a pre-check — a pre-check
		// would race and one of two simultaneous creates still has to lose.
		if db.IsDuplicateSiteName(err) {
			writeJSONErr(w, http.StatusConflict, "A site with that name already exists")
			return
		}
		log.Printf("[sites] create failed: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "site create failed")
		return
	}

	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "site.create", TargetType: "site", TargetID: site.ID, TargetName: site.Name,
	})
	s.broadcastSites()
	writeJSON(w, map[string]any{"ok": true, "site": site})
}

// siteUpdate is `PUT /api/sites/:id`.
func (s *Server) siteUpdate(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.siteAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	// THE COLUMNS, not the struct: `db.Site` collapses a NULL text column to "",
	// and the after side of the diff writes a true nil for a cleared field. See
	// `db.SiteColumns`. Its nil result is also the 404, so no second lookup.
	before, err := s.auditDB.SiteColumns(id)
	if err != nil {
		log.Printf("[sites] lookup %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "site lookup failed")
		return
	}
	if before == nil {
		writeJSONErr(w, http.StatusNotFound, "No such site")
		return
	}

	var body map[string]any
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body)

	// PARTIAL. An absent field leaves its column alone, which is what stops a
	// rename from blanking a description or a map pin.
	patch, err := sites.ParseSiteBody(body, true)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	cols := patch.Columns()
	site, err := s.auditDB.UpdateSite(id, cols)
	if err != nil {
		if db.IsDuplicateSiteName(err) {
			writeJSONErr(w, http.StatusConflict, "A site with that name already exists")
			return
		}
		log.Printf("[sites] update %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "site update failed")
		return
	}

	name := id
	if site != nil {
		name = site.Name
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "site.update", TargetType: "site", TargetID: id, TargetName: name,
		Before: before, After: cols,
	})
	s.broadcastSites()
	writeJSON(w, map[string]any{"ok": true, "site": site})
}

// siteDelete is `DELETE /api/sites/:id`.
//
// ── THE ORDER OF THE FOUR EFFECTS IS THE INTERESTING PART ───────────────────
//
// Devices are detached FIRST, because a device pointing at a site that no longer
// exists renders a blank chip and is unreachable to a site-scoped grant. Then the
// row goes, then the trail is written — while the site's name is still known —
// and only then are the site-scoped grants removed, which would otherwise outlive
// the site they name.
func (s *Server) siteDelete(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.siteAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	before, err := s.auditDB.GetSite(id)
	if err != nil {
		log.Printf("[sites] lookup %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "site lookup failed")
		return
	}
	if before == nil {
		writeJSONErr(w, http.StatusNotFound, "No such site")
		return
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "router store unavailable")
		return
	}

	// Devices live in routers.json, so SQLite cannot cascade into them.
	detached, err := s.store.ClearSite(id)
	if err != nil {
		// REFUSED BEFORE THE ROW GOES. Removing the site anyway would leave every
		// device pointing at an id that names nothing, with no second chance to
		// detach them — the site would no longer be there to name.
		log.Printf("[sites] detach for %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "could not detach devices")
		return
	}
	if _, err := s.auditDB.DeleteSite(id); err != nil {
		log.Printf("[sites] delete %s: %v", id, err)
		writeJSONErr(w, http.StatusInternalServerError, "site delete failed")
		return
	}

	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "site.delete", TargetType: "site", TargetID: id, TargetName: before.Name,
		Note: "routers detached and site-scoped grants removed",
	})
	if _, err := s.auditDB.DeleteGrantsForScope("site", id); err != nil {
		// The site is gone either way; a grant naming it is now unreachable
		// rather than dangerous, and failing the request would suggest the
		// deletion did not happen.
		log.Printf("[sites] grants for %s: %v", id, err)
	}

	// Detaching devices changes who can reach them, so every cached
	// authorization view is stale.
	s.hub.BroadcastAll("perms:changed", map[string]any{})
	s.broadcastSites()
	if detached > 0 {
		s.broadcastRouterList()
		s.syncPool()
		s.syncAlertPool()
	}
	writeJSON(w, map[string]any{"ok": true, "detached": detached})
}

// siteRoutersSet is `PUT /api/sites/:id/routers`.
func (s *Server) siteRoutersSet(w http.ResponseWriter, r *http.Request) {
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

	siteID := r.PathValue("id")
	// THE 404 COMES FIRST, before the body is even read. Without it an unknown
	// site id would DETACH every device that carries it — the removal branch
	// does not care whether the site exists, and a typo would quietly empty a
	// membership list with a 200.
	// `auditDB` is the WHOLE database, not only the trail — the name predates
	// the sites table living in it.
	if s.auditDB == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	site, err := s.auditDB.GetSite(siteID)
	if err != nil {
		log.Printf("[sites] lookup %s: %v", siteID, err)
		writeJSONErr(w, http.StatusInternalServerError, "site lookup failed")
		return
	}
	if site == nil {
		writeJSONErr(w, http.StatusNotFound, "No such site")
		return
	}

	// `routerIds` MUST BE AN ARRAY, and an absent one is not an empty one. The
	// live route answers 400 rather than treating it as `[]`, because `[]` is a
	// meaningful request — "this site has no devices" — and a client that forgot
	// the field would otherwise empty the site and be told it succeeded.
	var body struct {
		RouterIDs *[]string `json:"routerIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body); err != nil ||
		body.RouterIDs == nil {
		writeJSONErr(w, http.StatusBadRequest, "routerIds must be an array")
		return
	}
	wanted := *body.RouterIDs

	all, _ := s.store.Routers()
	fleet := make([]routers.SiteMemberRouter, 0, len(all))
	for _, rec := range all {
		// The RAW fields, not `store.RouterSiteIDs`: `memberSiteIDs` reproduces
		// that normalisation inside the pure package, and
		// `TestTheTwoSiteIDNormalisersAgree` holds the two together.
		fleet = append(fleet, routers.SiteMemberRouter{
			ID: rec.ID, SiteIDs: rec.SiteIDs, SiteID: rec.SiteID,
		})
	}

	changes := routers.SiteMembership(fleet, siteID, wanted)
	rec := s.httpRecorder(r, sess)
	changed := 0
	for _, c := range changes {
		if err := s.store.UpdateRouter(c.RouterID, map[string]any{"siteIds": c.After}); err != nil {
			// One device failing does not abandon the rest: the live loop has no
			// transaction either, and a half-applied list is better repaired by a
			// second save than by leaving the first half unwritten too.
			log.Printf("[sites] assign %s: %v", c.RouterID, err)
			continue
		}
		changed++
		name := c.RouterID
		if r := siteRouterByID(all, c.RouterID); r != nil {
			name = firstNonEmpty(r.Label, r.Host)
		}
		rec.Record(audit.Event{
			Action: "router.site", TargetType: "router", TargetID: c.RouterID,
			TargetName: name, RouterID: c.RouterID,
			Before: map[string]any{"siteIds": c.Before},
			After:  map[string]any{"siteIds": c.After},
		})
	}

	if changed > 0 {
		// ONE `perms:changed`, where the live route emits it TWICE
		// (`_broadcastPermsChanged(); _broadcastPermsChanged();` — the duplicate
		// looks like an editing slip, since no other write path repeats it). The
		// event carries no payload and every client's handler refetches, so a
		// second one is a duplicate refetch per connection and nothing else.
		// Filed upstream rather than reproduced.
		s.hub.BroadcastAll("perms:changed", map[string]any{})
		s.broadcastRouterList()
		s.syncPool()
		s.syncAlertPool()
	}
	writeJSON(w, map[string]any{"ok": true, "changed": changed})
}

func siteRouterByID(all []store.Router, id string) *store.Router {
	for i := range all {
		if all[i].ID == id {
			return &all[i]
		}
	}
	return nil
}

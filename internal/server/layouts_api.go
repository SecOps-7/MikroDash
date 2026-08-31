package server

// `/api/dashboard-layout` and `/api/topology-layout` — the two saved layouts.
//
// The storage is `internal/db/layouts.go`, ported with the nav preference; the
// position validator is `internal/topology`, ported on 2026-08-25. Only the
// routes were missing.
//
// ── TWO DIFFERENT PERMISSION QUESTIONS, AND THE DIFFERENCE IS THE POINT ─────
//
// The dashboard layout carries NO ROUTER, so it asks `CanPageAnywhere` — the
// live comment: "a scoped check with no target would fail closed and lock
// everyone out". The topology layout DOES carry one, so it asks the scoped
// `CanPage` for that router. Issue #108 is why: before it, these "were a
// cross-router probe: any authenticated session could confirm a router's
// existence and read its saved node positions".
//
// Using the weaker check on the topology route would restore that probe.

import (
	"encoding/json"
	"net/http"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
	"mikrodash/internal/topology"
)

func (s *Server) registerLayouts(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dashboard-layout", s.dashboardLayoutGet)
	mux.HandleFunc("POST /api/dashboard-layout", s.dashboardLayoutSave)
	mux.HandleFunc("GET /api/topology-layout", s.topologyLayoutGet)
	mux.HandleFunc("POST /api/topology-layout", s.topologyLayoutSave)
}

// layoutSession resolves the caller, or writes the refusal and returns nil.
func (s *Server) layoutSession(w http.ResponseWriter, r *http.Request) *Session {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil
	}
	return sess
}

// ── the dashboard layout ────────────────────────────────────────────────────

func (s *Server) dashboardLayoutGet(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil {
		return
	}
	if !s.mayUseDashboard(w, sess) {
		return
	}
	// OWN LAYOUT FIRST, THEN THE SHARED ONE. The live comment: "No layout of
	// their own yet — fall back to the shared one so the client's localStorage
	// cache is refreshed rather than left stale from a previous user." Answering
	// null instead would leave the previous user's arrangement on screen.
	own, err := s.auditDB.Layout(s.layoutUser(sess), "dashboard")
	if err == nil && own != nil {
		writeRawJSON(w, own)
		return
	}
	shared, serr := s.auditDB.Layout(db.SharedLayoutUser, "dashboard")
	if serr != nil || shared == nil {
		// `catch (_) { res.json(null) }` — a read failure is null, never a 500.
		// The dashboard renders its default arrangement and says nothing.
		writeJSON(w, nil)
		return
	}
	writeRawJSON(w, shared)
}

func (s *Server) dashboardLayoutSave(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil {
		return
	}
	if !s.mayUseDashboard(w, sess) {
		return
	}
	// `cards` MUST BE AN ARRAY and is the ONLY field stored. The live route
	// writes `{ cards: body.cards }`, so anything else in the body is dropped —
	// a port storing the whole body would persist whatever a caller sent into a
	// blob the dashboard later reads.
	var body struct {
		Cards *json.RawMessage `json:"cards"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body); err != nil {
		writeJSON400OK(w)
		return
	}
	if body.Cards == nil || !isJSONArray(*body.Cards) {
		writeJSON400OK(w)
		return
	}
	if err := s.auditDB.SetLayout(s.layoutUser(sess), "dashboard",
		map[string]any{"cards": *body.Cards}); err != nil {
		writeJSON500OK(w)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "layout.update", TargetType: "layout", TargetName: "dashboard",
	})
	writeJSON(w, map[string]any{"ok": true})
}

// mayUseDashboard is `_requireDashboard`. A failure to resolve the grant graph
// REFUSES, rather than falling through to the permissive answer.
func (s *Server) mayUseDashboard(w http.ResponseWriter, sess *Session) bool {
	if !permitted(s.rbac.CanPageAnywhere(s.userIDFor(sess.Username), "dashboard", "read")) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return false
	}
	return true
}

// permitted is the fail-closed rule for both checks in this file: allowed only
// when the grant lookup SUCCEEDED and said yes.
//
// ── ITS OWN FUNCTION BECAUSE THE ERROR ARM IS OTHERWISE UNTESTED ────────────
//
// Inline, `err != nil || !ok` mutated to `err == nil && !ok` SURVIVED: no test
// can make the grant graph fail through a route, so a database blip would have
// opened both layouts to anybody. The same extraction `disclosureAllowed` needed
// in `localcc_api.go`, for the same reason and on the same day.
//
// Taking `(bool, error)` positionally so it wraps the call at the site and there
// is nowhere to get the order wrong.
func permitted(ok bool, err error) bool { return err == nil && ok }

// ── the topology layout ─────────────────────────────────────────────────────

// topoLayoutRow is `_readTopoLayoutRow`: the caller's own row, then the shared
// one, then an empty map — the same fallback the dashboard uses.
func (s *Server) topoLayoutRow(sess *Session) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	blob, err := s.auditDB.Layout(s.layoutUser(sess), "topology")
	if err != nil || blob == nil {
		blob, err = s.auditDB.Layout(db.SharedLayoutUser, "topology")
	}
	if err != nil || blob == nil {
		return out
	}
	if json.Unmarshal(blob, &out) != nil {
		return map[string]json.RawMessage{}
	}
	return out
}

func (s *Server) topologyLayoutGet(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil {
		return
	}
	rid := r.URL.Query().Get("routerId")
	// THE PERMISSION CHECK COMES FIRST, and it is SCOPED TO THIS ROUTER.
	// Answering the shape of the refusal before checking would itself be the
	// cross-router probe #108 closed.
	if !s.mayUseTopology(w, sess, rid) {
		return
	}
	if !topology.IsValidRouterID(rid) {
		writeJSON(w, nil)
		return
	}
	// `{ positions: all[rid] || {} }` — an ABSENT router answers an empty map
	// rather than null, so the page draws its computed layout instead of
	// treating the response as a failure.
	positions := s.topoLayoutRow(sess)[rid]
	if len(positions) == 0 {
		positions = json.RawMessage(`{}`)
	}
	writeJSON(w, map[string]any{"positions": positions})
}

func (s *Server) topologyLayoutSave(w http.ResponseWriter, r *http.Request) {
	sess := s.layoutSession(w, r)
	if sess == nil {
		return
	}
	var body struct {
		RouterID  string          `json:"routerId"`
		Positions json.RawMessage `json:"positions"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024)).Decode(&body)
	if !s.mayUseTopology(w, sess, body.RouterID) {
		return
	}
	if !topology.IsValidRouterID(body.RouterID) {
		writeJSON400OK(w)
		return
	}
	// NULL IS A 400, NOT "no positions". `topologyLayout.js`'s own header says
	// so: "callers must treat null as a 400 rather than as 'no positions'" —
	// because treating it as empty turns a malformed save into a silent wipe.
	positions, ok := topology.CleanPositions(body.Positions)
	if !ok {
		writeJSON400OK(w)
		return
	}

	// MERGE. "a save for one router must never discard another router's layout."
	all := s.topoLayoutRow(sess)
	if len(positions) == 0 {
		// Re-layout posts `{}` to reset, and the live code DELETES the key
		// rather than storing an empty object — so the next read falls through
		// to the computed layout instead of restoring an empty arrangement.
		delete(all, body.RouterID)
	} else {
		encoded, merr := json.Marshal(positions)
		if merr != nil {
			writeJSON500OK(w)
			return
		}
		all[body.RouterID] = encoded
	}
	if err := s.auditDB.SetLayout(s.layoutUser(sess), "topology", all); err != nil {
		writeJSON500OK(w)
		return
	}
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "layout.update", TargetType: "layout", TargetName: "topology",
		RouterID: body.RouterID,
	})
	writeJSON(w, map[string]any{"ok": true})
}

// mayUseTopology is `Rbac.requirePage('topology', 'read', routerId)`.
//
// SCOPED, never CanPageAnywhere. Issue #108: before the scoping, these routes
// "were a cross-router probe — any authenticated session could confirm a
// router's existence and read its saved node positions".
func (s *Server) mayUseTopology(w http.ResponseWriter, sess *Session, routerID string) bool {
	if !permitted(s.rbac.CanPage(s.userIDFor(sess.Username), "topology", "read", routerID)) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return false
	}
	return true
}

// writeJSON500OK is the live `res.status(500).json({ ok: false })`.
func writeJSON500OK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"ok":false}`))
}

// writeRawJSON sends a stored blob unchanged. Re-encoding it through `any`
// would reorder its keys and turn floats into whatever Go chooses to print,
// which for an opaque preference is a change with no upside.
func writeRawJSON(w http.ResponseWriter, blob json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(blob)
}

// isJSONArray reports whether a raw message is an array, without decoding it.
// `Array.isArray(body.cards)` is the live check, and the cards are opaque to
// this process — it stores them and never reads inside them.
func isJSONArray(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

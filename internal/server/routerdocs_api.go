package server

// `/api/router-doc` — the operator's own description of a site.
//
// Three documents, one route, because they are one kind of thing: shared
// per-router state with no RouterOS menu behind it. See `internal/sitedoc` for
// what each holds and `internal/db/routerdocs.go` for why they are keyed on the
// router rather than on a person.
//
// ── THE PERMISSION IS THE OWNING PAGE'S, AND IT IS SCOPED ───────────────────
//
// A document belongs to a page — uplinks to WAN, pinned cabling to Network
// Topology, the plan to Wi-Fi Map — so the question "may this user read it" is
// the question "may this user read that page, for THIS router". Answering with
// `CanPageAnywhere` would restore exactly the cross-router probe issue #108
// closed on the topology layout: any signed-in session could confirm a router
// exists and read what somebody had drawn for it.
//
// ── AND AN UNKNOWN KIND IS A 400, NOT A 404 ─────────────────────────────────
//
// A 404 would leak the difference between "no such document type" and "no such
// router", which is the same probe by another route.

import (
	"encoding/json"
	"net/http"

	"mikrodash/internal/audit"
	"mikrodash/internal/sitedoc"
	"mikrodash/internal/topology"
)

// docPages maps a document kind to the page whose grants govern it.
//
// DECLARED, not derived. The two vocabularies are independent — a kind is a
// storage key and a page key is a URL — and a table is what stops a rename of
// either silently widening access.
var docPages = map[string]string{
	sitedoc.KindWANUplinks:    "wan",
	sitedoc.KindTopologyLinks: "network-topology",
	sitedoc.KindWifiMap:       "wifi-map",
}

func (s *Server) registerRouterDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/router-doc", s.routerDocGet)
	mux.HandleFunc("POST /api/router-doc", s.routerDocSave)
}

// mayUseDoc resolves the caller and the grant, or writes the refusal.
//
// FAIL CLOSED through `permitted`, which requires the lookup to have succeeded
// as well as said yes — see its note in layouts_api.go on why that is its own
// function rather than an inline `err != nil ||`.
func (s *Server) mayUseDoc(w http.ResponseWriter, r *http.Request,
	kind, routerID, access string) *Session {

	sess := s.layoutSession(w, r)
	if sess == nil {
		return nil
	}
	page, known := docPages[kind]
	if !known || !topology.IsValidRouterID(routerID) {
		writeJSON400OK(w)
		return nil
	}
	if !permitted(s.rbac.CanPage(s.userIDFor(sess.Username), page, access, routerID)) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return nil
	}
	return sess
}

func (s *Server) routerDocGet(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	rid := r.URL.Query().Get("routerId")
	if s.mayUseDoc(w, r, kind, rid, "read") == nil {
		return
	}
	// A READ FAILURE IS AN EMPTY DOCUMENT, NEVER A 500, for the reason the
	// dashboard layout gives: the page renders its default rather than an error
	// over a stored preference. `Clean` is total, so nil in is a usable document
	// out and the client has one shape to handle.
	blob, err := s.auditDB.Doc(rid, kind)
	if err != nil {
		blob = nil
	}
	doc, ok := sitedoc.Clean(kind, blob)
	if !ok {
		writeJSON400OK(w)
		return
	}
	writeJSON(w, map[string]any{"kind": kind, "routerId": rid, "doc": doc})
}

func (s *Server) routerDocSave(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RouterID string          `json:"routerId"`
		Kind     string          `json:"kind"`
		Doc      json.RawMessage `json:"doc"`
	}
	// 1 MiB: a site plan with a few hundred objects is a few kilobytes, and the
	// caps in `sitedoc` bound what survives anyway. This is the limit on what
	// reaches the decoder at all.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON400OK(w)
		return
	}
	sess := s.mayUseDoc(w, r, body.Kind, body.RouterID, "write")
	if sess == nil {
		return
	}
	// CLEANED BEFORE IT IS STORED, so the database holds only shapes this build
	// understands and every reader gets the same guarantees. See sitedoc.Clean.
	doc, ok := sitedoc.Clean(body.Kind, body.Doc)
	if !ok {
		writeJSON400OK(w)
		return
	}
	if err := s.auditDB.SetDoc(body.RouterID, body.Kind, doc); err != nil {
		writeJSON500OK(w)
		return
	}
	// AUDITED LIKE ANY OTHER WRITE. These documents change what the WAN page
	// calls an uplink and what the topology claims is plugged into what, so
	// "who drew this" is a question somebody will ask.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "sitedoc.update", TargetType: "sitedoc", TargetName: body.Kind,
		RouterID: body.RouterID,
	})
	// The WAN collector reads its document on its slow lane, so a save would
	// otherwise take up to a minute to show. Nudging it here is what makes the
	// toggle feel like a toggle.
	if body.Kind == sitedoc.KindWANUplinks {
		if sn := s.sessions.Live()[body.RouterID]; sn != nil && sn.CollectorEnabled("wan") {
			sn.Wan().RefreshNow()
		}
	}
	writeJSON(w, map[string]any{"ok": true, "doc": doc})
}

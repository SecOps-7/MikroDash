package server

// `GET /api/cities` — the location picker's town search (#96).
//
// ── THE GUARD IS ABOUT RESOURCES, NOT CONFIDENTIALITY ───────────────────────
//
// The live comment says so at length and ends with an instruction: "This guard
// is about resources, not confidentiality: place names are public geographic
// data, and the gazetteer is derived from a database already shipped in the
// image. What it protects is the *build* — the first search costs a few hundred
// milliseconds and tens of megabytes, so an arbitrary viewer should not be able
// to trigger it. Anyone who can edit something that carries a location may
// search: a site (system:principals) or at least one router. Do not 'fix' this
// into a confidentiality guard; there is nothing here to leak."
//
// So the check is deliberately WIDE — either capability passes — and tightening
// it would lock a router administrator out of the picker on the form they are
// filling in.

import (
	"net/http"
)

func (s *Server) registerCities(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/cities", s.citiesSearch)
}

func (s *Server) citiesSearch(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if !s.mayEditALocation(sess) {
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	if !s.cities.Available() {
		// NOT AN ERROR. The live comment: "An install whose geoip data cannot be
		// read still works; it simply cannot offer the picker, and the widget
		// renders that as a message rather than as a failure. Automatic
		// geolocation is unaffected."
		//
		// `cities` is an EMPTY ARRAY beside the flag, not omitted — the widget
		// reads its length before reading the flag.
		writeJSON(w, map[string]any{
			"ok": true, "cities": []any{},
			"unavailable": true, "reason": s.cities.UnavailableReason(),
		})
		return
	}
	writeJSON(w, map[string]any{
		"ok":     true,
		"cities": s.cities.Search(r.URL.Query().Get("q"), r.URL.Query().Get("limit")),
	})
}

// mayEditALocation is `_requireLocationEditor`: `system:principals` OR at least
// one manageable router.
//
// EITHER, never both. A router administrator with no site rights edits a
// router's location; a principals administrator edits a site's. Requiring both
// would lock each of them out of the form they actually fill in.
func (s *Server) mayEditALocation(sess *Session) bool {
	uid := s.userIDFor(sess.Username)
	if conferredHTTP(s.rbac.Can(uid, "system:principals", "")) {
		return true
	}
	ids, err := s.rbac.EffectiveRouterIDs(uid, "router:manage")
	return err == nil && len(ids) > 0
}

// conferredHTTP is the fail-closed `(bool, error)` rule, as `permitted` is in
// layouts_api.go and `conferred` is in internal/rbac. Named apart because Go has
// one package namespace and `permitted` already means this here.
func conferredHTTP(ok bool, err error) bool { return err == nil && ok }

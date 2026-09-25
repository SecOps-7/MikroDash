package server

// `GET /api/nav-prefs` and `POST /api/nav-prefs` — the sidebar's grouped flag
// and which categories are expanded.
//
// Small, and it is here because the cutover dry run on 2026-08-27 showed it
// among the two endpoints still answering 502 with no Node to proxy to. Nothing
// breaks without it — the sidebar falls back to its markup default — but it is
// one of only two console errors left in a standalone run.

import (
	"encoding/json"
	"net/http"

	"mikrodash/internal/db"
)

// ── THE CATEGORY ALLOW-LIST IS GONE, AND SO IS WHAT IT GUARDED ─────────────
//
// There was one, generated from `pages_table.json`, and its own comment called
// it a security property rather than tidiness: `expanded` was an unbounded list
// of arbitrary strings stored in a blob that is later rendered, which is how a
// preference becomes a stored-XSS vector. Filtering through the registry is
// what stopped that.
//
// `expanded` is no longer stored at all - the sidebar starts collapsed and the
// open set never leaves the tab - so this blob holds a single bool. The vector
// is removed BY CONSTRUCTION rather than left unguarded, which is why the
// filter goes with it instead of being kept for a field that no longer exists.

func (s *Server) registerNavPrefs(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/nav-prefs", s.navPrefsGet)
	mux.HandleFunc("POST /api/nav-prefs", s.navPrefsSave)
}

// navPrefsGet answers the stored blob, or `null`.
//
// NULL IS THE ANSWER FOR EVERY FAILURE, matching the live route's
// `catch (_) { res.json(null); }`. A 500 here would put an error in the console
// on every page load for a preference the sidebar can perfectly well do without.
func (s *Server) navPrefsGet(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	blob, lerr := s.ownLayout(sess, "nav")
	if lerr != nil || blob == nil {
		writeJSON(w, nil)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(blob)
}

func (s *Server) navPrefsSave(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	// POINTERS, so "absent" is distinguishable from `false` and from `[]`. A
	// plain bool would read a missing `grouped` as an explicit false and save it.
	// `expanded` IS GONE FROM THIS CONTRACT. Which categories are open stopped
	// being a preference: the sidebar starts fully collapsed every time. It is
	// not merely ignored here - the check below REQUIRED it, so a client that
	// stopped sending it got a 400 and its `grouped` never saved either.
	var body struct {
		Grouped *bool `json:"grouped"`
	}
	// ── THE DECODE ERROR IS NOT OPTIONAL, AND A NIL CHECK IS NOT ENOUGH ──
	//
	// The live checks are `typeof body.grouped !== 'boolean'` and
	// `!Array.isArray(body.expanded)`. ONE JavaScript expression answers two
	// questions there — was the field sent, and is it the right type — and in Go
	// they are separate. **encoding/json ALLOCATES THE POINTER BEFORE it
	// attempts the value**, so `{"grouped": "true"}` leaves `Grouped` non-nil
	// while the bool never decoded, and the nil check alone accepts it.
	//
	// Measured, not assumed: all three of the corpus's wrong-type bodies
	// (`grouped` as a string, `expanded` as a string, `expanded` as an object)
	// arrived with BOTH pointers non-nil and a non-nil error. They were being
	// stored with a garbage `expanded` where the live route answers 400.
	//
	// A malformed body is the same 400 either way — express hands the route
	// `{}` and `typeof undefined !== 'boolean'` refuses it — so returning here
	// on any decode failure matches rather than tightens. Unknown extra fields
	// are still ignored, which is also what the live route does.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body); err != nil {
		writeJSON400OK(w)
		return
	}
	if body.Grouped == nil {
		// `{ ok: false }` with no message, exactly as the live route answers.
		writeJSON400OK(w)
		return
	}

	user := s.layoutUser(sess)
	if user == "" {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	// REPLACED, not merged, so a record written before this change loses its
	// `expanded` list on the next save rather than carrying it for ever.
	if serr := s.auditDB.SetLayout(user, "nav", map[string]any{
		"grouped": *body.Grouped,
	}); serr != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	// NO AUDIT ROW, and the live comment says why: "Expanding a nav category is
	// up to 60 events a minute per user, and a trail that records sidebar clicks
	// is one nobody will read the important rows in."
	writeJSON(w, map[string]any{"ok": true})
}

// writeJSON400OK is the live `res.status(400).json({ ok: false })` — a refusal
// with no message. Kept as its own helper rather than reusing writeJSONErr,
// which sends an `error` string this route does not.
func writeJSON400OK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"ok":false}`))
}

// layoutUser is `_layoutUser(req)`: `authSession.userId || SHARED_LAYOUT_USER`.
//
// ── IT MUST BE THE USER ID, AND THIS PORT GOT IT WRONG FIRST ────────────────
//
// It returned `sess.Username`, with a comment arguing that usernames are unique
// and stable so keying on one costs at most a forgotten sidebar after a rename.
// That reasoning was answering the wrong question. The key is not this port's to
// choose: `user_layouts` is a table BOTH PROCESSES READ, and Node writes the id.
//
// FOUND BY LOOKING AT THE REAL DATABASE after a standalone run — the rows were
//
//	a734a81d-…  nav      <- written by Node
//	ca0584d5-…  nav      <- written by Node, for the `claude` account
//	claude      nav      <- written by THIS PORT, for the same account
//
// so one user had two preferences and neither app could see the other's. Nothing
// errors, nothing logs, and the symptom is a sidebar that forgets its state
// depending on which half served the request. No unit test could have caught it:
// a round trip through one implementation agrees with itself whatever the key.
//
// `userIDFor` resolves the username to the id out of `users.json`, which is what
// `webUserID` in `account_api.go` already did for the session store — the two
// now agree, as they always should have.
//
// ── THE SHARED ROW IS SIGN-IN-OFF'S, AND NOBODY ELSE'S ──────────────────────
//
// With no session, or sign-in off, there is no identity and the shared row is
// the answer. A SIGNED-IN user whose record is gone (deleted while the session
// lived) answers "" — they used to be handed the shared row, so their next save
// overwrote every sign-in-off viewer's layout. A write answers 401 on "", and a
// read treats it as "nothing saved".
func (s *Server) layoutUser(sess *Session) string {
	if sess == nil || sess.AuthMode == "none" {
		return db.SharedLayoutUser
	}
	return s.userIDFor(sess.Username)
}

// ownLayout reads a signed-in user's own row, or nothing when they have none.
func (s *Server) ownLayout(sess *Session, key string) (json.RawMessage, error) {
	user := s.layoutUser(sess)
	if user == "" {
		return nil, nil
	}
	return s.auditDB.Layout(user, key)
}

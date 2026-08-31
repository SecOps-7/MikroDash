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
	"sort"
	"strconv"

	"mikrodash/internal/db"
)

// navCategoryKeys is the allow-list `POST` filters against, generated from the
// LIVE `src/pages.js` into `pages_table.json`.
//
// ── THE FILTER IS A SECURITY PROPERTY, NOT TIDINESS ─────────────────────────
//
// The live comment: "Filtered through the registry rather than stored as sent.
// An unbounded list of arbitrary strings inside a blob that later gets rendered
// is how a preference becomes a stored-XSS vector; there are only ever a handful
// of category keys, and they are all known here."
//
// So it is GENERATED rather than typed. A hand-copied allow-list that gained an
// entry upstream would silently start rejecting a real category; one that lost
// an entry would silently start accepting anything.
var navCategoryKeys = mustCategoryKeys()

func mustCategoryKeys() map[string]bool {
	var f struct {
		CategoryKeys []string `json:"categoryKeys"`
	}
	if err := json.Unmarshal(pagesTableJSON, &f); err != nil {
		panic("server: pages_table.json: " + err.Error())
	}
	if len(f.CategoryKeys) == 0 {
		// A GENERATED ALLOW-LIST THAT ARRIVED EMPTY REFUSES EVERYTHING, which
		// would be a silent feature removal. Better to refuse to start: the
		// table is embedded at build time, so this can only be a build fault.
		panic("server: pages_table.json has no categoryKeys")
	}
	out := make(map[string]bool, len(f.CategoryKeys))
	for _, k := range f.CategoryKeys {
		out[k] = true
	}
	return out
}

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
	blob, lerr := s.auditDB.Layout(s.layoutUser(sess), "nav")
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
	var body struct {
		Grouped  *bool  `json:"grouped"`
		Expanded *[]any `json:"expanded"`
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
	if body.Grouped == nil || body.Expanded == nil {
		// `{ ok: false }` with no message, exactly as the live route answers.
		writeJSON400OK(w)
		return
	}

	// DEDUPLICATED, FILTERED AND SORTED, in that order — `[...new Set(...)]
	// .filter(...).sort()`. The sort is what makes two clients that expanded the
	// same categories in a different order store the same blob.
	seen := map[string]bool{}
	expanded := []string{}
	for _, raw := range *body.Expanded {
		k := jsString(raw)
		if seen[k] || !navCategoryKeys[k] {
			continue
		}
		seen[k] = true
		expanded = append(expanded, k)
	}
	sort.Strings(expanded)

	if serr := s.auditDB.SetLayout(s.layoutUser(sess), "nav", map[string]any{
		"grouped": *body.Grouped, "expanded": expanded,
	}); serr != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not save")
		return
	}
	// NO AUDIT ROW, and the live comment says why: "Expanding a nav category is
	// up to 60 events a minute per user, and a trail that records sidebar clicks
	// is one nobody will read the important rows in."
	writeJSON(w, map[string]any{"ok": true})
}

// jsString is `String(v)` for a decoded JSON value, and `[]any` is what the
// field above decodes into BECAUSE of it.
//
// ── `.map(String)` RUNS BEFORE THE FILTER, SO A NUMBER IS NOT A REFUSAL ─────
//
// `[...new Set(body.expanded.map(String))].filter(...)`. A `[]string` field
// looks like the obvious port and is wrong in the visible direction: it cannot
// decode `[7, null, true]` or `[{...}]`, so the decode fails and the whole
// request is refused — where the live route COERCES each element, gets "7",
// "null", "true", finds none of them in the registry and stores an empty list
// with `ok: true`. Caught by `numbers and nulls` and `a nested object`, which
// exist in the corpus for exactly this.
//
// NOTE `String(null)` IS "null", four characters — not "". `jsval.String` maps
// nil to "" for its own callers, which is right there and wrong here.
//
// IT IS AN EQUIVALENT MUTANT AND THAT WAS MEASURED, not assumed: replacing
// "null" with "" leaves every case in the corpus green, because neither string
// is a category key and both are filtered out. It is written correctly anyway,
// and recorded as undefended rather than left looking tested — a port that
// agrees by accident stops agreeing the moment somebody adds a category called
// "null", or moves the coercion after the filter instead of before it.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		// An object or an array. JavaScript gives "[object Object]" and
		// "a,b"; neither is a category key and neither ever will be, so the
		// exact text is not worth reproducing — what matters is that it is a
		// STRING that the filter then rejects, rather than a decode failure
		// that rejects the whole request.
		return "[object Object]"
	}
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
func (s *Server) layoutUser(sess *Session) string {
	if sess == nil {
		return db.SharedLayoutUser
	}
	id := s.userIDFor(sess.Username)
	if id == "" {
		// A username with no record is the shared identity rather than an empty
		// key: an empty `user_id` would collide with every other empty one and
		// hand a shared preference to whoever asked next.
		return db.SharedLayoutUser
	}
	return id
}

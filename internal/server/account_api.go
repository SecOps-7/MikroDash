package server

// The account modal's own-session endpoints: `GET /api/account/sessions` and
// `POST /api/account/sessions/revoke-others`.
//
// ── STANDALONE ONLY, FOR THE REASON LOGIN IS ────────────────────────────────
//
// Both read `sessions4Web`, and that store is only populated when THIS process
// mints the sessions. While Node is the authority it holds them in its own
// process-local Map, so a Go answer here would be an empty list beside a browser
// that is plainly signed in — "you have no sessions" on the page that exists to
// show you where you are signed in.
//
// So they register with the login routes and under the same condition. Getting
// this wrong is worse than leaving them proxied: an empty list is a confident
// wrong answer, where the proxy gives the right one.
//
// `/api/account/access` and `/api/account/password` are NOT here. See the notes
// at the foot of this file.

import (
	"encoding/json"
	"log"
	"mikrodash/internal/store"
	"net/http"
	"sort"

	"mikrodash/internal/audit"
	"mikrodash/internal/websession"
)

func (s *Server) registerAccount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account/sessions", s.accountSessions)
	mux.HandleFunc("POST /api/account/sessions/revoke-others", s.accountRevokeOthers)
}

// registerAccountAccess is SEPARATE and UNCONDITIONAL, unlike the two above.
//
// `GET /api/account/access` reads the grant graph out of the SQLite database
// both processes share, so Go and Node compute the same answer from the same
// rows — there is no process-local state to be wrong about. The two session
// routes are gated because the session store is this process's alone; applying
// the same gate here would leave a correct answer proxied for no reason.
func (s *Server) registerAccountAccess(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/account/access", s.accountAccess)
}

// accountAccess answers the role names this principal holds, by scope.
func (s *Server) accountAccess(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	summary, serr := s.rbac.AccessSummaryFor(s.userIDFor(sess.Username))
	if serr != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, serr)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "access": summary})
}

// accountSession is one row of the account modal's session list.
//
// THE TOKEN IS NOT IN IT. `listSessionsForUser` includes the token because the
// caller needs it to identify the current session, and its comment says "it must
// be projected away before any of this reaches a browser". This is that
// projection: `current` is the answer the token was needed for, and the token
// itself does not leave the process.
type accountSession struct {
	CreatedAt int64  `json:"createdAt"`
	ExpiresAt *int64 `json:"expiresAt"`
	Current   bool   `json:"current"`
}

func (s *Server) accountSessions(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	token := websession.ParseCookieHeader(r.Header.Get("Cookie"))[websession.CookieName]
	live := s.sessions4Web.ForUser(s.webUserID(sess))

	out := make([]accountSession, 0, len(live))
	for _, ws := range live {
		row := accountSession{CreatedAt: ws.CreatedAt.UnixMilli(), Current: ws.Token == token}
		// NULL FOR "NEVER EXPIRES", not a far-future timestamp: the live route
		// is `s.expiresAt === Infinity ? null : s.expiresAt`, and the modal
		// renders the null as "no expiry" rather than as a date in the year
		// 292277026596.
		if !ws.ExpiresAt.Equal(websession.NeverExpires) {
			ms := ws.ExpiresAt.UnixMilli()
			row.ExpiresAt = &ms
		}
		out = append(out, row)
	}
	// NEWEST FIRST — `sort((a, b) => b.createdAt - a.createdAt)`. Not cosmetic:
	// `ForUser` walks a map, so without this the order changes between requests
	// and the list reshuffles itself every time the modal is opened.
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	writeJSON(w, map[string]any{"ok": true, "sessions": out})
}

// accountRevokeOthers signs this user out everywhere except here.
func (s *Server) accountRevokeOthers(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	token := websession.ParseCookieHeader(r.Header.Get("Cookie"))[websession.CookieName]
	// THE CALLER'S OWN SESSION IS SPARED, or they would be signed out by their
	// own security action.
	revoked := s.sessions4Web.DeleteForUser(s.webUserID(sess), token)
	log.Printf("[account] sessions revoked — user=%q count=%d",
		logSafe(sess.Username), len(revoked))
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "account.sessions.revoke-others", TargetType: "user",
		TargetName: sess.Username,
		Extra:      []audit.KV{{Key: "revoked", Value: len(revoked)}},
	})
	writeJSON(w, map[string]any{"ok": true, "revoked": len(revoked)})
}

// webUserID is the identity `sessions4Web` is keyed on.
//
// `websession.Create` is called with `found.ID` from users.json, so this must
// resolve the same id — keying the LIST on the username while the STORE holds
// the id would return an empty list for everybody, which is the confident wrong
// answer this whole file is arranged to avoid.
func (s *Server) webUserID(sess *Session) string {
	return s.userIDFor(sess.Username)
}

// ── WHAT IS NOT HERE, AND WHY ───────────────────────────────────────────────
//
// `GET /api/account/access` is `Rbac.accessSummaryFor(userId)` — the role names
// a principal holds globally, per site and per router. Every input is ported
// (`internal/rbac` reads the grant graph, `internal/db` has the role, site and
// router names), so this is porting work rather than a blocker, and it is left
// for its own pass because it wants a generated corpus: the live function DROPS
// entries whose site or router has been deleted, and a port that rendered
// "null" at somebody instead would pass any test written from the happy path.
//
// `POST /api/account/password` WRITES users.json, and that is the reason it is
// not here. `src/users.js` caches the file on first load exactly as
// `settings.js` and `routers.js` do — `tools/coexistence-audit.js` covers all
// three — so a change written from this side would be invisible to the running
// Node app and reverted by its next save. Worse than useless for a password: the
// operator would be told their password had changed, and it would not have.
//
// It is a CUTOVER step, not a port step, and it stays proxied until then.

// registerAuthPermissions is `GET /api/auth/permissions` — the same capability
// object `/api/auth/status` nests under `session.caps`, on its own.
//
// UNCONDITIONAL, like `/api/account/access` and for the same reason: it is
// computed from the grant graph in the database both processes share, so Go and
// Node answer identically from the same rows.
//
// The client asks for it separately after a router switch, because `caps.pages`
// is a union across READABLE ROUTERS and that set can change — see `capsFor`.
func (s *Server) registerAuthPermissions(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/permissions", s.authPermissions)
}

func (s *Server) authPermissions(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	caps, cerr := s.rbac.CapabilitiesFor(s.userIDFor(sess.Username))
	if cerr != nil {
		// `catch { res.status(500).json({ ok: false }) }` — and NOT a partial
		// answer. Empty capabilities would read as "you may do nothing", which
		// is indistinguishable from a real refusal in the UI.
		writeJSON500OK(w)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "caps": caps})
}

// registerAccountPassword is `POST /api/account/password`.
//
// ── STANDALONE ONLY, LIKE THE SESSION ROUTES ABOVE, AND FOR A HARDER REASON ─
//
// It WRITES users.json, and `src/users.js` caches the file on first load and
// never re-reads it — so while Node is running, a change written here is
// invisible to it AND reverted by its next save. The operator would be told
// their password had changed when it had not, which is worse than the endpoint
// being absent.
//
// The hazard is coexistence and nothing else, so the route registers exactly
// where that hazard does not exist.
func (s *Server) registerAccountPassword(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/account/password", s.accountPassword)
}

// minPasswordLen is the live floor: `String(newPassword).length < 4`.
//
// The live comment records that `PUT /api/users/:id` SKIPS this check and calls
// it "a gap on that route — not one to inherit here". Reproduced with the gap
// left where it is: closing it on the other route is a change to the live app,
// not a porting decision.
const minPasswordLen = 4

func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body)
	if body.CurrentPassword == "" || body.NewPassword == "" {
		writeJSONErr(w, http.StatusBadRequest, "Current and new password are required")
		return
	}
	if len(body.NewPassword) < minPasswordLen {
		writeJSONErr(w, http.StatusBadRequest, "Password too short")
		return
	}

	// THE RAW RECORD, because verifying needs the hash and the salt.
	users, uerr := s.store.Users()
	if uerr != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, uerr)
		return
	}
	var found store.User
	for _, u := range users {
		if u.Username == sess.Username {
			found = u
			break
		}
	}
	if found.ID == "" {
		// A SIGNED-IN SESSION WHOSE RECORD IS GONE. The live route answers 404
		// rather than 401: the caller is authenticated, and telling them their
		// credentials are wrong would send them to re-enter a password for an
		// account that no longer exists.
		writeJSONErr(w, http.StatusNotFound, "User not found")
		return
	}
	if !store.VerifyPassword(found, body.CurrentPassword) {
		writeJSONErr(w, http.StatusUnauthorized, "Current password is incorrect")
		return
	}

	if serr := s.store.SetPassword(found.ID, body.NewPassword); serr != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, serr)
		return
	}

	// ── THE OTHER SESSIONS GO WITH IT ───────────────────────────────────
	//
	// "A password change is often a response to a suspected compromise, so the
	// other sessions go with it. The caller's own session is spared, or they
	// would be signed out by their own security action."
	token := websession.ParseCookieHeader(r.Header.Get("Cookie"))[websession.CookieName]
	revoked := s.sessions4Web.DeleteForUser(s.webUserID(sess), token)
	log.Printf("[account] password changed — user=%q othersRevoked=%d",
		logSafe(sess.Username), len(revoked))
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "account.password", TargetType: "user",
		TargetID: found.ID, TargetName: found.Username,
		Extra: []audit.KV{{Key: "otherSessionsRevoked", Value: len(revoked)}},
	})
	// NO PASSWORD IN THE AUDIT ROW, and none in the log line either — neither
	// the old one nor the new. The count is the whole record of what happened.
	writeJSON(w, map[string]any{"ok": true, "revokedOtherSessions": len(revoked)})
}

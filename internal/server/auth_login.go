package server

// `POST /api/auth/login` and `GET /api/auth/logout`, served by Go — but ONLY
// when there is no Node to delegate to.
//
// ── THE PROBLEM THIS SOLVES, AND WHY IT WAS INVISIBLE ───────────────────────
//
// `auth.go` delegates authentication to Node on purpose: sessionStore.js keeps
// sessions in a process-local Map, so Go cannot mint a `mikrodash_sid` that Node
// would honour, and a browser holding a Go-minted cookie would be
// unauthenticated the moment it touched a proxied page. One source of truth for
// as long as both halves run.
//
// At cutover, with Node stopped, that leaves NOBODY ABLE TO LOG IN. Found on
// 2026-08-27 by standing this server against the live /data for the first time
// and discovering there is no Go handler for the login route at all. auth.go
// states the constraint that leads here and stops short of the consequence, and
// the RBAC gap it DOES record was closed — so the file reads as though its
// cutover problem is solved.
//
// ── WHY IT IS CONDITIONAL, WHICH IS THE PART THAT MATTERS ───────────────────
//
// Registering these routes unconditionally would BREAK COEXISTENCE, silently and
// immediately. `/api/auth/login` is proxied today; a Go handler at the same
// pattern stops it reaching Node, so the browser gets a Go session that Node
// does not know, and every unported page starts answering 401. The bug would
// look like "the login works but half the app logged me out".
//
// So they are registered only when `-node` is empty, which is what cutover
// means: nothing to proxy to, and therefore nothing whose session view could
// disagree. `standalone` is that condition, and `Handler` says so at the call.

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/store"
	"mikrodash/internal/websession"
)

// sessionTimeout is `_sessionTimeoutMs()`. Zero means never expires.
//
// Read from settings on every login rather than captured, so an operator
// shortening the timeout affects the next sign-in instead of the next restart —
// which is what the live app does, since it calls the helper inside the handler.
func (s *Server) sessionTimeout() time.Duration {
	if s.store == nil {
		return 0
	}
	cfg, err := s.store.Settings()
	if err != nil {
		return 0
	}
	// The live key, and it is in MILLISECONDS. A port reading it as seconds
	// gives every session a timeout 1000x too short, which reads as "the app
	// keeps logging me out" rather than as a unit bug.
	ms, _ := cfg["sessionTimeoutMs"].(float64)
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}

func (s *Server) registerAuthLogin(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("GET /api/auth/logout", s.authLogout)
}

// authLogin verifies a password against users.json and mints a session.
func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	// A malformed body is the same answer as an empty one: the live route reads
	// `req.body || {}` and then checks the two fields, so it cannot distinguish
	// them either.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	if body.Username == "" || body.Password == "" {
		writeJSONErr(w, http.StatusBadRequest, "Missing credentials")
		return
	}

	users, err := s.store.Users()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	// A ZERO USER, NOT A NIL ONE, so the constant-time path in VerifyPassword is
	// reached for a name nobody has. Returning early here would reintroduce the
	// username-enumeration oracle that store.VerifyPassword exists to close, and
	// it would do it one layer higher where no test was looking.
	var found store.User
	for _, u := range users {
		if u.Username == body.Username {
			found = u
			break
		}
	}
	if !store.VerifyPassword(found, body.Password) {
		// THE CLAIMED NAME, NOT A RESOLVED ONE. A failed login may name a user
		// that does not exist, and that is exactly what is worth seeing in the
		// audit trail.
		log.Printf("[auth] login failed — user=%q", logSafe(body.Username))
		s.loginRecorder(r, body.Username).Record(audit.Event{
			Action: "auth.login", TargetType: "user", TargetName: body.Username,
			Outcome: "denied",
		})
		// ONE MESSAGE FOR BOTH FAILURES, matching the live text exactly: a
		// response that distinguished "no such user" from "wrong password"
		// would hand back by content what the constant-time hashing withholds
		// by timing.
		writeJSONErr(w, http.StatusUnauthorized, "Invalid username or password")
		return
	}

	sess, err := s.sessions4Web.Create(found.ID, found.Username, found.Role,
		s.sessionTimeout(), nil)
	if err != nil {
		// A FAILED RANDOM READ IS A 500, never a weaker token. See
		// websession.Create: a predictable token is a silent auth bypass, and an
		// operator retrying a 500 costs nothing by comparison.
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Set-Cookie",
		s.sessions4Web.BuildCookieHeader(sess.Token, sess.ExpiresAt, s.forceHTTPS))
	log.Printf("[auth] login — user=%q role=%s", found.Username, found.Role)
	s.loginRecorder(r, found.Username).Record(audit.Event{
		Action: "auth.login", TargetType: "user",
		TargetID: found.ID, TargetName: found.Username,
	})
	writeJSON(w, map[string]any{"ok": true, "role": found.Role, "username": found.Username})
}

// authLogout clears the session. It answers ok even for a token nobody has —
// the live route does, and a logout that reported failure would leave a browser
// looking signed in.
func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	token := websession.ParseCookieHeader(r.Header.Get("Cookie"))[websession.CookieName]
	if sess := s.sessions4Web.Get(token); sess != nil {
		log.Printf("[auth] logout — user=%q", sess.Username)
		s.loginRecorder(r, sess.Username).Record(audit.Event{
			Action: "auth.logout", TargetType: "user",
			TargetID: sess.UserID, TargetName: sess.Username,
		})
	}
	if token != "" {
		s.sessions4Web.Delete(token)
	}
	w.Header().Set("Set-Cookie", websession.ClearCookieHeader(s.forceHTTPS))
	writeJSON(w, map[string]any{"ok": true})
}

// loginRecorder records an authentication event as `audit.forLogin` does: the
// CLAIMED USERNAME as the actor name, and NO actor id.
//
// ── WHY NOT httpRecorder(r, nil) ────────────────────────────────────────────
//
// It looks equivalent and is not. With no session, `httpRecorder` passes an
// empty name to `audit.ForUser`, which substitutes **"local"** — the fallback
// `fromSocket` uses for an unauthenticated socket. Every login through this
// process therefore appeared in the audit trail as `local` where Node records
// the account name, and the actor column is exactly what an operator filters by.
//
// FOUND BY READING THE REAL TABLE, not by a test: 14 rows saying `local` all
// dated from the hour this port's login started serving, beside 171 from Node
// naming the account. Nothing errors, and a round trip through one
// implementation agrees with itself — the same shape as the `user_layouts`
// keying bug found in the tick before this one, and the same only-visible-in-
// the-data symptom.
//
// The id stays NULL deliberately, which is `forLogin`'s whole point: "a failed
// login may name a user that does not exist, and that is worth seeing".
func (s *Server) loginRecorder(r *http.Request, username string) *audit.Recorder {
	var sink audit.Sink
	if s.auditDB != nil {
		sink = auditSink{s.auditDB}
	}
	return audit.New(sink, audit.ForLogin(username, clientIPOf(r)), nowMillis)
}

// logSafe strips anything that could forge a log line out of a name the caller
// chose. The live `_logSafe` does the same; a username is attacker-supplied on
// a FAILED login by definition.
func logSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r < 0x20 {
			return -1
		}
		return r
	}, s)
	if len(s) > 64 {
		return s[:64] + "…"
	}
	return s
}

// authStatus is `GET /api/auth/status` when Go is the authority.
//
// The SHAPE IS NODE'S, exactly: the login page reads `authMode` and `firstRun`
// before it will show a form, and the SPA reads `session` for its first paint.
// A port that answered a tidier object would leave the login page blank with
// nothing in the console to explain it.
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.Users()
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	out := map[string]any{
		// `firstRun` is "there are no users yet", which is what puts the setup
		// wizard on screen instead of the login form.
		"firstRun": len(users) == 0,
		"authMode": "modern",
		"session":  nil,
	}
	token := websession.ParseCookieHeader(r.Header.Get("Cookie"))[websession.CookieName]
	if sess := s.sessions4Web.Get(token); sess != nil {
		caps, cerr := s.rbac.CapabilitiesFor(sess.UserID)
		if cerr != nil {
			// The capabilities failing is NOT the same as not being signed in.
			// Answering "no session" would bounce a signed-in operator to the
			// login page over a database blip; answering with empty
			// capabilities shows an app with no pages, which is visibly wrong
			// in the right direction and leaves the session intact.
			log.Printf("[auth] capabilities for %q: %v", sess.Username, cerr)
		}
		// THE WHOLE CAPS OBJECT, not a hand-built subset. It used to send
		// `pages`, `routers` and `readable` alone — and `web/src/caps.ts` reads
		// `managePrincipals`, `manageSettings` and `createRouters` directly, so
		// an ABSENT key is `undefined`, which is falsy. An administrator got the
		// Add Router button hidden and Save Settings disabled with
		// "Administrator access required". Invisible during coexistence, because
		// Node answers this route there.
		out["session"] = map[string]any{
			"username": sess.Username,
			"role":     sess.Role,
			"caps":     caps,
		}
	}
	writeJSON(w, out)
}

// localSession is the standalone resolver handed to Auth.SetLocal.
func (s *Server) localSession(token string) (*Session, bool) {
	sess := s.sessions4Web.Get(token)
	if sess == nil {
		return nil, false
	}
	caps, err := s.rbac.CapabilitiesFor(sess.UserID)
	if err != nil {
		// FAIL CLOSED on the capability half while keeping the identity. See
		// authStatus: an empty page map shows an app with no pages rather than
		// silently opening every one of them.
		log.Printf("[auth] capabilities for %q: %v", sess.Username, err)
	}
	return &Session{
		Username: sess.Username,
		Role:     sess.Role,
		AuthMode: "modern",
		Pages:    caps.Pages,
		Readable: caps.Readable,
	}, true
}

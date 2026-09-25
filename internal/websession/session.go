// Package websession is the browser session store — the port of
// `src/auth/sessionStore.js`.
//
// ── WHY THIS EXISTS, AND WHY IT DID NOT UNTIL NOW ───────────────────────────
//
// `internal/server/auth.go` delegates authentication to Node, deliberately:
// sessionStore.js keeps sessions in a process-local Map, there is no shared
// store to read, and Go cannot mint a `mikrodash_sid` that Node would honour.
// For as long as both halves run, one source of truth for sessions is the only
// arrangement that works.
//
// It is also fatal at cutover. With Node stopped, `POST /api/auth/login` — a
// proxied route with no Go handler — stops answering, and NOBODY CAN LOG IN.
// That was found on 2026-08-27 by standing this server against the live /data
// for the first time; it is not a defect in either app, it is a step nobody had
// written down. auth.go states the constraint that leads here and stops short of
// the consequence.
//
// ── IN MEMORY, AND THAT IS THE PORTED BEHAVIOUR RATHER THAN A SHORTCUT ──────
//
// The original's comment: "Sessions are intentionally lost on container restart
// — the login page will re-prompt." Persisting them here would be a
// user-visible change (a restart would no longer sign everyone out) dressed up
// as an improvement, and this port does not make those.
package websession

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CookieName is `mikrodash_sid`, and it must stay exactly that: during
// coexistence Node reads the same cookie, and after cutover every browser
// already holding one keeps its session rather than being signed out by a
// rename.
const CookieName = "mikrodash_sid"

// NeverExpires is the port of the original's `Infinity`.
//
// A TIMEOUT OF 0 MEANS NEVER, NOT "ALREADY EXPIRED", and neither does a
// negative one — the live expression is `(timeoutMs && timeoutMs > 0) ? ... :
// Infinity`. A port reading 0 as an immediate expiry signs every user out the
// instant they sign in, which is the single worst way this could be wrong.
var NeverExpires = time.UnixMilli(math.MaxInt64 / int64(time.Millisecond))

// Session is one signed-in browser.
type Session struct {
	Token            string
	UserID           string
	Username         string
	Role             string
	CreatedAt        time.Time
	ExpiresAt        time.Time
	AllowedRouterIDs []string
	ActiveRouterID   string
}

// Store holds the live sessions.
//
// GUARDED BY A MUTEX, where the original needed none: Node's event loop gives
// sessionStore.js single-threaded access to its Map for free, and Go serves
// requests concurrently. Nothing about the behaviour changes; leaving it out
// would be a data race on every login.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
	now      func() time.Time
}

// New returns an empty store.
func New() *Store { return &Store{sessions: map[string]*Session{}, now: time.Now} }

// Create mints a session. `timeout` of zero or less never expires.
func (s *Store) Create(userID, username, role string, timeout time.Duration,
	allowedRouterIDs []string) (*Session, error) {
	// 32 BYTES, matching `crypto.randomBytes(32).toString('hex')` — 64 hex
	// characters. The length is part of the contract with every browser already
	// holding a cookie, and it is the whole security of the token.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// AN ERROR, NEVER A FALLBACK. A weaker token is worse than no login:
		// the caller can answer 500 and the operator retries, where a
		// predictable token is a silent authentication bypass.
		return nil, err
	}
	now := s.now()
	sess := &Session{
		Token:     hex.EncodeToString(raw),
		UserID:    userID,
		Username:  username,
		Role:      role,
		CreatedAt: now,
		ExpiresAt: NeverExpires,
		// `Array.isArray(x) ? x : []` — never nil, so a caller ranging over it
		// does not have to check.
		AllowedRouterIDs: append([]string{}, allowedRouterIDs...),
	}
	if timeout > 0 {
		sess.ExpiresAt = now.Add(timeout)
	}
	s.mu.Lock()
	s.sessions[sess.Token] = sess
	s.mu.Unlock()
	return sess, nil
}

// expired is `session.expiresAt !== Infinity && _now() > session.expiresAt`.
//
// STRICTLY AFTER. A session whose expiry is exactly now is still live, which is
// why the one-millisecond case in the corpus is live immediately.
func (s *Store) expired(sess *Session) bool {
	return !sess.ExpiresAt.Equal(NeverExpires) && s.now().After(sess.ExpiresAt)
}

// Get returns a session, or nil if it is unknown or expired. An expired one is
// removed on access, as the original does.
func (s *Store) Get(token string) *Session {
	if token == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[token]
	if !ok {
		return nil
	}
	if s.expired(sess) {
		delete(s.sessions, token)
		return nil
	}
	return sess
}

// Delete removes a session (logout).
func (s *Store) Delete(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// SetActiveRouter is `updateSession(token, { activeRouterId })`. It does nothing
// for a token that is unknown or expired.
func (s *Store) SetActiveRouter(token, routerID string) {
	if sess := s.Get(token); sess != nil {
		s.mu.Lock()
		sess.ActiveRouterID = routerID
		s.mu.Unlock()
	}
}

// PruneExpired removes every expired session.
func (s *Store) PruneExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for token, sess := range s.sessions {
		if s.expired(sess) {
			delete(s.sessions, token)
			n++
		}
	}
	return n
}

// ForUser is `listSessionsForUser` — every live session belonging to one user,
// so somebody can see where they are signed in.
//
// A FLAT SCAN, like the original's, and its comment says why: a userID→tokens
// index would have to be kept correct on every create, delete and prune, and
// this map holds one entry per signed-in browser on a self-hosted dashboard.
//
// THE TOKEN IS INCLUDED because callers need it to identify the current
// session, and it must be projected away before any of this reaches a browser.
func (s *Store) ForUser(userID string) []*Session {
	out := []*Session{}
	if userID == "" {
		return out
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.UserID == userID && !s.expired(sess) {
			out = append(out, sess)
		}
	}
	return out
}

// DeleteForUser signs a user out everywhere except `exceptToken`, and returns
// the tokens it removed.
//
// NOTE IT DOES NOT SKIP EXPIRED ONES, matching the original: an expired session
// is being deleted either way, and counting it costs nothing.
func (s *Store) DeleteForUser(userID, exceptToken string) []string {
	removed := []string{}
	if userID == "" {
		return removed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.sessions {
		if sess.UserID == userID && token != exceptToken {
			delete(s.sessions, token)
			removed = append(removed, token)
		}
	}
	return removed
}

// Count is `getSessionCount`.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

// ParseCookieHeader is the port of `parseCookieHeader`.
//
// ── IT SPLITS ON THE FIRST `=` ONLY ─────────────────────────────────────────
//
// `strings.SplitN(part, "=", 2)`, never `strings.Split`. A base64 value ends in
// padding, and a two-way split truncates the token at the first `=` of it — the
// session then never matches and the browser is signed out with no error
// anywhere. Pinned by `a value containing equals signs`.
//
// A pair with no `=` is SKIPPED rather than stored with an empty value, and so
// is one whose name is empty. Both are in the corpus.
func ParseCookieHeader(header string) map[string]string {
	out := map[string]string{}
	if header == "" {
		return out
	}
	for _, part := range strings.Split(header, ";") {
		eq := strings.Index(part, "=")
		if eq == -1 {
			continue
		}
		name := strings.TrimSpace(part[:eq])
		value := strings.TrimSpace(part[eq+1:])
		if name == "" {
			continue
		}
		// LAST ONE WINS, which is what a plain assignment gives and what the
		// original's does. Pinned, because "first wins" is equally defensible
		// and would disagree with Node on a browser sending two.
		out[name] = value
	}
	return out
}

// BuildCookieHeader is the port of `buildCookieHeader`.
//
// `forceHTTPS` is the live `process.env.FORCE_HTTPS === 'true'`. It is a
// PARAMETER rather than a read of the environment, so the caller decides once
// and this stays testable in both states — an install behind TLS and one without
// get different cookies, and only one of them was ever going to be tested by
// accident.
func (s *Store) BuildCookieHeader(token string, expiresAt time.Time, forceHTTPS bool) string {
	var b strings.Builder
	b.WriteString(CookieName + "=" + token + "; HttpOnly; SameSite=Strict; Path=/")
	if !expiresAt.Equal(NeverExpires) {
		// `Math.max(1, Math.round((expiresAt - now) / 1000))`. THE CLAMP IS
		// LOAD-BEARING: an expiry already in the past yields 1, never 0 and
		// never a negative — some browsers treat a negative Max-Age as a
		// session cookie rather than a dead one, which is the opposite of what
		// an expired session should produce.
		secs := int64(math.Round(float64(expiresAt.Sub(s.now()).Milliseconds()) / 1000))
		if secs < 1 {
			secs = 1
		}
		b.WriteString("; Max-Age=" + strconv.FormatInt(secs, 10))
	}
	if forceHTTPS {
		b.WriteString("; Secure")
	}
	return b.String()
}

// ClearCookieHeader is the port of `clearCookieHeader`, used on logout.
func ClearCookieHeader(forceHTTPS bool) string {
	out := CookieName + "=; HttpOnly; SameSite=Strict; Path=/; Max-Age=0"
	if forceHTTPS {
		out += "; Secure"
	}
	return out
}

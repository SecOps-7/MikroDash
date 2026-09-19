package server

// Who the browser is.
//
// THIS PROCESS IS THE AUTHORITY. It was not always: while the Node app ran
// beside it, Node's in-memory session store was the only one, and this file
// asked Node's `/api/auth/status` for every cookie. That coexistence mode was
// retired on 2026-09-19; sessions are this process's own (auth_login.go,
// `localSession`), and `Auth` resolves a cookie against them and nothing else.
//
// ── THE PAGE GATE IS STILL TWO ANSWERS ANDED ────────────────────────────────
//
// `Session.Pages` is the UNION of a principal's page access across every router
// they may read, which is what the first paint needs. The per-router answer
// comes from internal/rbac and the grant graph; `(*conn).canPage` ANDs the two,
// so the resolver can only make the answer stricter. Where the database cannot
// be opened the union stands alone for a READ, and a WRITE fails closed.

import (
	"errors"
	"strings"
)

// Session is the browser's identity: who, in which auth mode, and what they may see.
type Session struct {
	Username string
	Role     string
	// AuthMode is "modern" or "none". In 'none' mode there is no identity and
	// every request is implicitly admin — rbac.js's `if (!_isModern()) return true`
	// is the ONLY copy of that short circuit there, and this is the only copy
	// here, for the same reason: three places to forget it independently is how
	// it got centralised in the first place.
	AuthMode string
	// Pages maps a page key to "read" or "write", unioned across readable
	// routers. See the file header.
	Pages map[string]string
	// Readable is the router ids this principal may read.
	Readable []string
}

// CanReadRouter reports whether this session may watch a router at all. It is
// the coarse gate every finer one is intersected with.
func (s *Session) CanReadRouter(id string) bool {
	for _, r := range s.Readable {
		if r == id {
			return true
		}
	}
	return false
}

// CanPage answers read or write access for a page on a router, from the UNION.
// It is the coarse half — see (*conn).canPage, which is what call
// sites use, and which intersects this with the per-router answer.
func (s *Session) CanPage(page, access, routerID string) bool {
	if !s.CanReadRouter(routerID) {
		return false
	}
	got, ok := s.Pages[page]
	if !ok {
		return false
	}
	if access == "write" {
		return got == "write"
	}
	return got == "read" || got == "write"
}

// ErrNoSession means the cookie named no live session. It is not a transport
// failure and must not be reported as one: the browser needs to be sent to the
// login page, not told the server is broken.
var ErrNoSession = errors.New("server: no session")

// Auth resolves a Cookie header to a session through the local resolver.
type Auth struct {
	// local answers a token from this process's session store. Nil answers no
	// session for every token, which is what a test that never signs in wants.
	local Local
}

// NewAuth builds the validator. The server installs its resolver with SetLocal
// once it exists, because the resolver closes over the server.
func NewAuth() *Auth { return &Auth{} }

// Token pulls mikrodash_sid out of a Cookie header, matching
// SessionStore.parseCookieHeader: split on the FIRST '=' only, so a value
// containing '=' survives.
func Token(cookieHeader string) string {
	for _, part := range strings.Split(cookieHeader, ";") {
		eq := strings.Index(part, "=")
		if eq < 0 {
			continue
		}
		if strings.TrimSpace(part[:eq]) == "mikrodash_sid" {
			return strings.TrimSpace(part[eq+1:])
		}
	}
	return ""
}

// Local resolves a token from this process's session store.
type Local func(token string) (*Session, bool)

// SetLocal installs the resolver.
func (a *Auth) SetLocal(fn Local) { a.local = fn }

// Validate resolves a Cookie header to a session, or ErrNoSession.
func (a *Auth) Validate(cookieHeader string) (*Session, error) {
	tok := Token(cookieHeader)
	if tok == "" || a.local == nil {
		return nil, ErrNoSession
	}
	if s, ok := a.local(tok); ok {
		return s, nil
	}
	return nil, ErrNoSession
}

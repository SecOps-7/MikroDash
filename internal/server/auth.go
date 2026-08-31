package server

// Who the browser is — answered by Node, not by Go.
//
// THIS IS THE ONE PLACE THE STRANGLER CANNOT BE CLEAN, and the reason is worth
// stating rather than discovering. src/auth/sessionStore.js keeps sessions in a
// process-local Map: "Sessions are intentionally lost on container restart".
// There is no shared store to read, so Go cannot mint a `mikrodash_sid` that
// Node would honour, and every proxied request to an unported page would be
// unauthenticated the moment it tried.
//
// So Node stays the authority and Go asks it. GET /api/auth/status already
// takes the cookie and answers with the username, the role and the resolved
// capabilities — it needed no change to the live repo, which is what makes this
// viable at all. One source of truth for sessions, for as long as both halves
// are running.
//
// THE GAP BELOW IS CLOSED — see internal/rbac and (*conn).canPage. It is kept
// here in full because the SHAPE still matters: `caps.pages` really is a union,
// Session.CanPage below really does gate on it, and that gate is still the first
// half of the answer. What changed is that it is no longer the whole answer.
//
// A KNOWN GAP, RECORDED HERE BECAUSE IT IS A CUTOVER BLOCKER, NOT A DETAIL.
// Node gates a page with Rbac.canPage(session, page, access, routerId) — a
// PER-ROUTER answer. `caps.pages` is the union across every readable router,
// which Node itself describes as "what the first paint needs" while "the
// per-router answer is authoritative and arrives over the socket". Go has no
// access to the grant graph (604 lines of rbac.js plus the database), so it
// gates on the union intersected with the readable-router list.
//
// That is exact for any install where a principal's page access does not vary
// BETWEEN routers — every single-router install, and every multi-router install
// whose grants are global. It OVER-PERMITS in one specific case: a principal
// holding dns:write on router A and dns:read on router B would be offered the
// write controls on B. The write itself is still executed against the router by
// this process, so this must be closed before any page is cut over from Node.
//
// HOW IT WAS CLOSED. internal/rbac reads the grant graph — grants,
// group_members, roles, role_pages — out of the SQLite database Node owns, and
// answers the per-router question exactly as rbac.js's canPage does. The two
// answers are ANDed, never substituted: the union gate is Node's own
// computation and the resolver can only ever make the answer stricter, so a bug
// in the port cannot grant access Node would refuse. Where the database cannot
// be opened, the union gate stands alone and the gap above is live again — which
// is why (*conn).canPage says so out loud rather than silently degrading.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Session is the browser's identity as Node reports it.
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
	// routers. See the gap above.
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

// CanPage answers read or write access for a page on a router, from the UNION
// Node sent. It is the coarse half — see (*conn).canPage, which is what call
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

// Auth validates cookies against the Node process.
type Auth struct {
	nodeURL string
	client  *http.Client
	ttl     time.Duration

	mu    sync.Mutex
	cache map[string]cached

	// local is the standalone resolver; nil while Node is the authority.
	local Local
}

type cached struct {
	session *Session
	until   time.Time
}

// NewAuth builds the validator. ttl bounds how stale a cached answer may be;
// it is deliberately far shorter than Node's own 60-second session sweep, so Go
// never holds a view of a session that Node has already discarded for longer
// than Node itself would.
func NewAuth(nodeURL string, ttl time.Duration) *Auth {
	return &Auth{
		nodeURL: strings.TrimSuffix(nodeURL, "/"),
		client:  &http.Client{Timeout: 5 * time.Second},
		ttl:     ttl,
		cache:   map[string]cached{},
	}
}

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

// Local, when set, resolves a token WITHOUT asking Node.
//
// It is installed only in standalone mode — see auth_login.go. In that mode
// there is no Node to ask, and `ask` would fail on every request; here it is
// never consulted at all, because Local answers first and its answer is
// complete.
//
// NOT A FALLBACK, and the difference matters: a fallback would mean a
// Go-minted session was tried against Node when the local store did not
// recognise it, which during coexistence would let a Go login half-work. Local
// is set or it is not.
type Local func(token string) (*Session, bool)

// SetLocal installs the standalone resolver.
func (a *Auth) SetLocal(fn Local) { a.local = fn }

// Validate resolves a Cookie header to a session, or ErrNoSession.
func (a *Auth) Validate(cookieHeader string) (*Session, error) {
	tok := Token(cookieHeader)
	if tok == "" {
		return nil, ErrNoSession
	}

	// STANDALONE: this process is the authority, so there is nothing to cache
	// against and nothing to ask. The session store is already in memory.
	if a.local != nil {
		if s, ok := a.local(tok); ok {
			return s, nil
		}
		return nil, ErrNoSession
	}

	a.mu.Lock()
	if c, ok := a.cache[tok]; ok && time.Now().Before(c.until) {
		a.mu.Unlock()
		if c.session == nil {
			return nil, ErrNoSession
		}
		return c.session, nil
	}
	a.mu.Unlock()

	s, err := a.ask(cookieHeader)
	// A transport failure is NOT cached. Caching it would turn one blip in the
	// Node process into ttl seconds of everybody being logged out.
	if err != nil && !errors.Is(err, ErrNoSession) {
		return nil, err
	}
	a.mu.Lock()
	a.cache[tok] = cached{session: s, until: time.Now().Add(a.ttl)}
	a.mu.Unlock()
	if s == nil {
		return nil, ErrNoSession
	}
	return s, nil
}

// Forget drops a cached answer, so a logout takes effect at once rather than
// at the end of the TTL.
func (a *Auth) Forget(cookieHeader string) {
	tok := Token(cookieHeader)
	if tok == "" {
		return
	}
	a.mu.Lock()
	delete(a.cache, tok)
	a.mu.Unlock()
}

type statusReply struct {
	AuthMode string `json:"authMode"`
	Session  *struct {
		Username string `json:"username"`
		Role     string `json:"role"`
		Caps     struct {
			Pages   map[string]string `json:"pages"`
			Routers struct {
				Readable []string `json:"readable"`
			} `json:"routers"`
		} `json:"caps"`
	} `json:"session"`
}

func (a *Auth) ask(cookieHeader string) (*Session, error) {
	req, err := http.NewRequest(http.MethodGet, a.nodeURL+"/api/auth/status", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookieHeader)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("server: asking node for the session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server: /api/auth/status answered %d", resp.StatusCode)
	}
	var out statusReply
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("server: decoding the session: %w", err)
	}
	if out.Session == nil {
		return nil, ErrNoSession
	}
	pages := out.Session.Caps.Pages
	if pages == nil {
		pages = map[string]string{}
	}
	return &Session{
		Username: out.Session.Username,
		Role:     out.Session.Role,
		AuthMode: out.AuthMode,
		Pages:    pages,
		Readable: out.Session.Caps.Routers.Readable,
	}, nil
}

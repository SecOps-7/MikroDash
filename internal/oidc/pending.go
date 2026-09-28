package oidc

// The logins that have left for the identity provider and not come back.
//
// ── IN MEMORY, FOR THE REASON websession IS ─────────────────────────────────
//
// Sessions are already lost on restart, deliberately. A restart mid-login is a
// re-click, not a lockout, so putting these in SQLite would buy a table, a
// migration, a retention rule and a sweep for a record whose whole life is ten
// minutes. One mechanism per job.
//
// THE COST, WRITTEN DOWN BECAUSE SOMEBODY ELSE WILL MEET IT: the callback must
// reach the same process that served the start. MikroDash is one container with
// an in-memory session store, so this changes nothing today - but two replicas
// behind a load balancer would make sign-in work roughly one time in two, and
// that symptom is close to unfindable unless it is written here.
//
// ── MODELLED ON websession.Store, NOT ADDED TO IT ──────────────────────────
//
// `internal/websession` is a port of a live module and its value is that it
// still agrees with it. Bolting a second, unported map onto a ported file is how
// a port stops being one. Same shape - a mutex, an injectable clock, create and
// consume - different object, different lifetime.

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

const (
	// pendingTTL bounds how long a login may be in flight.
	//
	// Five minutes is uncomfortably short for a hardware key or a push
	// approval; an hour holds a code verifier far longer than it is needed.
	// Ten is the usual value and it bounds the window in which a leaked
	// authorization request is worth anything.
	pendingTTL = 10 * time.Minute

	// maxPending caps the map. The start endpoint is UNAUTHENTICATED: without a
	// cap, anyone who can reach the login page can grow this without limit. The
	// same reasoning, and the same fix, as the rate limiter's own sweep.
	maxPending = 256

	// tokenBytes is how much entropy each of state, nonce, verifier and the
	// pending id carries. 32 bytes is what websession uses for a session token,
	// and these are no less worth guessing.
	tokenBytes = 32
)

// ErrNoEntropy is returned when the system random source fails.
//
// A FAILURE HERE IS FATAL TO THE LOGIN, NEVER A FALLBACK. websession.Create
// makes the same call and states the rule: a weaker token is worse than no
// login. There is no seeded-PRNG path and there must not be one.
var ErrNoEntropy = errors.New("oidc: no entropy for a login token")

// login is one authentication request in flight.
type login struct {
	// ProviderID is carried so the callback knows which provider to verify
	// against WITHOUT reading it from the query string. Taking it from the URL
	// would let a caller pair one provider's code with another's configuration.
	ProviderID string
	State      string
	Nonce      string
	Verifier   string
	// Next is where to go afterwards, already validated by safeNext at the
	// start. It rides here rather than through the provider, so nothing that
	// round-trips through a third party can influence it.
	Next string
	at   time.Time
}

// Challenge is the PKCE code challenge for this login.
//
// S256 ONLY. `plain` makes the challenge equal the verifier, so anyone who saw
// the authorization request can complete the exchange - the entire mechanism
// defeated. There is no runtime downgrade: this is the only challenge this
// package can produce.
func (l *login) Challenge() string {
	sum := sha256.Sum256([]byte(l.Verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Pending holds them.
type Pending struct {
	mu   sync.Mutex
	byID map[string]*login
	now  func() time.Time
}

// NewPending builds the store. `now` is injectable so the tests need no sleeps.
func NewPending(now func() time.Time) *Pending {
	if now == nil {
		now = time.Now
	}
	return &Pending{byID: map[string]*login{}, now: now}
}

// Create mints a login and returns its opaque id.
//
// The id goes in a cookie and the state goes through the provider. They are
// DIFFERENT VALUES on purpose - see Consume.
func (p *Pending) Create(providerID, next string) (string, *login, error) {
	id, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	state, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	nonce, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	verifier, err := randomToken()
	if err != nil {
		return "", nil, err
	}
	rec := &login{
		ProviderID: providerID, State: state, Nonce: nonce,
		Verifier: verifier,
		// ── THE next GUARD RUNS HERE, NOT IN THE HANDLER ────────────────────
		//
		// `next` arrives from a query string on an unauthenticated route and
		// ends up in a Location header. Validating it at the one point every
		// login must pass through means a handler cannot forget it, which is
		// the only version of this check that stays true. It is why safeNext is
		// unexported: there is nothing for a caller to remember.
		Next: safeNext(next),
		at:   p.now(),
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	// STILL FULL AFTER A PRUNE means live entries, so the oldest gives way.
	// Refusing the new login instead would let anyone who can fill the map lock
	// everybody else out, which is a denial of service with extra steps.
	for len(p.byID) >= maxPending {
		p.dropOldestLocked()
	}
	p.byID[id] = rec
	return id, rec, nil
}

// Consume takes a pending login, and takes it whether or not the caller turns
// out to be entitled to it.
//
// ── THE RECORD IS DELETED BEFORE THE STATE IS CHECKED ──────────────────────
//
// Deleting only on the success path would leave a record alive for somebody
// holding a valid id to keep guessing the state against. One lookup, one chance.
// A replay of a callback that worked is therefore indistinguishable from one
// that never did, which is also what makes the audit line honest.
//
// The state is compared in constant time: it is a secret this process generated,
// and length-leaking equality on a secret is a habit worth not having even where
// the timing channel is implausible over a network.
func (p *Pending) Consume(id, state string) (*login, bool) {
	if id == "" || state == "" {
		return nil, false
	}
	p.mu.Lock()
	rec, ok := p.byID[id]
	delete(p.byID, id)
	p.mu.Unlock()

	if !ok {
		return nil, false
	}
	if p.now().Sub(rec.at) > pendingTTL {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(rec.State), []byte(state)) != 1 {
		return nil, false
	}
	return rec, true
}

// PruneExpired drops what has timed out. Called from whatever ticker already
// prunes sessions; Create prunes too, so an idle process does not hold the last
// few for ever.
func (p *Pending) PruneExpired() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
}

// Len is for the tests and for a diagnostic line. It is not a decision input.
func (p *Pending) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.byID)
}

func (p *Pending) pruneLocked() {
	cutoff := p.now().Add(-pendingTTL)
	for id, rec := range p.byID {
		if rec.at.Before(cutoff) {
			delete(p.byID, id)
		}
	}
}

func (p *Pending) dropOldestLocked() {
	var oldestID string
	var oldest time.Time
	for id, rec := range p.byID {
		if oldestID == "" || rec.at.Before(oldest) {
			oldestID, oldest = id, rec.at
		}
	}
	if oldestID != "" {
		delete(p.byID, oldestID)
	}
}

// randomToken is 32 bytes of crypto/rand as base64url.
func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", ErrNoEntropy
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

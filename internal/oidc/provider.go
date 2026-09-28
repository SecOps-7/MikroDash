package oidc

// Talking to the identity provider: its metadata, and its signing keys.
//
// ── THE THIRD OUTBOUND DESTINATION IN THIS SERVER ──────────────────────────
//
// `internal/notify` talks to channels the operator configured; `internal/
// changelog` talks to MikroTik and fails soft. This one talks to the identity
// provider the operator configured and MUST NOT fail soft: a fetch that cannot
// be completed is a login that does not happen, never a login that happens on
// weaker terms.
//
// ── REDIRECTS ARE REFUSED ───────────────────────────────────────────────────
//
// `internal/aiprovider` states the rule for a request carrying a credential:
// "Go attaches the request's headers to a redirected request, so following one
// would hand the API key to whatever host the response named." That applies
// directly and lethally to the token endpoint, where a 307 or 308 REPLAYS THE
// POST BODY - the client secret and the code verifier - to whatever host the
// provider named.
//
// For discovery and the key set the argument is different but not weaker. Those
// carry no secret, so nothing leaks; what a redirect moves is the decision about
// WHICH KEYS WE TRUST. The issuer-equality check below limits it, but `jwks_uri`
// is then an arbitrary URL from a document fetched from an arbitrary host.
// Refusing keeps the set of hosts we accept metadata from equal to the set the
// operator typed.
//
// The refusal carries its target, so the settings page can say "that issuer
// redirected to X; enter that instead" - a setup step rather than a wall.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// requestTimeout matches internal/notify's. One number for outbound HTTP in
	// this process is one fewer to reconcile.
	requestTimeout = 10 * time.Second

	// bodyLimit caps a metadata response. internal/changelog uses the same
	// figure for the same reason: a remote system should not decide how much
	// memory this one spends.
	bodyLimit = 256 << 10

	// discoTTL and jwksTTL. Endpoints change on the order of years and keys
	// rotate daily-to-monthly, so an hour bounds a mis-cached document without
	// costing a round trip per login. The unknown-kid path below covers a
	// rotation inside the window.
	discoTTL = time.Hour
	jwksTTL  = time.Hour

	// negativeTTL is straight from changelog's: an isolated install would
	// otherwise pay the full timeout on every attempt. Ten people clicking Sign
	// In against a dead provider should cost one timeout, not ten.
	negativeTTL = 60 * time.Second

	// kidRefetchEvery throttles the refetch an unknown `kid` triggers.
	//
	// THE CALLBACK IS UNAUTHENTICATED AND THE KID COMES OUT OF AN
	// ATTACKER-SUPPLIED TOKEN. Without this, MikroDash is a free request
	// amplifier pointed at the operator's identity provider - and a provider
	// that starts refusing us breaks every genuine login. The cost is that a
	// rotation is picked up within five minutes rather than instantly, during
	// which some logins fail and are retried.
	kidRefetchEvery = 5 * time.Minute

	// staleCeiling is how long a key set may be served after refreshes start
	// failing.
	//
	// Serving a stale set is right: refusing every sign-in because the key
	// endpoint returned a 502 is a self-inflicted outage. Serving one FOR EVER
	// is not: a permanently dead provider would leave revoked keys trusted for
	// the life of the process.
	staleCeiling = 24 * time.Hour

	// maxKeys bounds a key set. A document with ten thousand keys is a memory
	// and CPU problem reachable without authenticating.
	maxKeys = 32
)

// RedirectError is returned when the provider redirects a metadata or token
// request. It names the target so the operator can be told what to enter.
type RedirectError struct{ To string }

func (e *RedirectError) Error() string {
	return "the identity provider redirected to " + e.To + "; MikroDash follows no redirects " +
		"when fetching provider metadata"
}

// discovery is the part of the provider's metadata this app uses. Everything
// else in the document is ignored: no dynamic registration, no userinfo, no
// logout, no introspection.
type discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ChallengeMethods      []string `json:"code_challenge_methods_supported"`
	SigningAlgs           []string `json:"id_token_signing_alg_values_supported"`
	AuthMethods           []string `json:"token_endpoint_auth_methods_supported"`
}

// Provider is one configured identity provider and what this process has
// learned about it. One per provider, for the life of the process.
type Provider struct {
	cfg Config

	// HTTP and Now are the whole test seam, lifted from changelog.Client.
	// Nothing in this package calls time.Now or http.DefaultClient directly.
	HTTP *http.Client
	Now  func() time.Time

	mu         sync.Mutex
	disco      *discovery
	discoAt    time.Time
	discoErrAt time.Time
	keys       *keySet
	keysAt     time.Time
	keysErrAt  time.Time
	lastKidAt  time.Time
}

// NewProvider builds one. Config is validated when the operator saves it, not
// here - somebody who typed an unusable issuer should learn while the form is
// open, not at three in the morning through another person's failed login.
func NewProvider(cfg Config) *Provider {
	return &Provider{cfg: cfg, Now: time.Now}
}

func (p *Provider) now() time.Time {
	if p.Now == nil {
		return time.Now()
	}
	return p.Now()
}

func (p *Provider) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return defaultClient
}

var defaultClient = &http.Client{
	Timeout: requestTimeout,
	CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		return &RedirectError{To: req.URL.String()}
	},
}

// Discovery returns the provider's metadata, fetching it if the copy is stale.
//
// ── THE LOCK IS HELD ACROSS THE FETCH, DELIBERATELY ────────────────────────
//
// Ten simultaneous logins therefore queue behind one round trip rather than
// making ten. The cost is that a slow provider blocks other sign-ins for up to
// the timeout; the negative cache means that happens at most once a minute, and
// a login is not a hot path.
//
// The alternative - a single-flight channel, releasing the lock across the
// fetch - is about fifteen more lines and a second place where the result is
// published. It is the right change if this ever sits in front of something
// busier, and it is not needed for a login page.
func (p *Provider) Discovery(ctx context.Context) (*discovery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.disco != nil && now.Sub(p.discoAt) < discoTTL {
		return p.disco, nil
	}
	// A recent failure is not retried until the negative TTL has passed - but a
	// usable copy is still served while that runs down.
	if !p.discoErrAt.IsZero() && now.Sub(p.discoErrAt) < negativeTTL {
		if p.disco != nil {
			return p.disco, nil
		}
		return nil, Refuse(CodeProvider, "the identity provider could not be reached")
	}

	var doc discovery
	u := strings.TrimSuffix(p.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	if err := p.getJSON(ctx, u, &doc); err != nil {
		p.discoErrAt = now
		if p.disco != nil {
			return p.disco, nil
		}
		return nil, err
	}
	// ── THE DOCUMENT'S OWN ISSUER MUST BE THE CONFIGURED ONE ───────────────
	//
	// Exact string equality: not normalised, not case-folded, not
	// trailing-slash-tolerant. Without it, a document served from host A that
	// declares itself issuer B makes every later `iss` check compare B with B
	// and pass, while the signing keys came from A. It is also the multi-tenant
	// case: two tenants of one provider differ only in this string.
	if doc.Issuer != p.cfg.Issuer {
		p.discoErrAt = now
		return nil, Refuse(CodeConfig, "the provider's metadata declares a different issuer")
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		p.discoErrAt = now
		return nil, Refuse(CodeConfig, "the provider's metadata is missing an endpoint")
	}
	// The endpoints are NOT required to share the issuer's host. Google's issuer
	// is accounts.google.com and its keys are on www.googleapis.com; Entra's
	// issuer is sts.windows.net and its endpoints are on login.microsoftonline.
	// A same-host rule would break the two commonest providers, and a design
	// that hardens itself out of the field is not hardened.
	for _, ep := range []string{doc.AuthorizationEndpoint, doc.TokenEndpoint, doc.JWKSURI} {
		if err := httpsURL(ep); err != nil {
			p.discoErrAt = now
			return nil, Refuse(CodeConfig, err.Error())
		}
	}
	p.disco, p.discoAt = &doc, now
	p.discoErrAt = time.Time{}
	return p.disco, nil
}

// Keys returns the provider's signing keys.
//
// `stale` asks for a refetch even when the cached copy is inside its TTL, which
// is what a token naming an unknown `kid` means. It is throttled - see
// kidRefetchEvery.
func (p *Provider) Keys(ctx context.Context, stale bool) (*keySet, error) {
	doc, err := p.Discovery(ctx)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	fresh := p.keys != nil && now.Sub(p.keysAt) < jwksTTL

	if stale {
		// The refetch an unknown kid asks for, at most once per window.
		if !p.lastKidAt.IsZero() && now.Sub(p.lastKidAt) < kidRefetchEvery {
			if p.keys != nil {
				return p.keys, nil
			}
			return nil, Refuse(CodeProvider, "the provider's signing keys are not available")
		}
		p.lastKidAt = now
	} else if fresh {
		return p.keys, nil
	}

	if !p.keysErrAt.IsZero() && now.Sub(p.keysErrAt) < negativeTTL && p.keys != nil {
		return p.keys, nil
	}

	var set keySet
	if err := p.getJSON(ctx, doc.JWKSURI, &set); err != nil {
		p.keysErrAt = now
		// ── A STALE SET IS SERVED, UP TO A CEILING ─────────────────────────
		//
		// Refusing every sign-in because the key endpoint returned a 502 would
		// be a self-inflicted outage; keys rotate slowly. Serving one for ever
		// would leave a revoked key trusted for the life of the process, which
		// is the sentence nobody wants to write afterwards.
		if p.keys != nil && now.Sub(p.keysAt) < staleCeiling {
			return p.keys, nil
		}
		return nil, err
	}
	if len(set.Keys) == 0 {
		p.keysErrAt = now
		return nil, Refuse(CodeProvider, "the provider published no signing keys")
	}
	if len(set.Keys) > maxKeys {
		p.keysErrAt = now
		return nil, Refuse(CodeProvider, fmt.Sprintf("the provider published %d keys", len(set.Keys)))
	}
	p.keys, p.keysAt = &set, now
	p.keysErrAt = time.Time{}
	return p.keys, nil
}

// AuthorizeURL is where the browser is sent to sign in.
func (p *Provider) AuthorizeURL(ctx context.Context, l *login, scopes string) (string, error) {
	doc, err := p.Discovery(ctx)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(doc.AuthorizationEndpoint)
	if err != nil {
		return "", Refuse(CodeConfig, "the provider's authorization endpoint is not a URL")
	}
	if strings.TrimSpace(scopes) == "" {
		scopes = "openid profile email"
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", p.cfg.ClientID)
	q.Set("redirect_uri", p.cfg.RedirectURI)
	q.Set("scope", scopes)
	q.Set("state", l.State)
	q.Set("nonce", l.Nonce)
	q.Set("code_challenge", l.Challenge())
	// S256 ONLY, and never read from the metadata. An attacker who could
	// influence the document would otherwise be choosing the method, and `plain`
	// makes the challenge equal the verifier.
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// getJSON fetches and decodes, with every bound this package places on a remote
// system in one place.
func (p *Provider) getJSON(ctx context.Context, rawURL string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Refuse(CodeConfig, "the metadata URL is not usable")
	}
	req.Header.Set("Accept", "application/json")
	res, err := p.client().Do(req)
	if err != nil {
		// A redirect refusal arrives wrapped in *url.Error, so it is unwrapped
		// rather than type-asserted. Easy to get wrong, and the operator-facing
		// message depends on it.
		var redir *RedirectError
		if errors.As(err, &redir) {
			return Refuse(CodeConfig, redir.Error())
		}
		return Refuse(CodeProvider, "the identity provider could not be reached")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Refuse(CodeProvider, fmt.Sprintf("the identity provider answered %d", res.StatusCode))
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return Refuse(CodeProvider, fmt.Sprintf("the identity provider answered %q, not JSON", ct))
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit+1))
	if err != nil {
		return Refuse(CodeProvider, "the identity provider's answer could not be read")
	}
	if len(body) > bodyLimit {
		return Refuse(CodeProvider, "the identity provider's answer is too large")
	}
	if err := json.Unmarshal(body, into); err != nil {
		return Refuse(CodeProvider, "the identity provider's answer is not JSON")
	}
	return nil
}

// httpsURL is the one shape check applied to an endpoint out of the metadata.
func httpsURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("the provider named %q, which is not an https URL", raw)
	}
	return nil
}

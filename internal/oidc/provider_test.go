package oidc

// The fetch layer, against a fake identity provider.
//
// Everything here runs on httptest, so the suite needs no network and no
// provider account. The fake's OPTIONS ARE THE MUTATION SET: each is a way a
// real or hostile provider can answer, and each has a named test.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeIdP serves the documents a provider serves.
type fakeIdP struct {
	srv *httptest.Server
	// issuer is what the discovery document DECLARES, which is not always the
	// URL it was fetched from - that is the attack the issuer check stops.
	issuer      string
	jwks        *keySet
	discoHits   int
	jwksHits    int
	jwksStatus  int
	discoStatus int
	// contentType, when set, replaces application/json.
	contentType string
	// redirectTo, when set, makes discovery a 302.
	redirectTo string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{jwks: rsaJWKS()}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		f.discoHits++
		if f.redirectTo != "" {
			http.Redirect(w, r, f.redirectTo, http.StatusFound)
			return
		}
		if f.discoStatus != 0 {
			w.WriteHeader(f.discoStatus)
			return
		}
		issuer := f.issuer
		if issuer == "" {
			issuer = f.srv.URL
		}
		f.writeJSON(w, map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                "https://idp.example/authorize",
			"token_endpoint":                        "https://idp.example/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"code_challenge_methods_supported":      []string{"S256"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		f.jwksHits++
		if f.jwksStatus != 0 {
			w.WriteHeader(f.jwksStatus)
			return
		}
		f.writeJSON(w, f.jwks)
	})
	// A TLS SERVER, because the production rule is that every endpoint out of
	// the metadata is https. Serving the fake over http and relaxing that rule
	// would be weakening a security check to make a test pass, which is the one
	// thing this repository refuses outright.
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) writeJSON(w http.ResponseWriter, v any) {
	ct := f.contentType
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}

// providerFor builds a Provider pointed at the fake, with a controllable clock.
func providerFor(f *fakeIdP, now *time.Time) *Provider {
	p := NewProvider(Config{
		Issuer: f.srv.URL, ClientID: testClient, ClientSecret: "s",
	})
	p.HTTP = f.srv.Client()
	// The fake's own client follows redirects; refusing them is the rule under
	// test, so it is reapplied here exactly as the production client has it.
	p.HTTP.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return &RedirectError{To: req.URL.String()}
	}
	p.Now = func() time.Time { return *now }
	return p
}

// ── THE BELIEVABILITY FLOOR ─────────────────────────────────────────────────

func TestAProviderIsDiscoveredAndItsKeysFetched(t *testing.T) {
	f := newFakeIdP(t)
	now := testNow
	p := providerFor(f, &now)
	doc, err := p.Discovery(context.Background())
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if doc.TokenEndpoint != "https://idp.example/token" {
		t.Errorf("the token endpoint read as %q", doc.TokenEndpoint)
	}
	keys, err := p.Keys(context.Background(), false)
	if err != nil || len(keys.Keys) != 1 {
		t.Fatalf("keys: %v, %+v", err, keys)
	}
}

// ── THE ISSUER CHECK ────────────────────────────────────────────────────────
//
// A document served from host A that declares itself issuer B makes every later
// `iss` check compare B with B and pass, while the signing keys came from A.
func TestADiscoveryDocumentClaimingAnotherIssuerIsRefused(t *testing.T) {
	f := newFakeIdP(t)
	f.issuer = "https://issuer.example/tenant-b"
	now := testNow
	if _, err := providerFor(f, &now).Discovery(context.Background()); err == nil {
		t.Fatal("a document declaring a different issuer was accepted; its jwks_uri would " +
			"then decide which keys we trust")
	}
}

// AND THE https RULE IS ENFORCED, not merely present. The fake serves TLS so
// every other test exercises the happy path; this one proves the check bites.
func TestAnHTTPEndpointInTheMetadataIsRefused(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer plain.Close()

	f := newFakeIdP(t)
	// Point the key set at an http URL, leaving everything else valid.
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := json.Marshal(map[string]any{
			"issuer":                 f.srv.URL,
			"authorization_endpoint": "https://idp.example/authorize",
			"token_endpoint":         "https://idp.example/token",
			"jwks_uri":               plain.URL,
		})
		_, _ = w.Write(b)
	})
	f.srv.Config.Handler = mux

	now := testNow
	if _, err := providerFor(f, &now).Discovery(context.Background()); err == nil {
		t.Fatal("an http endpoint in the provider's metadata was accepted")
	}
}

// ── REDIRECTS ───────────────────────────────────────────────────────────────

func TestADiscoveryRedirectIsNotFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the redirect target was fetched; metadata must only come from the host " +
			"the operator typed")
	}))
	defer target.Close()

	f := newFakeIdP(t)
	f.redirectTo = target.URL + "/.well-known/openid-configuration"
	now := testNow
	_, err := providerFor(f, &now).Discovery(context.Background())
	if err == nil {
		t.Fatal("a redirected discovery was accepted")
	}
	// THE REFUSAL NAMES THE TARGET, so the settings page can tell the operator
	// what to enter instead. A bare refusal reads as "MikroDash is broken".
	if !strings.Contains(err.Error(), target.URL) {
		t.Errorf("the refusal does not name where it was sent: %v", err)
	}
}

// ── AND THE CLIENT PRODUCTION ACTUALLY USES REFUSES THEM ───────────────────
//
// A mutation sweep found this gap: the test above injects its own client with
// CheckRedirect set, so it proves the RULE works and says nothing about whether
// the shipped default carries it. Deleting CheckRedirect from defaultClient left
// that test green.
//
// So this reaches for the real thing. It is the only test in the package that
// touches defaultClient, and that is the point.
func TestTheDefaultClientRefusesRedirects(t *testing.T) {
	if defaultClient.CheckRedirect == nil {
		t.Fatal("the default client follows redirects; a 307 on the token endpoint would " +
			"replay the client secret and the code verifier to whatever host the provider named")
	}
	req, err := http.NewRequest(http.MethodGet, "https://elsewhere.example/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	err = defaultClient.CheckRedirect(req, nil)
	if err == nil {
		t.Fatal("the default client's CheckRedirect allowed a redirect")
	}
	var redir *RedirectError
	if !errors.As(err, &redir) || !strings.Contains(err.Error(), "elsewhere.example") {
		t.Errorf("the refusal does not name the target (%v); the settings page needs it "+
			"to tell the operator what to enter", err)
	}
	if defaultClient.Timeout != requestTimeout {
		t.Errorf("the default client's timeout is %v, want %v", defaultClient.Timeout, requestTimeout)
	}
}

// ── CACHING ─────────────────────────────────────────────────────────────────

func TestTheMetadataIsCachedAndRefreshedOnItsTTL(t *testing.T) {
	f := newFakeIdP(t)
	now := testNow
	p := providerFor(f, &now)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := p.Discovery(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if f.discoHits != 1 {
		t.Errorf("five discoveries made %d fetches; the cache is not working", f.discoHits)
	}
	now = now.Add(discoTTL + time.Second)
	if _, err := p.Discovery(ctx); err != nil {
		t.Fatal(err)
	}
	if f.discoHits != 2 {
		t.Errorf("the document was not refetched after its TTL (%d fetches)", f.discoHits)
	}
}

// An isolated install should pay one timeout, not one per attempt.
func TestAFailingProviderIsNotRetriedEveryTime(t *testing.T) {
	f := newFakeIdP(t)
	f.discoStatus = http.StatusBadGateway
	now := testNow
	p := providerFor(f, &now)
	for i := 0; i < 5; i++ {
		if _, err := p.Discovery(context.Background()); err == nil {
			t.Fatal("a 502 discovery was accepted")
		}
	}
	if f.discoHits != 1 {
		t.Errorf("five attempts against a dead provider made %d fetches", f.discoHits)
	}
	// THE CONTROL: once the negative TTL passes it tries again, so the cache is
	// a delay and not a permanent refusal.
	now = now.Add(negativeTTL + time.Second)
	if _, err := p.Discovery(context.Background()); err == nil {
		t.Fatal("still expected a failure")
	}
	if f.discoHits != 2 {
		t.Errorf("the provider was not retried after the negative TTL (%d fetches)", f.discoHits)
	}
}

// ── THE UNKNOWN-KID THROTTLE ────────────────────────────────────────────────
//
// The callback is unauthenticated and the kid comes out of an attacker-supplied
// token. An unthrottled refetch makes this a request amplifier pointed at the
// operator's provider.
func TestAStreamOfUnknownKidsCausesOneRefetch(t *testing.T) {
	f := newFakeIdP(t)
	now := testNow
	p := providerFor(f, &now)
	ctx := context.Background()
	if _, err := p.Keys(ctx, false); err != nil {
		t.Fatal(err)
	}
	before := f.jwksHits
	for i := 0; i < 50; i++ {
		if _, err := p.Keys(ctx, true); err != nil {
			t.Fatal(err)
		}
	}
	if got := f.jwksHits - before; got != 1 {
		t.Errorf("fifty unknown kids caused %d refetches; one is the cap per window", got)
	}
	// THE CONTROL: after the window a rotation is picked up.
	now = now.Add(kidRefetchEvery + time.Second)
	if _, err := p.Keys(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := f.jwksHits - before; got != 2 {
		t.Errorf("a rotation after the window was not picked up (%d refetches)", got)
	}
}

// ── STALENESS, BOUNDED BOTH WAYS ────────────────────────────────────────────

func TestAStaleKeySetIsServedButNotForEver(t *testing.T) {
	f := newFakeIdP(t)
	now := testNow
	p := providerFor(f, &now)
	ctx := context.Background()
	if _, err := p.Keys(ctx, false); err != nil {
		t.Fatal(err)
	}
	f.jwksStatus = http.StatusBadGateway

	// Past the TTL with the endpoint failing: the old set is still served,
	// because refusing every sign-in over a 502 is a self-inflicted outage.
	now = now.Add(jwksTTL + time.Minute)
	if _, err := p.Keys(ctx, false); err != nil {
		t.Errorf("a stale key set was refused while the provider was down: %v", err)
	}
	// Past the ceiling: refused, or a dead provider's revoked keys stay trusted
	// for the life of the process.
	now = now.Add(staleCeiling)
	if _, err := p.Keys(ctx, false); err == nil {
		t.Error("a key set stale for more than a day was still used")
	}
}

// ── BOUNDS ON WHAT A PROVIDER MAY SEND ──────────────────────────────────────

func TestAProviderAnsweringSomethingOtherThanJSONIsRefused(t *testing.T) {
	f := newFakeIdP(t)
	f.contentType = "text/html"
	now := testNow
	if _, err := providerFor(f, &now).Discovery(context.Background()); err == nil {
		t.Fatal("an HTML answer was parsed as metadata")
	}
}

func TestAKeySetWithTooManyKeysIsRefused(t *testing.T) {
	f := newFakeIdP(t)
	big := &keySet{}
	for i := 0; i < maxKeys+1; i++ {
		k := rsaJWKS().Keys[0]
		k.Kid = fmt.Sprintf("key-%d", i)
		big.Keys = append(big.Keys, k)
	}
	f.jwks = big
	now := testNow
	if _, err := providerFor(f, &now).Keys(context.Background(), false); err == nil {
		t.Fatalf("a key set of %d keys was accepted", len(big.Keys))
	}
}

// ── THE AUTHORIZATION REQUEST ───────────────────────────────────────────────

func TestTheAuthorizationRequestCarriesS256AndTheFixedRedirectURI(t *testing.T) {
	f := newFakeIdP(t)
	now := testNow
	p := providerFor(f, &now)
	pend := NewPending(func() time.Time { return now })
	_, rec, err := pend.Create("prov-1", "/logs", "https://dash.example/api/auth/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := p.AuthorizeURL(context.Background(), rec, "")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for _, tc := range []struct{ key, wantV, why string }{
		{"response_type", "code", "the implicit flow puts a token in a URL"},
		{"code_challenge_method", "S256", "plain makes the challenge equal the verifier"},
		{"code_challenge", rec.Challenge(), "the challenge binds the exchange to this login"},
		{"state", rec.State, "the state is what the callback is matched against"},
		{"nonce", rec.Nonce, "the nonce binds the token to this request"},
		{"client_id", testClient, ""},
		{"redirect_uri", "https://dash.example/api/auth/sso/callback", "the provider exact-matches this"},
	} {
		if got := q.Get(tc.key); got != tc.wantV {
			t.Errorf("%s = %q, want %q — %s", tc.key, got, tc.wantV, tc.why)
		}
	}
	// THE VERIFIER NEVER LEAVES THIS PROCESS IN THE AUTHORIZATION REQUEST. Only
	// its hash does; sending the verifier would defeat PKCE entirely.
	if strings.Contains(raw, rec.Verifier) {
		t.Error("the code verifier was sent in the authorization request")
	}
	// AND NEITHER DOES `next`. It rides in the pending record, so nothing that
	// round-trips through the provider can influence where we land.
	if strings.Contains(raw, "/logs") {
		t.Error("the next target was sent through the identity provider")
	}
}

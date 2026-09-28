package oidc

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// tokenIdP extends the fake with a token endpoint, recording what was posted.
type tokenIdP struct {
	*fakeIdP
	srv *httptest.Server
	// gotForm and gotAuth are what the last exchange actually sent.
	gotForm url.Values
	gotAuth string
	// status, body and ctype shape the reply; zero values mean a good answer.
	status int
	body   string
	ctype  string
}

func newTokenIdP(t *testing.T) *tokenIdP {
	t.Helper()
	ti := &tokenIdP{fakeIdP: newFakeIdP(t)}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ti.gotForm = r.PostForm
		ti.gotAuth = r.Header.Get("Authorization")
		ct := ti.ctype
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		if ti.status != 0 {
			w.WriteHeader(ti.status)
		}
		body := ti.body
		if body == "" {
			// The provider sends all three. Only one may survive this process.
			body = `{"id_token":"a.b.c","access_token":"SECRET-ACCESS","refresh_token":"SECRET-REFRESH"}`
		}
		_, _ = w.Write([]byte(body))
	})
	ti.srv = httptest.NewTLSServer(mux)
	t.Cleanup(ti.srv.Close)
	return ti
}

// provider wires discovery at the fake and the token endpoint at ours.
func (ti *tokenIdP) provider(t *testing.T, now *time.Time, authMethods []string) *Provider {
	t.Helper()
	p := providerFor(ti.fakeIdP, now)
	if _, err := p.Discovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	p.disco.TokenEndpoint = ti.srv.URL + "/token"
	p.disco.AuthMethods = authMethods
	p.HTTP = ti.srv.Client()
	p.HTTP.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		return &RedirectError{To: req.URL.String()}
	}
	return p
}

// ── THE ACCESS TOKEN IS DISCARDED BY THE TYPE ──────────────────────────────
//
// The provider sends one. Nothing in this process can see it, because the struct
// it decodes into has no field for it.
func TestTheAccessAndRefreshTokensAreDiscarded(t *testing.T) {
	ti := newTokenIdP(t)
	now := testNow
	p := ti.provider(t, &now, []string{"client_secret_basic"})

	idToken, err := p.Exchange(context.Background(), "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("a good exchange failed: %v", err)
	}
	if idToken != "a.b.c" {
		t.Errorf("got id token %q", idToken)
	}
	if strings.Contains(idToken, "SECRET") {
		t.Fatal("an access or refresh token reached the caller")
	}
}

// ── AND THE TYPE IS WHY, WHICH ONLY THE SOURCE CAN SHOW ────────────────────
//
// A mutation sweep found this gap: adding an access_token field to
// tokenResponse failed nothing, because the test above checks the RETURNED id
// token and a new field does not change it. "The struct has no field for it" is
// a claim about the type, so it is checked against the type.
//
// It reads exchange.go and not this file, so the strings it looks for cannot be
// found in the checker itself.
func TestTheTokenResponseStructHasNoTokenFieldsButTheIDToken(t *testing.T) {
	src, err := os.ReadFile("exchange.go")
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(src, []byte("type tokenResponse struct {"))
	if start < 0 {
		t.Fatal("tokenResponse is gone; this check no longer knows what to read")
	}
	end := bytes.Index(src[start:], []byte("}"))
	if end < 0 {
		t.Fatal("tokenResponse has no closing brace")
	}
	body := string(src[start : start+end])

	for _, forbidden := range []string{"access" + "_token", "refresh" + "_token"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("tokenResponse has a %s field. A value with nowhere to be decoded cannot "+
				"be logged, stored or reached for by a later edit; a field can.", forbidden)
		}
	}
	// THE CONTROL: the check can see a field that IS there, so a pass means the
	// forbidden ones are absent rather than that the scan found nothing.
	if !strings.Contains(body, "id"+"_token") {
		t.Error("the scan cannot see the id token field either; it is reading the wrong thing")
	}
}

// ── WHAT IS SENT ────────────────────────────────────────────────────────────

func TestTheExchangeSendsTheVerifierAndTheFixedRedirectURI(t *testing.T) {
	ti := newTokenIdP(t)
	now := testNow
	p := ti.provider(t, &now, []string{"client_secret_basic"})
	if _, err := p.Exchange(context.Background(), "the-code", "the-verifier"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ key, wantV, why string }{
		{"grant_type", "authorization_code", ""},
		{"code", "the-code", ""},
		{"code_verifier", "the-verifier", "without it PKCE proves nothing"},
		{"redirect_uri", p.cfg.RedirectURI, "the provider compares this byte for byte"},
	} {
		if got := ti.gotForm.Get(tc.key); got != tc.wantV {
			t.Errorf("%s = %q, want %q %s", tc.key, got, tc.wantV, tc.why)
		}
	}
	// THE SECRET IS IN THE HEADER, NOT THE BODY, when basic is offered.
	if ti.gotForm.Get("client_secret") != "" {
		t.Error("the client secret was put in the form although basic auth was offered")
	}
	if !strings.HasPrefix(ti.gotAuth, "Basic ") {
		t.Errorf("no basic credential was sent: %q", ti.gotAuth)
	}
}

// A provider offering only client_secret_post gets the secret in the form. Two
// lines, and without them a whole class of provider fails with a 401 that points
// nowhere.
func TestAProviderOfferingOnlyPostGetsTheSecretInTheForm(t *testing.T) {
	ti := newTokenIdP(t)
	now := testNow
	p := ti.provider(t, &now, []string{"client_secret_post"})
	if _, err := p.Exchange(context.Background(), "c", "v"); err != nil {
		t.Fatal(err)
	}
	if ti.gotForm.Get("client_secret") == "" {
		t.Error("a provider that offers only client_secret_post was sent no secret")
	}
	if ti.gotAuth != "" {
		t.Errorf("a basic credential was sent to a provider that does not accept one: %q", ti.gotAuth)
	}
}

// ── THE BASIC ENCODING TRAP ────────────────────────────────────────────────
//
// RFC 6749 §2.3.1 form-urlencodes each half before joining. req.SetBasicAuth
// does not, and is right almost always - which is exactly why a secret with a
// colon in it fails for ever against a conforming server and nobody connects the
// two.
func TestAClientSecretContainingAColonIsEncodedBeforeJoining(t *testing.T) {
	got := basicCredential("client:1", "se:cret")
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly one colon, the separator. SetBasicAuth would produce three.
	if n := strings.Count(string(raw), ":"); n != 1 {
		t.Errorf("the credential decodes to %q with %d colons; the id and secret must be "+
			"escaped before they are joined", raw, n)
	}
	if wantPair := url.QueryEscape("client:1") + ":" + url.QueryEscape("se:cret"); string(raw) != wantPair {
		t.Errorf("the credential is %q, want %q", raw, wantPair)
	}
}

// ── WHAT COMES BACK ─────────────────────────────────────────────────────────

// A plain OAuth 2.0 server answers 200 with a valid token response and no
// id_token. Verifying "" would report a parse error instead of the truth.
func TestATokenResponseWithNoIDTokenIsNamed(t *testing.T) {
	ti := newTokenIdP(t)
	ti.body = `{"access_token":"x","token_type":"bearer"}`
	now := testNow
	p := ti.provider(t, &now, []string{"client_secret_basic"})
	_, err := p.Exchange(context.Background(), "c", "v")
	if err == nil {
		t.Fatal("a response with no id token was accepted")
	}
	if !strings.Contains(err.Error(), "OpenID") {
		t.Errorf("the refusal does not say what is actually wrong: %v", err)
	}
}

func TestATokenEndpointRefusalIsReported(t *testing.T) {
	ti := newTokenIdP(t)
	ti.status = http.StatusBadRequest
	ti.body = `{"error":"invalid_grant","error_description":"code already used"}`
	now := testNow
	p := ti.provider(t, &now, []string{"client_secret_basic"})
	_, err := p.Exchange(context.Background(), "c", "v")
	if err == nil {
		t.Fatal("a 400 from the token endpoint was accepted")
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("the OAuth error code is missing from %v", err)
	}
	// THE DESCRIPTION IS FREE TEXT FROM A REMOTE SYSTEM and never reaches a log
	// line, because a log-forging primitive is what it would be.
	if strings.Contains(err.Error(), "code already used") {
		t.Error("the provider's error_description was carried into our error")
	}
}

// A hostile provider must not be able to forge a log line through the one field
// of its answer that is kept.
func TestAHostileErrorCodeIsFlattened(t *testing.T) {
	got := safeErrorCode("bad\ncode: injected=1")
	if strings.ContainsAny(got, "\n\r :=") {
		t.Errorf("safeErrorCode kept punctuation a log line cares about: %q", got)
	}
	if safeErrorCode("invalid_grant") != "invalid_grant" {
		t.Error("an ordinary OAuth error code was mangled")
	}
}

func TestAnExchangeWithNoCodeIsRefusedBeforeAnythingIsSent(t *testing.T) {
	ti := newTokenIdP(t)
	now := testNow
	p := ti.provider(t, &now, []string{"client_secret_basic"})
	ti.gotForm = nil
	if _, err := p.Exchange(context.Background(), "", "v"); err == nil {
		t.Fatal("an empty code was exchanged")
	}
	if ti.gotForm != nil {
		t.Error("an empty code still reached the identity provider")
	}
}

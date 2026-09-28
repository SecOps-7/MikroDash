package oidc

// Trading an authorization code for an id token.
//
// ── THE MOST DANGEROUS REQUEST IN THE FEATURE ──────────────────────────────
//
// This is the one that carries the client secret and the code verifier. A 307 or
// 308 REPLAYS THE POST BODY to whatever host the provider named, which is a
// one-hop credential exfiltration primitive handed to anyone who controls the
// provider's DNS, its CDN, or a misconfigured path on it. The client refuses
// redirects; see provider.go.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// tokenResponse HAS NO access_token AND NO refresh_token FIELD, AND THAT IS THE
// WHOLE MECHANISM.
//
// encoding/json ignores members with no destination, so those two values are
// decoded into nothing and cease to exist at this line. A field that does not
// exist cannot be logged, stored, returned, or reached for by a later edit that
// "just needs the access token for one thing". Discarding them in a later
// statement would rely on nobody moving that statement.
//
// Nothing is lost. MikroDash calls no provider API and never acts on the user's
// behalf: every claim it needs is in the id token. A refresh token would be a
// long-lived credential for somebody's identity account - a new secret at rest,
// in every backup of /data, with a rotation story and a revocation story - in
// exchange for nothing. `offline_access` is never requested.
//
// THE COST, STATED SO IT IS NOT DISCOVERED LATER: MikroDash cannot notice that
// the provider disabled an account mid-session. A deprovisioned user keeps their
// session until it times out. The remedy is the one that already exists - delete
// or disable the MikroDash user, which ends their sessions.
type tokenResponse struct {
	IDToken string `json:"id_token"`
}

// tokenError is the OAuth failure shape. Only `error` is kept: it comes from a
// fixed vocabulary, while `error_description` is free text from a remote system
// and therefore a log-forging primitive.
type tokenError struct {
	Error string `json:"error"`
}

// Exchange trades the code for an id token, and returns only that.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (string, error) {
	if code == "" || verifier == "" {
		return "", refuse(CodeToken, "the callback carried no code")
	}
	doc, err := p.Discovery(ctx)
	if err != nil {
		return "", err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	// BYTE-IDENTICAL TO THE ONE SENT AT THE START. The provider compares them,
	// and a mismatch produces an `invalid_grant` that reads like a problem with
	// the code rather than with this field.
	form.Set("redirect_uri", p.cfg.RedirectURI)
	form.Set("code_verifier", verifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, doc.TokenEndpoint, nil)
	if err != nil {
		return "", refuse(CodeConfig, "the token endpoint is not a usable URL")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// Client authentication may add to the form, so the body is built after it
	// and in exactly one place.
	p.authenticateClient(req, form, doc)
	encoded := form.Encode()
	req.Body = io.NopCloser(strings.NewReader(encoded))
	req.ContentLength = int64(len(encoded))

	res, err := p.client().Do(req)
	if err != nil {
		var redir *RedirectError
		if errors.As(err, &redir) {
			// Named separately from an unreachable provider: this one is a
			// configuration problem with a specific fix, and it is the
			// credential-leaking case.
			return "", refuse(CodeConfig, redir.Error())
		}
		return "", refuse(CodeProvider, "the identity provider could not be reached")
	}
	defer res.Body.Close()

	body, err := io.ReadAll(io.LimitReader(res.Body, bodyLimit+1))
	if err != nil {
		return "", refuse(CodeProvider, "the identity provider's answer could not be read")
	}
	if len(body) > bodyLimit {
		return "", refuse(CodeProvider, "the identity provider's answer is too large")
	}
	if res.StatusCode != http.StatusOK {
		// The OAuth error CODE only. error_description is free text from a
		// remote system and never reaches a log line.
		var te tokenError
		_ = json.Unmarshal(body, &te)
		if te.Error != "" {
			return "", refuse(CodeDenied, fmt.Sprintf("the identity provider refused the code: %s",
				safeErrorCode(te.Error)))
		}
		return "", refuse(CodeProvider, fmt.Sprintf("the identity provider answered %d", res.StatusCode))
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		return "", refuse(CodeProvider, fmt.Sprintf("the identity provider answered %q, not JSON", ct))
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", refuse(CodeProvider, "the identity provider's answer is not JSON")
	}
	if tr.IDToken == "" {
		// THE FAILURE WORTH NAMING: a plain OAuth 2.0 server with no OpenID
		// support returns a perfectly valid token response with no id_token. An
		// implementation that then verified "" would report a parse error
		// instead of "this is not an OpenID Provider".
		return "", refuse(CodeConfig, "the provider returned no id token; it may not be an OpenID Provider")
	}
	return tr.IDToken, nil
}

// authenticateClient proves who we are to the token endpoint.
//
// ── BASIC WHERE OFFERED, POST WHERE NOT, AND THE METADATA DECIDES ──────────
//
// `client_secret_basic` is mandatory-to-implement in OIDC Core and
// `client_secret_post` is optional, so basic is the one that always works. But
// real providers differ, and the discovery document already says which they
// accept. Reading it is two lines, and without it a whole class of provider
// fails with a 401 that points nowhere.
func (p *Provider) authenticateClient(req *http.Request, form url.Values, doc *discovery) {
	// Some providers want the id in the form even with basic auth, and none
	// object to it being there.
	form.Set("client_id", p.cfg.ClientID)
	if !supports(doc.AuthMethods, "client_secret_basic") && supports(doc.AuthMethods, "client_secret_post") {
		form.Set("client_secret", p.cfg.ClientSecret)
		return
	}
	req.Header.Set("Authorization", "Basic "+basicCredential(p.cfg.ClientID, p.cfg.ClientSecret))
}

// basicCredential builds the Authorization header value.
//
// ── req.SetBasicAuth IS WRONG HERE, AND IT LOOKS RIGHT ─────────────────────
//
// RFC 6749 §2.3.1 form-urlencodes the client id and the secret EACH, then joins
// them with a colon and base64s the result. `SetBasicAuth` joins the raw values.
// It is right almost always, because almost every client secret is
// alphanumeric - and a secret containing a colon or a non-ASCII byte fails
// against a conforming server with an opaque 401, for ever, and nobody connects
// the two.
//
// Written out because the correct version looks like a mistake and will
// otherwise be "fixed" back.
func basicCredential(id, secret string) string {
	pair := url.QueryEscape(id) + ":" + url.QueryEscape(secret)
	return base64.StdEncoding.EncodeToString([]byte(pair))
}

func supports(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

// safeErrorCode keeps a provider's error code to the shape OAuth defines, so a
// hostile one cannot forge a log line through it.
func safeErrorCode(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			out = append(out, r)
			continue
		}
		out = append(out, '?')
	}
	return string(out)
}

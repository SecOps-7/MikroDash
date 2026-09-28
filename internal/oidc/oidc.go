// Package oidc proves which person at which identity provider has just signed
// in, and nothing else.
//
// ── NO JOSE LIBRARY, AND THAT IS A CONSTRAINT RATHER THAN A PREFERENCE ──────
//
// `golang.org/x/crypto` has no JOSE package and this repository takes no new
// dependency without a reason better than convenience, so the signature check,
// the JWK decode and the claim checks are written here against `crypto/rsa`,
// `crypto/ecdsa`, `math/big` and `encoding/base64`. That is a few hundred lines.
//
// It is also the entire authentication boundary. Every check in verify.go is the
// only thing standing between a forged string and a session, so each one names
// the attack it stops — a check removed without a reason reads exactly like one
// that never existed, and here that reads like a login.
//
// ── WHAT IS NOT HERE ────────────────────────────────────────────────────────
//
// No HTTP handlers, no database, no account creation. This package answers one
// question — "which subject at which issuer has just proved themselves, if
// any" — and hands back a Verified or an error. WHICH MikroDash account that is
// happens to be the most consequential decision in the feature, and it is
// policy: it lives with the handlers, in internal/server.
//
// ── THE SUBJECT IS THE ACCOUNT KEY. NEVER THE EMAIL OR THE USERNAME ─────────
//
// Stated here because it is the highest-consequence line in the feature and this
// is where somebody will look. On many identity providers a user can change
// their own email address or preferred username. If either were the key an
// account is found by, changing it to match an existing MikroDash account is a
// takeover that needs no exploit at all.
//
// This is CLAUDE.md's identity-column trap wearing a different hat, and it fails
// the same way: silently, looking correct, and visible only in the real data.
package oidc

import "errors"

// Config is one identity provider as the operator entered it.
//
// The secret is here in plaintext because this package has to send it; it is
// sealed in the database and unsealed on the way to this struct.
type Config struct {
	// Issuer is exactly the string an id token's `iss` must equal. Not
	// normalised, not case-folded, not trailing-slash-tolerant: OIDC Discovery
	// requires exact equality and every bit of leniency is somewhere an attack
	// can live. On a shared provider, two tenants differ only in this string.
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURI is registered at the provider and must be byte-identical in
	// the authorization request and the token exchange, or the provider answers
	// a mismatch with an error that reads like a problem with the code.
	RedirectURI string
}

// Expect is what the caller already knows before it reads a token.
//
// NOTHING HERE COMES OUT OF THE TOKEN. That is the whole point of the type: a
// check that compared a token against itself would pass for any token.
type Expect struct {
	// Issuer is the CONFIGURED issuer, never the discovery document's. A
	// document fetched from a host we do not control cannot be allowed to
	// nominate the issuer it is then checked against.
	Issuer   string
	ClientID string
	// Nonce is the one this process generated for this login. An empty value is
	// a programming error rather than a permission - see verify.go.
	Nonce string
}

// Verified is what a good token proved.
type Verified struct {
	// Subject is the provider's `sub`. See the package header: this is the
	// account key, and the rest is decoration.
	Subject string
	Issuer  string
	// Claims is every claim the token carried, for the configurable lookups -
	// an operator chooses which claim holds the username, the email and the
	// roles, so this package cannot know their names in advance.
	Claims map[string]any
}

// ── THE REFUSAL CODES ───────────────────────────────────────────────────────
//
// A CLOSED SET, AND THE ONLY THING A BROWSER IS TOLD. Sign-in has roughly
// fifteen distinct ways to fail and an unauthenticated stranger can reach every
// one of them. Telling them which half of a forgery worked is free
// intelligence, so a bad signature, a wrong audience, a stale nonce and an
// unknown state all arrive at the browser as the same two or three words. The
// detail goes to the log, and the two are joined by a correlation id.
const (
	// CodeConfig is the operator's problem: the provider is not configured in a
	// way that can work.
	CodeConfig = "config"
	// CodeProvider is the identity provider's problem: unreachable, or
	// answering with something that is not an OpenID Provider's answer.
	CodeProvider = "provider"
	// CodeDenied is a refusal: the person pressed Cancel, or the provider said
	// no. It is not a fault and must not be reported as one.
	CodeDenied = "denied"
	// CodeExpired covers a login that took too long. It is the one failure an
	// ordinary person hits, and the one worth naming precisely, because the
	// remedy is simply to try again.
	CodeExpired = "expired"
	// CodeToken is every way a token failed to prove what it claimed. Several
	// very different faults collapse into it on purpose.
	CodeToken = "token"
)

// Err is how this package refuses.
//
// `Code` is for the browser, `Detail` for the log. Detail is built from fixed
// format strings with non-secret arguments: a token, an authorization code, a
// client secret, a verifier, a state or a nonce must never reach a log line, and
// the way to guarantee that is for no path to put one in this field.
type Err struct {
	Code   string
	Detail string
	Err    error
}

func (e *Err) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

func (e *Err) Unwrap() error { return e.Err }

// refuse builds an Err. Every refusal in this package goes through it, so
// "which codes exist" is answered by reading the constants above rather than by
// grepping for string literals.
func refuse(code, detail string) *Err { return &Err{Code: code, Detail: detail} }

// ErrNoKey says a token named a signing key this process does not hold. The
// caller may refetch the key set ONCE and try again - see jwks.go for why that
// retry is rate limited rather than automatic.
var ErrNoKey = errors.New("oidc: no such signing key")

// StringClaim reads a claim the operator named, when it is a string.
//
// An absent claim and a claim of the wrong shape give the same answer, "", for a
// reason: the caller's next move is identical either way, and a configured claim
// name that does not appear is an operator mistake rather than an exception.
func (v *Verified) StringClaim(name string) string {
	if v == nil || name == "" {
		return ""
	}
	s, _ := v.Claims[name].(string)
	return s
}

// StringsClaim reads a claim the operator named, as a list.
//
// A ROLES CLAIM IS A STRING OR A LIST DEPENDING ON THE PROVIDER, and sometimes
// depending on how many values there are: a single-valued custom claim
// frequently arrives as a bare string where a multi-valued one is an array.
// Accepting both here means the mapping code never has to ask.
//
// Non-string members are dropped rather than stringified: a roles claim carrying
// an object is not something to guess about.
func (v *Verified) StringsClaim(name string) []string {
	out := []string{}
	if v == nil || name == "" {
		return out
	}
	switch t := v.Claims[name].(type) {
	case string:
		if t != "" {
			out = append(out, t)
		}
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// HasClaimOverage reports that the provider left a claim out because it was too
// big, and pointed at an API instead.
//
// ── ENTRA'S GROUP CAP, AND WHY THIS IS NOT SILENTLY IGNORED ─────────────────
//
// Microsoft Entra ID caps the groups claim at 200 in a JWT. Past that it omits
// `groups` entirely and emits `_claim_names` / `_claim_sources` naming a
// Microsoft Graph endpoint to call instead.
//
// MikroDash does not call Graph - it holds no access token to call it with, by
// the design in exchange.go. So a user in more than 200 groups arrives with NO
// roles claim, every role mapping misses, and sign-in is refused.
//
// That refusal is correct. What would be wrong is reporting it as "you have no
// role here", which sends the operator to inspect a mapping that is fine. This
// exists so the refusal can say the true thing and name the two fixes: use Entra
// app roles, which do not overflow this way, or narrow the group claim to the
// groups assigned to the application.
func (v *Verified) HasClaimOverage(name string) bool {
	if v == nil || name == "" {
		return false
	}
	names, ok := v.Claims["_claim_names"].(map[string]any)
	if !ok {
		return false
	}
	_, over := names[name]
	return over
}

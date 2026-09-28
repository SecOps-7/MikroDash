package oidc

// Checking an id token. THIS FILE IS THE AUTHENTICATION BOUNDARY.
//
// ── THE ORDER IS THE SECURITY PROPERTY ──────────────────────────────────────
//
// Structure, then signature, then claims. Never claims before signature: a claim
// read off an unverified token is attacker-controlled data, and comparing the
// nonce against one would turn it into an oracle any unsigned string could
// probe.
//
// The one thing that MUST be read before the signature is the header, because
// the header names the key. That is not a flaw to apologise for - it is the
// reason the header checks below are as strict as they are. Everything the
// header says is a REQUEST, and each one is answered from a fixed list rather
// than obeyed.
//
// ── NO I/O ──────────────────────────────────────────────────────────────────
//
// Verify takes bytes, keys and a clock. It never fetches: the key set is handed
// to it. That is what keeps every case below a table row in the tests.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	algRS256 = "RS256"
	algES256 = "ES256"

	// maxTokenBytes caps the whole compact serialisation before anything is
	// decoded. The callback is unauthenticated, so the first bound has to come
	// before the first allocation.
	maxTokenBytes = 16 << 10

	// clockLeeway covers ordinary NTP drift on exp, iat and nbf.
	//
	// ONE CONSTANT, PINNED BY A TEST, because every second of leeway is a second
	// added to the replay window of every expired token. It is a security
	// parameter rather than a taste.
	clockLeeway = 60 * time.Second

	// maxTokenAge bounds replay independently of whatever `exp` the provider
	// chose. Some set it an hour out; an id token arriving at a callback should
	// be seconds old.
	maxTokenAge = 10 * time.Minute

	// ecdsaSigBytes is what an ES256 JWS signature is: r ‖ s, 32 bytes each.
	ecdsaSigBytes = 64
)

// keySet is a provider's published signing keys.
type keySet struct {
	Keys []jwk `json:"keys"`
}

// header is the JOSE header, including the four members that can nominate a key.
//
// THOSE FOUR ARE PARSED SO THEY CAN BE REFUSED. We do not read them, so ignoring
// them would already be safe — but *ignored* and *honoured* look identical in a
// diff, and a later edit that started reading `jku` would be a one-line total
// bypass with nothing failing. Refusing outright means such an edit has to
// delete a check that says why.
type header struct {
	Alg string          `json:"alg"`
	Kid string          `json:"kid"`
	Typ string          `json:"typ"`
	JWK json.RawMessage `json:"jwk"`
	JKU string          `json:"jku"`
	X5U string          `json:"x5u"`
	X5C json.RawMessage `json:"x5c"`
}

// Verify checks an id token and returns what it proves.
func Verify(raw string, keys *keySet, want Expect, now time.Time) (*Verified, error) {
	// An empty expectation is a programming error, not a permission. Every
	// field below is compared against something the caller already knew; a
	// caller that knew none of it would be asking this function to check a
	// token against itself.
	if want.Issuer == "" || want.ClientID == "" || want.Nonce == "" {
		return nil, Refuse(CodeConfig, "the caller supplied no issuer, client id or nonce to check against")
	}
	if keys == nil || len(keys.Keys) == 0 {
		return nil, Refuse(CodeProvider, "the provider published no signing keys")
	}

	// ── 0. STRUCTURE ────────────────────────────────────────────────────────
	if len(raw) > maxTokenBytes {
		return nil, Refuse(CodeToken, fmt.Sprintf("the token is %d bytes", len(raw)))
	}
	parts := strings.Split(raw, ".")
	// FIVE PARTS IS A JWE, not an attack - but a splitter that took parts[0],
	// parts[1] and "the rest" would verify something that is not what it then
	// interprets. Exactly three, or nothing.
	if len(parts) != 3 {
		return nil, Refuse(CodeToken, fmt.Sprintf("the token has %d segments, not 3", len(parts)))
	}
	for i, p := range parts {
		if p == "" {
			return nil, Refuse(CodeToken, fmt.Sprintf("segment %d is empty", i))
		}
	}

	// ── 1. THE HEADER, AND THE KEYS IT MAY NOT NOMINATE ────────────────────
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, Refuse(CodeToken, "the header is not base64url")
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, Refuse(CodeToken, "the header is not JSON")
	}
	if len(h.JWK) > 0 || h.JKU != "" || h.X5U != "" || len(h.X5C) > 0 {
		return nil, Refuse(CodeToken, "the header tries to nominate its own signing key")
	}
	// `typ` is optional. When present it must say this is a JWT: a weak check
	// that costs nothing and keeps a token minted for another purpose out.
	if h.Typ != "" && !strings.EqualFold(h.Typ, "JWT") {
		return nil, Refuse(CodeToken, fmt.Sprintf("the header typ is %q", h.Typ))
	}

	// ── 2. THE ALGORITHM ALLOW-LIST ────────────────────────────────────────
	//
	// Compared against these two constants. NEVER against the discovery
	// document's advertised algorithms - an attacker who can influence the
	// metadata would then be choosing - and never against the header's own
	// claim about itself.
	//
	// `none` is the canonical bypass: a `case "none": return nil` accepts any
	// claims at all, with no key material, from a browser.
	//
	// HS* is the more interesting one, because OIDC really does define HMAC id
	// tokens keyed on the client secret - so a codebase holding a client secret
	// has a plausible-sounding reason to implement it. That is the trap. The
	// moment an HMAC branch exists, key selection is driven by the token's own
	// header, and an attacker takes the provider's PUBLIC key - published,
	// public by definition - and HMACs a token using its bytes as the secret.
	// With only asymmetric algorithms there is no branch to confuse.
	if h.Alg != algRS256 && h.Alg != algES256 {
		return nil, Refuse(CodeToken, fmt.Sprintf("the token is signed with %q; this verifies %s and %s",
			h.Alg, algRS256, algES256))
	}

	// ── 3. KEY SELECTION ───────────────────────────────────────────────────
	//
	// One key, chosen by kid, or none. NEVER every key in turn: trying them all
	// makes the key set an oracle, lets a key published for another purpose
	// sign for this one, and hides a rotation failure behind a verifier that
	// quietly works anyway.
	var chosen *jwk
	for i := range keys.Keys {
		k := keys.Keys[i]
		if !k.usableFor(h.Alg) {
			continue
		}
		if h.Kid != "" {
			if k.Kid == h.Kid {
				chosen = &keys.Keys[i]
				break
			}
			continue
		}
		// No kid in the header. One usable key is unambiguous; more than one is
		// a guess, and guessing is what this block exists to refuse.
		if chosen != nil {
			return nil, Refuse(CodeToken, "the token names no key and the provider publishes several")
		}
		chosen = &keys.Keys[i]
	}
	if chosen == nil {
		// ErrNoKey, not a refusal: the caller may refetch the key set once and
		// try again, because this is what an ordinary key rotation looks like.
		return nil, ErrNoKey
	}

	// ── 4. THE KEY TYPE MUST MATCH THE ALGORITHM ───────────────────────────
	//
	// Enforced inside publicKey, which has no branch returning an RSA key for
	// ES256 or the reverse.
	pub, err := chosen.publicKey(h.Alg)
	if err != nil {
		return nil, Refuse(CodeToken, err.Error())
	}

	// ── 5. THE SIGNATURE ───────────────────────────────────────────────────
	//
	// THE SIGNING INPUT IS THE BYTES AS RECEIVED, never a re-serialisation of
	// the decoded structures. Re-encoding breaks on whitespace and key order,
	// and - worse - would make the bytes verified different from the bytes
	// interpreted.
	signing := parts[0] + "." + parts[1]
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, Refuse(CodeToken, "the signature is not base64url")
	}
	if err := checkSignature(h.Alg, pub, []byte(signing), sig); err != nil {
		return nil, Refuse(CodeToken, err.Error())
	}

	// ── 6 ONWARD. THE CLAIMS, NOW THAT THEY ARE THE PROVIDER'S WORDS ───────
	cb, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, Refuse(CodeToken, "the claims are not base64url")
	}
	var claims map[string]any
	if err := json.Unmarshal(cb, &claims); err != nil {
		return nil, Refuse(CodeToken, "the claims are not JSON")
	}

	// 6. iss - exact, against the CONFIGURED issuer. Stops a token from another
	// provider, or from another tenant of the same one.
	if iss, _ := claims["iss"].(string); iss != want.Issuer {
		return nil, Refuse(CodeToken, "the token was issued by a different issuer")
	}

	// 7. aud - stops TOKEN SUBSTITUTION. Without it, an id token the provider
	// minted for any other relying party - obtainable by anyone who can register
	// a client there - replays at our callback as that user.
	aud := audienceOf(claims)
	if len(aud) == 0 {
		return nil, Refuse(CodeToken, "the token names no audience")
	}
	if !contains(aud, want.ClientID) {
		return nil, Refuse(CodeToken, "the token was issued for a different client")
	}

	// 8. azp - when several audiences are present the authorised party must be
	// us. Stops another client in a shared audience list replaying its token.
	azp, hasAzp := claims["azp"].(string)
	if len(aud) > 1 && !hasAzp {
		return nil, Refuse(CodeToken, "the token has several audiences and no authorised party")
	}
	if hasAzp && azp != want.ClientID {
		return nil, Refuse(CodeToken, "the token was authorised for a different party")
	}

	// 9-11. The clock. exp and iat are required; nbf is optional.
	exp, ok := claimTime(claims, "exp")
	if !ok {
		return nil, Refuse(CodeToken, "the token has no expiry")
	}
	if now.After(exp.Add(clockLeeway)) {
		return nil, Refuse(CodeExpired, "the token has expired")
	}
	iat, ok := claimTime(claims, "iat")
	if !ok {
		return nil, Refuse(CodeToken, "the token has no issued-at")
	}
	if iat.After(now.Add(clockLeeway)) {
		return nil, Refuse(CodeToken, "the token was issued in the future")
	}
	if now.Sub(iat) > maxTokenAge+clockLeeway {
		return nil, Refuse(CodeExpired, "the token is too old to be arriving now")
	}
	if nbf, ok := claimTime(claims, "nbf"); ok && now.Before(nbf.Add(-clockLeeway)) {
		return nil, Refuse(CodeToken, "the token is not valid yet")
	}

	// 12. nonce - binds THIS token to THIS authentication request, which is the
	// only check here that does. With code+PKCE it is defence in depth, and it
	// is one comparison. Constant time because the nonce is a secret this
	// process generated.
	nonce, _ := claims["nonce"].(string)
	if subtle.ConstantTimeCompare([]byte(nonce), []byte(want.Nonce)) != 1 {
		return nil, Refuse(CodeToken, "the token does not answer this sign-in")
	}

	// 13. sub - it is the identity. See the package header on why it, and not
	// the email or the username, is what an account is found by.
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, Refuse(CodeToken, "the token names no subject")
	}

	return &Verified{Subject: sub, Issuer: want.Issuer, Claims: claims}, nil
}

// checkSignature verifies the one algorithm the header asked for.
//
// NOT CONSTANT TIME, DELIBERATELY. Signature verification operates entirely on
// public data. `crypto/subtle` appears exactly once in this package - on the
// nonce, which is a secret we generated - and a reader who sees it there should
// not be left wondering why it is absent here.
func checkSignature(alg string, pub any, signing, sig []byte) error {
	sum := sha256.Sum256(signing)
	switch alg {
	case algRS256:
		key, ok := pub.(*rsa.PublicKey)
		if !ok {
			return fmt.Errorf("an RS256 token reached a %T", pub)
		}
		// PKCS#1 v1.5, not PSS. VerifyPSS is PS256, and confusing the two
		// produces a verifier that rejects every real token.
		if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
			return fmt.Errorf("the signature does not match the provider's key")
		}
		return nil
	case algES256:
		key, ok := pub.(*ecdsa.PublicKey)
		if !ok {
			return fmt.Errorf("an ES256 token reached a %T", pub)
		}
		// ── 64 RAW BYTES, r ‖ s. NOT ASN.1 DER ─────────────────────────────
		//
		// RFC 7518 §3.4. `ecdsa.VerifyASN1` rejects every genuine token, which
		// is the most common implementation bug in this area.
		//
		// Anything but 64 is refused rather than left-padded or parsed as DER:
		// accepting several encodings of one signature is malleability, and it
		// admits shapes no provider produces.
		//
		// r or s being zero or >= N: ecdsa.Verify already returns false. Said
		// here so nobody adds the check twice, and nobody assumes it was
		// forgotten.
		if len(sig) != ecdsaSigBytes {
			return fmt.Errorf("an ES256 signature is %d bytes; this one is %d", ecdsaSigBytes, len(sig))
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(key, sum[:], r, s) {
			return fmt.Errorf("the signature does not match the provider's key")
		}
		return nil
	}
	return fmt.Errorf("unsupported algorithm %q", alg)
}

// audienceOf reads `aud`, which RFC 7519 allows to be a string or an array.
func audienceOf(claims map[string]any) []string {
	switch t := claims["aud"].(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// claimTime reads a NumericDate.
//
// encoding/json gives a float64 for every JSON number. Unix seconds are around
// 1.7e9 and exactly representable, so the conversion is lossless for every value
// a real token carries.
func claimTime(claims map[string]any, name string) (time.Time, bool) {
	f, ok := claims[name].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

func contains(all []string, want string) bool {
	for _, s := range all {
		if s == want {
			return true
		}
	}
	return false
}

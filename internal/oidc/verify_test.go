package oidc

// The verifier's tests ARE the specification.
//
// Everything else in internal/ is a port, and its correctness question is "does
// this agree with the module it came from". This package has no original: it is
// the first authentication code in the repository with nothing to be pinned
// against, so the gates that make the rest of the tree safe do not apply here
// and these cases are the only oracle.
//
// So every check in verify.go has a named test below, written to fail if that
// check is deleted. A security suite that stays green when a check is removed is
// worse than no suite, because it reports the absence as safety.
//
// ── KEYS ARE GENERATED, NEVER COMMITTED ─────────────────────────────────────
//
// A real private key in the tree would trip `TestNoCommittedCredential` and
// every scanner that reads this public repository. They are made once in
// TestMain and shared.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

const (
	testIssuer = "https://issuer.example/tenant-a"
	testClient = "client-1"
	testNonce  = "nonce-for-this-login"
	testKid    = "key-1"
)

var (
	rsaKey *rsa.PrivateKey
	ecKey  *ecdsa.PrivateKey
	// testNow is fixed so every clock case is exact rather than nearly right.
	testNow = time.Unix(1759000000, 0)
)

func TestMain(m *testing.M) {
	var err error
	if rsaKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		panic(err)
	}
	if ecKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// ── MINTING ─────────────────────────────────────────────────────────────────

// mint builds a compact token. `sign` receives the signing input and returns the
// signature bytes, so a test can sign correctly, wrongly, or not at all.
func mint(t *testing.T, hdr, claims map[string]any, sign func([]byte) []byte) string {
	t.Helper()
	hb, err := json.Marshal(hdr)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return mintRaw(t, string(hb), string(cb), sign)
}

// mintRaw is mint without the JSON round trip, for the cases needing exact
// bytes - notably the one proving the signature covers the token as sent.
func mintRaw(t *testing.T, hdrJSON, claimsJSON string, sign func([]byte) []byte) string {
	t.Helper()
	signing := b64.EncodeToString([]byte(hdrJSON)) + "." + b64.EncodeToString([]byte(claimsJSON))
	return signing + "." + b64.EncodeToString(sign([]byte(signing)))
}

func signRS256(in []byte) []byte {
	sum := sha256.Sum256(in)
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaKey, crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return sig
}

// signES256 produces the JWS form: r ‖ s, 32 bytes each, big-endian, NOT DER.
func signES256(in []byte) []byte {
	sum := sha256.Sum256(in)
	r, s, err := ecdsa.Sign(rand.Reader, ecKey, sum[:])
	if err != nil {
		panic(err)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	s.FillBytes(out[32:])
	return out
}

func rsaJWKS() *keySet {
	return &keySet{Keys: []jwk{{
		Kty: "RSA", Kid: testKid, Use: "sig", Alg: algRS256,
		N: b64.EncodeToString(rsaKey.N.Bytes()),
		E: b64.EncodeToString(big.NewInt(int64(rsaKey.E)).Bytes()),
	}}}
}

func ecJWKS() *keySet {
	return &keySet{Keys: []jwk{{
		Kty: "EC", Kid: testKid, Use: "sig", Alg: algES256, Crv: "P-256",
		X: b64.EncodeToString(ecKey.X.FillBytes(make([]byte, 32))),
		Y: b64.EncodeToString(ecKey.Y.FillBytes(make([]byte, 32))),
	}}}
}

func rsaHeader() map[string]any {
	return map[string]any{"alg": algRS256, "kid": testKid, "typ": "JWT"}
}

func goodClaims() map[string]any {
	return map[string]any{
		"iss": testIssuer, "aud": testClient, "sub": "subject-1", "nonce": testNonce,
		"iat": float64(testNow.Unix()),
		"exp": float64(testNow.Add(5 * time.Minute).Unix()),
	}
}

func want() Expect {
	return Expect{Issuer: testIssuer, ClientID: testClient, Nonce: testNonce}
}

// ── THE BELIEVABILITY FLOOR ─────────────────────────────────────────────────
//
// Without this, a mutation that broke EVERYTHING would leave a security suite
// entirely green in the wrong direction: every refusal test would still pass,
// because everything would be refused.

func TestAGenuineTokenSignsTheUserIn(t *testing.T) {
	v, err := Verify(mint(t, rsaHeader(), goodClaims(), signRS256), rsaJWKS(), want(), testNow)
	if err != nil {
		t.Fatalf("a good RS256 token was refused: %v", err)
	}
	if v.Subject != "subject-1" || v.Issuer != testIssuer {
		t.Errorf("verified %+v, want subject-1 at %s", v, testIssuer)
	}
}

func TestAGenuineES256TokenSignsTheUserIn(t *testing.T) {
	h := map[string]any{"alg": algES256, "kid": testKid, "typ": "JWT"}
	if _, err := Verify(mint(t, h, goodClaims(), signES256), ecJWKS(), want(), testNow); err != nil {
		t.Fatalf("a good ES256 token was refused: %v", err)
	}
}

// ── THE FORGERIES ───────────────────────────────────────────────────────────

// alg:none - the canonical bypass. No key material, from a browser.
func TestATokenSignedWithAlgNoneIsRefused(t *testing.T) {
	h := map[string]any{"alg": "none", "kid": testKid}
	raw := mint(t, h, goodClaims(), func([]byte) []byte { return nil })
	if _, err := Verify(raw, rsaJWKS(), want(), testNow); err == nil {
		t.Fatal("an alg:none token was accepted")
	}
	signed := mint(t, h, goodClaims(), func([]byte) []byte { return []byte("x") })
	_, err := Verify(signed, rsaJWKS(), want(), testNow)
	if err == nil {
		t.Fatal("an alg:none token with a junk signature was accepted")
	}
	// ── AND IT IS THE ALLOW-LIST THAT REFUSED IT, NOT KEY SELECTION ────────
	//
	// A mutation sweep found this: with the allow-list removed the token is
	// still refused, but by key selection, as ErrNoKey - which is the signal
	// that tells the caller to REFETCH THE KEY SET. A junk `alg` from an
	// unauthenticated callback would then be an attacker-triggered fetch at the
	// operator's identity provider.
	//
	// So the assertion is about WHICH check fired, and it is the reason the
	// allow-list sits above key selection rather than below it.
	if errors.Is(err, ErrNoKey) {
		t.Error("an alg:none token was refused as an unknown key, so the algorithm allow-list " +
			"no longer runs before key selection; a junk alg can now trigger a JWKS refetch")
	}
}

// ALGORITHM CONFUSION. The provider's public key is public; an HMAC branch turns
// it into a secret the attacker also holds.
func TestATokenHMACedWithTheProvidersPublicKeyIsRefused(t *testing.T) {
	pubBytes := rsaKey.N.Bytes()
	h := map[string]any{"alg": "HS256", "kid": testKid}
	raw := mint(t, h, goodClaims(), func(in []byte) []byte {
		mac := hmac.New(sha256.New, pubBytes)
		mac.Write(in)
		return mac.Sum(nil)
	})
	hmacErr := func() error { _, e := Verify(raw, rsaJWKS(), want(), testNow); return e }()
	if hmacErr == nil {
		t.Fatal("a token HMACed with the provider's own public key was accepted - " +
			"this is total authentication bypass")
	}
	// Refused by the allow-list, for the reason above: HS256 must never reach
	// key selection, where an HMAC branch would later be able to pick up a
	// public key and treat it as a shared secret.
	if errors.Is(hmacErr, ErrNoKey) {
		t.Error("an HS256 token was refused as an unknown key rather than by the allow-list")
	}
}

func TestAHeaderNamingItsOwnKeyIsRefused(t *testing.T) {
	for _, member := range []string{"jwk", "jku", "x5u", "x5c"} {
		h := rsaHeader()
		switch member {
		case "jwk":
			h[member] = map[string]any{"kty": "RSA"}
		case "x5c":
			h[member] = []any{"cert"}
		default:
			h[member] = "https://attacker.example/keys"
		}
		if _, err := Verify(mint(t, h, goodClaims(), signRS256), rsaJWKS(), want(), testNow); err == nil {
			t.Errorf("a header carrying %q was accepted; a later edit that started reading it "+
				"would be a one-line bypass", member)
		}
	}
}

func TestATokenWithOneFlippedPayloadByteIsRefused(t *testing.T) {
	raw := mint(t, rsaHeader(), goodClaims(), signRS256)
	parts := strings.Split(raw, ".")
	b := []byte(parts[1])
	b[len(b)/2] ^= 0x01
	if _, err := Verify(parts[0]+"."+string(b)+"."+parts[2], rsaJWKS(), want(), testNow); err == nil {
		t.Fatal("a token whose claims were altered after signing was accepted")
	}
}

// THE SIGNING INPUT IS THE BYTES AS SENT. A verifier that re-serialised the
// decoded header would compute a different input and reject this - or, worse,
// verify bytes that are not the ones it then interprets.
func TestTheSignatureIsCheckedOverTheBytesAsSent(t *testing.T) {
	cb, err := json.Marshal(goodClaims())
	if err != nil {
		t.Fatal(err)
	}
	spaced := `{"alg":"RS256", "kid":"key-1"}`
	if _, err := Verify(mintRaw(t, spaced, string(cb), signRS256), rsaJWKS(), want(), testNow); err != nil {
		t.Fatalf("a token whose header JSON has a space was refused (%v) - the signature is "+
			"being checked over a re-serialisation rather than over what arrived", err)
	}
}

// ── KEY SELECTION ───────────────────────────────────────────────────────────

func TestATokenNamingAnUnknownKidAsksForARefetch(t *testing.T) {
	h := rsaHeader()
	h["kid"] = "some-other-key"
	_, err := Verify(mint(t, h, goodClaims(), signRS256), rsaJWKS(), want(), testNow)
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("an unknown kid gave %v, want ErrNoKey so the caller can refetch once", err)
	}
}

func TestAKeySetWithTwoKeysAndNoKidIsRefused(t *testing.T) {
	keys := rsaJWKS()
	second := keys.Keys[0]
	second.Kid = "key-2"
	keys.Keys = append(keys.Keys, second)
	h := rsaHeader()
	delete(h, "kid")
	if _, err := Verify(mint(t, h, goodClaims(), signRS256), keys, want(), testNow); err == nil {
		t.Fatal("a token naming no key was verified against a set holding several; " +
			"trying keys in turn makes the key set an oracle")
	}
}

// Providers publish encryption keys beside signing keys. Using one to check a
// signature is what happens to anyone who selects on kid alone.
func TestAnEncryptionKeyIsNotUsedToVerifyASignature(t *testing.T) {
	keys := rsaJWKS()
	keys.Keys[0].Use = "enc"
	if _, err := Verify(mint(t, rsaHeader(), goodClaims(), signRS256), keys, want(), testNow); err == nil {
		t.Fatal("a key published for encryption verified a signature")
	}
	// THE CONTROL: the same key marked for signing does verify, so the check
	// above is about `use` and not about the key being broken.
	keys.Keys[0].Use = "sig"
	if _, err := Verify(mint(t, rsaHeader(), goodClaims(), signRS256), keys, want(), testNow); err != nil {
		t.Fatalf("the same key marked sig was refused: %v", err)
	}
}

func TestAnECKeyCannotVerifyAnRS256Token(t *testing.T) {
	keys := ecJWKS()
	keys.Keys[0].Alg = "" // do not let the alg cross-check do the work
	if _, err := Verify(mint(t, rsaHeader(), goodClaims(), signRS256), keys, want(), testNow); err == nil {
		t.Fatal("an EC key verified an RS256 token")
	}
}

func TestAPrivateKeyInTheKeySetIsRefused(t *testing.T) {
	keys := rsaJWKS()
	keys.Keys[0].D = b64.EncodeToString([]byte("a private exponent"))
	if _, err := Verify(mint(t, rsaHeader(), goodClaims(), signRS256), keys, want(), testNow); err == nil {
		t.Fatal("a key set publishing a PRIVATE key was used anyway")
	}
}

// ── THE JWK DECODER ─────────────────────────────────────────────────────────

func TestAShortRSAKeyIsRefused(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	k := jwk{Kty: "RSA", N: b64.EncodeToString(small.N.Bytes()),
		E: b64.EncodeToString(big.NewInt(int64(small.E)).Bytes())}
	if _, err := k.publicKey(algRS256); err == nil {
		t.Fatalf("a %d-bit RSA key was accepted; it is factorable", small.N.BitLen())
	}
}

func TestAnRSAJWKWithAnAbsurdExponentIsRefused(t *testing.T) {
	k := rsaJWKS().Keys[0]
	k.E = b64.EncodeToString(make([]byte, 64))
	if _, err := k.publicKey(algRS256); err == nil {
		t.Fatal("a 64-byte RSA exponent was accepted; it overflows an int on 32-bit")
	}
}

func TestAnECJWKWithAShortCoordinateIsRefused(t *testing.T) {
	k := ecJWKS().Keys[0]
	k.X = b64.EncodeToString(ecKey.X.Bytes()[1:])
	if _, err := k.publicKey(algES256); err == nil {
		t.Fatal("an EC coordinate shorter than 32 bytes was accepted")
	}
}

func TestAnECJWKOffTheCurveIsRefused(t *testing.T) {
	k := ecJWKS().Keys[0]
	bad := make([]byte, 32)
	bad[31] = 2
	k.Y = b64.EncodeToString(bad)
	if _, err := k.publicKey(algES256); err == nil {
		t.Fatal("a point that is not on P-256 was accepted as a key")
	}
}

func TestADERencodedES256SignatureIsRefused(t *testing.T) {
	h := map[string]any{"alg": algES256, "kid": testKid, "typ": "JWT"}
	raw := mint(t, h, goodClaims(), func(in []byte) []byte {
		sum := sha256.Sum256(in)
		der, err := ecdsa.SignASN1(rand.Reader, ecKey, sum[:])
		if err != nil {
			panic(err)
		}
		return der
	})
	if _, err := Verify(raw, ecJWKS(), want(), testNow); err == nil {
		t.Fatal("a DER-encoded ES256 signature was accepted; accepting two encodings of " +
			"one signature is malleability")
	}
}

// ── THE CLAIMS ──────────────────────────────────────────────────────────────

func TestTheClaimChecksEachRefuse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		claim string
		value any
		stops string
	}{
		{"another issuer", "iss", "https://issuer.example/tenant-b",
			"a token from another tenant of the same provider"},
		{"another client", "aud", "someone-elses-client",
			"token substitution: an id token minted for any other relying party"},
		{"no audience", "aud", "", "a token naming nobody"},
		{"another login's nonce", "nonce", "a-different-nonce",
			"replay of a token captured from another sign-in"},
		{"no subject", "sub", "", "a token that names no identity"},
		{"expired", "exp", float64(testNow.Add(-2 * time.Minute).Unix()),
			"replay of a token captured after it expired"},
		{"issued in the future", "iat", float64(testNow.Add(10 * time.Minute).Unix()),
			"a forged issue time"},
		{"issued far too long ago", "iat", float64(testNow.Add(-30 * time.Minute).Unix()),
			"replay bounded independently of whatever exp the provider chose"},
		{"not yet valid", "nbf", float64(testNow.Add(10 * time.Minute).Unix()),
			"a token minted for later use"},
	} {
		claims := goodClaims()
		claims[tc.claim] = tc.value
		if _, err := Verify(mint(t, rsaHeader(), claims, signRS256), rsaJWKS(), want(), testNow); err == nil {
			t.Errorf("%s was accepted; that check stops %s", tc.name, tc.stops)
		}
	}
}

// A shared audience list needs an authorised party, and it must be us.
func TestAMultiAudienceTokenIsCheckedOnAzp(t *testing.T) {
	claims := goodClaims()
	claims["aud"] = []any{testClient, "another-client"}
	if _, err := Verify(mint(t, rsaHeader(), claims, signRS256), rsaJWKS(), want(), testNow); err == nil {
		t.Error("a token with several audiences and no azp was accepted")
	}
	claims["azp"] = "another-client"
	if _, err := Verify(mint(t, rsaHeader(), claims, signRS256), rsaJWKS(), want(), testNow); err == nil {
		t.Error("a token authorised for another party was accepted")
	}
	// THE CONTROL: the same shape, authorised for us, is fine.
	claims["azp"] = testClient
	if _, err := Verify(mint(t, rsaHeader(), claims, signRS256), rsaJWKS(), want(), testNow); err != nil {
		t.Errorf("a token in a shared audience authorised for us was refused: %v", err)
	}
}

// ── THE CLOCK CONSTANT ──────────────────────────────────────────────────────
//
// Every second of leeway is a second added to the replay window of every expired
// token, so the number is pinned rather than left to taste.
func TestTheClockLeewayIsSixtySeconds(t *testing.T) {
	if clockLeeway != 60*time.Second {
		t.Fatalf("the clock leeway is %v, and the cases below assume 60s", clockLeeway)
	}
	inside := goodClaims()
	inside["exp"] = float64(testNow.Add(-30 * time.Second).Unix())
	if _, err := Verify(mint(t, rsaHeader(), inside, signRS256), rsaJWKS(), want(), testNow); err != nil {
		t.Errorf("a token 30s past expiry was refused; ordinary clock drift would break sign-in: %v", err)
	}
	outside := goodClaims()
	outside["exp"] = float64(testNow.Add(-90 * time.Second).Unix())
	if _, err := Verify(mint(t, rsaHeader(), outside, signRS256), rsaJWKS(), want(), testNow); err == nil {
		t.Error("a token 90s past expiry was accepted")
	}
}

// ── THE STRUCTURE ───────────────────────────────────────────────────────────

func TestAMalformedTokenIsRefused(t *testing.T) {
	good := mint(t, rsaHeader(), goodClaims(), signRS256)
	parts := strings.Split(good, ".")
	for _, tc := range []struct{ name, raw string }{
		{"two segments", parts[0] + "." + parts[1]},
		{"five segments, which is a JWE", good + ".extra.more"},
		{"an empty signature", parts[0] + "." + parts[1] + "."},
		{"padded base64", parts[0] + "==." + parts[1] + "." + parts[2]},
		{"nothing at all", ""},
		{"oversized", strings.Repeat("a", maxTokenBytes+1)},
	} {
		if _, err := Verify(tc.raw, rsaJWKS(), want(), testNow); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}
}

// A caller that knew nothing to check against would be asking this function to
// compare a token with itself.
func TestVerifyRefusesAnEmptyExpectation(t *testing.T) {
	raw := mint(t, rsaHeader(), goodClaims(), signRS256)
	for _, e := range []Expect{
		{ClientID: testClient, Nonce: testNonce},
		{Issuer: testIssuer, Nonce: testNonce},
		{Issuer: testIssuer, ClientID: testClient},
	} {
		if _, err := Verify(raw, rsaJWKS(), e, testNow); err == nil {
			t.Errorf("Verify accepted a token against an incomplete expectation %+v", e)
		}
	}
}

// ── THE CLAIM ACCESSORS ─────────────────────────────────────────────────────

func TestARolesClaimIsReadWhetherItIsAStringOrAList(t *testing.T) {
	v := &Verified{Claims: map[string]any{
		"one":   "netops",
		"many":  []any{"netops", "admins", 7, ""},
		"empty": "",
	}}
	if got := v.StringsClaim("one"); len(got) != 1 || got[0] != "netops" {
		t.Errorf("a single-valued roles claim read as %v", got)
	}
	if got := v.StringsClaim("many"); len(got) != 2 || got[0] != "netops" || got[1] != "admins" {
		t.Errorf("a list roles claim read as %v; non-strings must be dropped, not stringified", got)
	}
	for _, name := range []string{"empty", "absent"} {
		if got := v.StringsClaim(name); len(got) != 0 {
			t.Errorf("%s read as %v, want nothing", name, got)
		}
	}
}

// Entra omits `groups` past 200 and points at Graph instead. MikroDash does not
// call Graph, so the sign-in is refused - and it must be refused for the true
// reason rather than as "you have no role here".
func TestAGroupsOverageIsRecognisedRatherThanReadAsNoGroups(t *testing.T) {
	over := &Verified{Claims: map[string]any{
		"_claim_names":   map[string]any{"groups": "src1"},
		"_claim_sources": map[string]any{"src1": map[string]any{"endpoint": "https://graph.example"}},
	}}
	if !over.HasClaimOverage("groups") {
		t.Error("a groups overage was not recognised; the operator would be sent to inspect " +
			"a role mapping that is fine")
	}
	if over.HasClaimOverage("roles") {
		t.Error("a claim with no overage was reported as overflowed")
	}
	// THE CONTROL: an ordinary token reports no overage.
	if (&Verified{Claims: map[string]any{"groups": []any{"netops"}}}).HasClaimOverage("groups") {
		t.Error("an ordinary groups claim was reported as overflowed")
	}
}

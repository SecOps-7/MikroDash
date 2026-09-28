package oidc

// One JSON Web Key into a public key, and nothing else. No I/O lives here.
//
// ── THE ENCODING, BECAUSE GETTING IT WRONG IS SILENT ────────────────────────
//
// RFC 7518 §6: every numeric JWK member is the BIG-ENDIAN UNSIGNED integer,
// base64url with no padding. `big.Int.SetBytes` reads exactly that - big-endian,
// unsigned, no sign byte - so there is no reversal and no two's-complement
// handling anywhere in this file. If you find yourself writing either, the input
// is not a JWK.

import (
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
)

const (
	// minRSABits refuses a modulus small enough to factor.
	//
	// Go verifies happily against a 512-bit key, and a 512-bit RSA key is
	// factorable on a laptop. This is the check that makes substituting a key
	// set expensive rather than free, and it is why the number is here rather
	// than left to whatever the provider published.
	minRSABits = 2048
	// maxExponentBytes bounds `e` BEFORE it becomes an int. Without it a
	// hostile key set overflows on 32-bit and produces an absurd exponent on
	// 64-bit. A real exponent is three bytes (65537).
	maxExponentBytes = 8
	// p256CoordBytes is what RFC 7518 §6.2.1.2 requires of x and y: the full
	// coordinate size, left-padded with zeros. A short value is refused rather
	// than padded - see ecKey.
	p256CoordBytes = 32
)

// jwk is one key as a provider publishes it.
//
// `D` is here ONLY so a private key can be recognised and refused. Nothing
// reads it.
type jwk struct {
	Kty    string   `json:"kty"`
	Kid    string   `json:"kid"`
	Use    string   `json:"use"`
	Alg    string   `json:"alg"`
	KeyOps []string `json:"key_ops"`
	Crv    string   `json:"crv"`
	N      string   `json:"n"`
	E      string   `json:"e"`
	X      string   `json:"x"`
	Y      string   `json:"y"`
	D      string   `json:"d"`
}

// usableFor reports whether this key may verify a signature made with `alg`.
//
// ── A KEY SET HOLDS KEYS FOR MORE THAN ONE JOB ─────────────────────────────
//
// Providers publish encryption keys alongside signing keys in the same
// document. Using one to check a signature is not hypothetical: it is what
// happens to anyone who selects a key by `kid` alone and meets a collision, or
// who tries every key in turn.
//
// Each field is checked only when the provider supplied it. Absent means "not
// stated", and a key that states nothing is usable - that is what RFC 7517 says,
// and refusing it would break real providers.
func (k jwk) usableFor(alg string) bool {
	if k.Use != "" && k.Use != "sig" {
		return false
	}
	if len(k.KeyOps) > 0 {
		ok := false
		for _, op := range k.KeyOps {
			if op == "verify" {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	// A key that names its own algorithm must agree with the token's header.
	// This is the cross-check that stops a P-256 key being handed an RS256
	// token because the two happened to share a kid.
	return k.Alg == "" || k.Alg == alg
}

// publicKey turns the JWK into something crypto can verify with.
//
// The returned key is matched to `alg` by TYPE: an RS256 token can only ever end
// up with an *rsa.PublicKey and an ES256 token with an *ecdsa.PublicKey, because
// no branch here produces the other one. verify.go relies on that and says so.
func (k jwk) publicKey(alg string) (crypto.PublicKey, error) {
	// A PROVIDER PUBLISHING A PRIVATE KEY IS A CATASTROPHIC PROVIDER BUG, and
	// ingesting it quietly would mean holding it in memory and possibly writing
	// it somewhere. Refuse before decoding anything.
	if k.D != "" {
		return nil, fmt.Errorf("the key set contains a PRIVATE key (kid %q); refusing it", k.Kid)
	}
	switch {
	case alg == algRS256 && k.Kty == "RSA":
		return k.rsaKey()
	case alg == algES256 && k.Kty == "EC":
		return k.ecKey()
	}
	return nil, fmt.Errorf("a %q key cannot verify a %s signature", k.Kty, alg)
}

func (k jwk) rsaKey() (*rsa.PublicKey, error) {
	nb, err := b64.DecodeString(k.N)
	if err != nil || len(nb) == 0 {
		return nil, fmt.Errorf("the RSA modulus is not base64url")
	}
	eb, err := b64.DecodeString(k.E)
	if err != nil || len(eb) == 0 {
		return nil, fmt.Errorf("the RSA exponent is not base64url")
	}
	if len(eb) > maxExponentBytes {
		return nil, fmt.Errorf("the RSA exponent is %d bytes; no real one is", len(eb))
	}
	e := new(big.Int).SetBytes(eb).Int64()
	// An even exponent is not a valid RSA exponent, and 1 would make every
	// signature verify against itself. Both are nonsense rather than attacks,
	// but nonsense that reaches crypto/rsa is worth stopping where it can still
	// be named.
	if e < 3 || e%2 == 0 {
		return nil, fmt.Errorf("the RSA exponent %d is not usable", e)
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: int(e)}
	if bits := key.N.BitLen(); bits < minRSABits {
		return nil, fmt.Errorf("the RSA key is %d bits; %d is the minimum", bits, minRSABits)
	}
	return key, nil
}

func (k jwk) ecKey() (*ecdsa.PublicKey, error) {
	// ES256 IS P-256 AND ONLY P-256. The curve is part of the algorithm, so a
	// key on another curve under an ES256 header is a mismatch rather than a
	// curve this code has not got round to supporting.
	if k.Crv != "P-256" {
		return nil, fmt.Errorf("ES256 needs a P-256 key; this one is %q", k.Crv)
	}
	x, err := b64.DecodeString(k.X)
	if err != nil {
		return nil, fmt.Errorf("the EC x coordinate is not base64url")
	}
	y, err := b64.DecodeString(k.Y)
	if err != nil {
		return nil, fmt.Errorf("the EC y coordinate is not base64url")
	}
	// EXACTLY 32 BYTES EACH, not "at most". RFC 7518 requires the full
	// coordinate size, zero-padded on the left. Accepting a short value and
	// padding it is the kind of leniency that admits shapes no provider emits,
	// which is the smell that precedes a real break.
	if len(x) != p256CoordBytes || len(y) != p256CoordBytes {
		return nil, fmt.Errorf("the EC coordinates are %d and %d bytes; both must be %d",
			len(x), len(y), p256CoordBytes)
	}
	// ── ON THE CURVE, CHECKED BY BUILDING THE POINT ────────────────────────
	//
	// `elliptic.IsOnCurve` is deprecated. `crypto/ecdh` parses an uncompressed
	// point (0x04 ‖ x ‖ y) and refuses one that is off the curve or is the
	// identity, so it serves as the validator; the parsed value is discarded,
	// because ECDH is not what this key is for.
	//
	// DEFENCE IN DEPTH RATHER THAN THE GUARD: `ecdsa.Verify` returns false for
	// a garbage point anyway, and signature VERIFICATION has no secret scalar
	// to leak the way ECDH would. It is here because a point off the curve
	// means the key set itself is wrong, and saying that beats a signature that
	// merely fails to verify.
	point := make([]byte, 0, 1+2*p256CoordBytes)
	point = append(point, 0x04)
	point = append(point, x...)
	point = append(point, y...)
	if _, err := ecdh.P256().NewPublicKey(point); err != nil {
		return nil, fmt.Errorf("the EC key is not a point on P-256")
	}
	return &ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     new(big.Int).SetBytes(x),
		Y:     new(big.Int).SetBytes(y),
	}, nil
}

// b64 is the one decoder this package uses for JOSE values.
//
// RAW, UNPADDED, URL ALPHABET - RFC 7515 §2. A value carrying `+`, `/` or `=`
// was produced by something not following the spec, and decoding it anyway
// would mean accepting two encodings of one value.
var b64 = base64.RawURLEncoding

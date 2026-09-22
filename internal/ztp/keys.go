// Package ztp is zero-touch provisioning: the userspace WireGuard endpoint that
// routers call home to, the addresses and keys it hands out, and the bootstrap
// scripts that make a router call.
//
// ── WHY USERSPACE ───────────────────────────────────────────────────────────
//
// TunGuard (MIT), the prior art, runs wireguard-go on a KERNEL tun interface,
// which needs root and NET_ADMIN, a /dev/net/tun in the container, and routes.
// This uses the same library's netstack instead: the tunnel's IP stack lives in
// this process, MikroDash dials a router's API through it like any other
// connection, and the container needs nothing but one published UDP port. Its
// netstack does not forward, so devices on the tunnel cannot reach each other.
//
// Keys, addresses and scripts are pure and tested without a network; the engine
// is the one part with sockets.
package ztp

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewKeyPair makes an X25519 keypair in WireGuard's base64 form. RouterOS takes
// such a private key (`/interface/wireguard add private-key=`) and reports the
// same public key Go derives from it (measured, cmd/ztpprobe z4, 7.24.4).
func NewKeyPair() (private, public string, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()),
		base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// PublicKey derives the public key from a base64 private key.
func PublicKey(private string) (string, error) {
	raw, err := decodeKey(private)
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// ValidKey is whether s is a WireGuard key: 32 bytes, base64.
func ValidKey(s string) bool {
	_, err := decodeKey(s)
	return err == nil
}

func decodeKey(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("not a WireGuard key")
	}
	return b, nil
}

// hexKey is the form wireguard-go's configuration protocol wants.
func hexKey(s string) (string, error) {
	b, err := decodeKey(s)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

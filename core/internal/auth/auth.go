// Package auth covers secrets shared with agents (join tokens, per-node
// credentials) and, later, admin authentication and RBAC.
//
// Secrets are random opaque tokens. The database stores only SHA-256 hashes,
// so a leaked database does not leak usable credentials.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// NewSecret returns a fresh random secret and its storable hash.
func NewSecret() (raw string, hash string) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, HashSecret(raw)
}

// HashSecret hashes a secret for storage or lookup.
func HashSecret(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// Package secret encrypts small secrets at rest — the private components of
// generated variable groups (REALITY private keys, post-quantum seeds).
//
// Threat model: someone walks off with the SQLite file (a stolen backup, a
// snapshot of the panel host's disk) but not the panel's environment. The key
// lives in CHIRAL_SECRET_KEY, so the database alone yields nothing usable.
// This does NOT defend against an attacker who already runs code on the panel
// host — they can read the environment.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	plainPrefix = "plain:"
	encPrefix   = "enc:v1:"
	// MinKeyLen guards against a short, guessable CHIRAL_SECRET_KEY. The key
	// is expected to be high-entropy (e.g. `openssl rand -base64 32`), not a
	// human-chosen passphrase — it is hashed, not stretched.
	MinKeyLen = 16
)

// ErrNoKey is returned when opening ciphertext without a configured key.
var ErrNoKey = errors.New("value is encrypted but CHIRAL_SECRET_KEY is not set")

// Box seals and opens secrets. The zero value is not usable; use NewBox.
type Box struct {
	aead  cipher.AEAD
	keyID string
}

// NewBox builds a Box from the raw key material. An empty key returns a Box
// that stores values in the clear — supported so a panel can start without
// configuration, but the caller should warn loudly.
func NewBox(key string) (*Box, error) {
	if key == "" {
		return &Box{}, nil
	}
	if len(key) < MinKeyLen {
		return nil, fmt.Errorf("CHIRAL_SECRET_KEY must be at least %d characters; generate one with `openssl rand -base64 32`", MinKeyLen)
	}
	sum := sha256.Sum256([]byte(key))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// A short, non-reversible fingerprint of the key, stored alongside the
	// ciphertext so a changed key produces a clear diagnostic instead of an
	// opaque authentication failure.
	idSum := sha256.Sum256([]byte("chiral-key-id\x00" + key))
	return &Box{aead: aead, keyID: hex.EncodeToString(idSum[:4])}, nil
}

// Enabled reports whether values are actually encrypted.
func (b *Box) Enabled() bool { return b.aead != nil }

// Seal encrypts value. name is bound into the ciphertext as additional
// authenticated data, so a ciphertext cannot be moved to a different variable
// by someone with write access to the database.
func (b *Box) Seal(name, value string) (string, error) {
	if !b.Enabled() {
		return plainPrefix + value, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(value), []byte(name))
	return encPrefix + b.keyID + ":" + base64.RawStdEncoding.EncodeToString(ct), nil
}

// Owns reports whether this Box sealed the stored value.
//
// Distinguishes the two reasons Open can succeed: the value is ours, or it is
// plaintext from a panel that ran with no key. A key rotation has to tell
// those apart — the first is already done, the second still needs sealing.
func (b *Box) Owns(stored string) bool {
	rest, ok := strings.CutPrefix(stored, encPrefix)
	if !ok || !b.Enabled() {
		return false
	}
	keyID, _, ok := strings.Cut(rest, ":")
	return ok && keyID == b.keyID
}

// Open decrypts a stored value. It accepts values written in the clear (by a
// panel with no key configured) so enabling encryption later is not a
// breaking change; such values are simply returned as-is.
func (b *Box) Open(name, stored string) (string, error) {
	if rest, ok := strings.CutPrefix(stored, plainPrefix); ok {
		return rest, nil
	}
	rest, ok := strings.CutPrefix(stored, encPrefix)
	if !ok {
		// Values written before this package existed have no prefix.
		return stored, nil
	}
	keyID, payload, ok := strings.Cut(rest, ":")
	if !ok {
		return "", errors.New("malformed encrypted value")
	}
	if !b.Enabled() {
		return "", ErrNoKey
	}
	if keyID != b.keyID {
		return "", fmt.Errorf("value was encrypted with a different CHIRAL_SECRET_KEY (stored key id %s, current %s)", keyID, b.keyID)
	}
	raw, err := base64.RawStdEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("malformed encrypted value: %w", err)
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("malformed encrypted value: too short")
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], []byte(name))
	if err != nil {
		return "", fmt.Errorf("decrypting %q failed (wrong key, or the stored value was tampered with)", name)
	}
	return string(pt), nil
}

// IsEncrypted reports whether a stored value is ciphertext.
func IsEncrypted(stored string) bool { return strings.HasPrefix(stored, encPrefix) }

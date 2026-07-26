package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// Passwords need a different treatment from the panel's other secrets.
//
// Join tokens and node credentials are 32 random bytes, so a single SHA-256 is
// enough — there is nothing to guess. A password is chosen by a person and
// lives in the guessable space, so it gets a deliberately slow hash with a
// per-password salt.
//
// PBKDF2-HMAC-SHA256 comes from the standard library, which keeps the panel
// free of a crypto dependency for this.
const (
	pbkdf2Iterations = 210_000 // OWASP's current PBKDF2-HMAC-SHA256 guidance
	saltLen          = 16
	keyLen           = 32
	// MinPasswordLen is enforced at the API, not here: rejecting a weak
	// password is a policy decision and belongs where the message is shown.
	MinPasswordLen = 8
)

// HashPassword returns an encoded hash of the form
// "pbkdf2$<iterations>$<salt>$<key>", so the parameters travel with the hash
// and can be raised later without invalidating existing passwords.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2$%d$%s$%s",
		pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches the encoded hash. The
// comparison is constant-time; a malformed or unknown-scheme hash simply fails
// rather than erroring, so a corrupted row cannot be told apart from a wrong
// password by an attacker.
func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is a real hash of a value nobody can supply, used to spend the
// same work on a username that does not exist as on one that does.
//
// Built once at startup rather than per request: PBKDF2 at these iteration
// counts is the expensive part, and hashing twice on every miss would double
// the cost of exactly the path an attacker controls.
var dummyHash = func() string {
	unguessable := make([]byte, 32)
	if _, err := rand.Read(unguessable); err != nil {
		// Only reachable if the system entropy source is broken, at which
		// point the panel has larger problems than a timing side channel.
		panic("auth: cannot read random bytes: " + err.Error())
	}
	h, err := HashPassword(base64.RawStdEncoding.EncodeToString(unguessable))
	if err != nil {
		panic("auth: cannot build dummy hash: " + err.Error())
	}
	return h
}()

// SpendVerification does the work of a password check and throws the answer
// away.
//
// Without it, "no such user" returns as fast as the database lookup while a
// real username costs 210,000 PBKDF2 rounds — a difference of two orders of
// magnitude, measurable over the network, and enough to enumerate accounts.
// Call it on every path that declines before reaching VerifyPassword.
func SpendVerification(password string) {
	VerifyPassword(password, dummyHash)
}

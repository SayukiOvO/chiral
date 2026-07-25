package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP, per RFC 6238. Small enough to implement against the RFC's own test
// vectors (see totp_test.go), which is the only reason it is here rather than
// behind a dependency: an authenticator that silently accepts the wrong code
// is not something to take on trust.
const (
	// TOTPPeriod is the 30-second step every authenticator app assumes.
	TOTPPeriod = 30 * time.Second
	// TOTPDigits is the 6-digit code every authenticator app assumes.
	TOTPDigits = 6
	// TOTPSkew is how many steps either side of now are accepted, covering
	// clock drift between the panel and the phone. One step each way is the
	// usual compromise: two extra codes valid at any moment, rather than a
	// wider window that meaningfully helps someone guessing.
	TOTPSkew = 1
)

// b32 is the unpadded base32 that authenticator apps expect in a secret.
var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh 20-byte secret, base32-encoded. Twenty bytes
// is what RFC 4226 recommends and what every app handles.
func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// TOTPCode computes the code for a given moment.
func TOTPCode(secret string, at time.Time) (string, error) {
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", fmt.Errorf("secret is not valid base32: %w", err)
	}
	counter := uint64(at.Unix()) / uint64(TOTPPeriod.Seconds())
	return hotp(key, counter), nil
}

// VerifyTOTP reports whether code is valid for this secret around `at`,
// allowing TOTPSkew steps of drift either way.
//
// The comparison is constant-time, and every candidate step is evaluated
// rather than returning on the first match, so the time taken does not reveal
// which step matched.
func VerifyTOTP(secret, code string, at time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != TOTPDigits {
		return false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return false
	}
	counter := uint64(at.Unix()) / uint64(TOTPPeriod.Seconds())

	var matched int
	for skew := -TOTPSkew; skew <= TOTPSkew; skew++ {
		c := counter
		if skew < 0 {
			if c < uint64(-skew) {
				continue
			}
			c -= uint64(-skew)
		} else {
			c += uint64(skew)
		}
		matched |= subtle.ConstantTimeCompare([]byte(hotp(key, c)), []byte(code))
	}
	return matched == 1
}

// hotp is RFC 4226's truncation of an HMAC-SHA1 over the counter.
func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", TOTPDigits, value%pow10(TOTPDigits))
}

func pow10(n int) uint32 {
	out := uint32(1)
	for i := 0; i < n; i++ {
		out *= 10
	}
	return out
}

// TOTPURI builds the otpauth:// URI an authenticator app scans. The issuer
// appears both in the label and as a parameter, which is what apps expect in
// order to group and name the entry sensibly.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{
		"secret": {secret},
		"issuer": {issuer},
		"digits": {fmt.Sprint(TOTPDigits)},
		"period": {fmt.Sprint(int(TOTPPeriod.Seconds()))},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

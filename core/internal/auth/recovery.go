package auth

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// RecoveryCodeCount is how many single-use codes an enrolment produces.
// Enough to survive a few uses without becoming a list nobody stores safely.
const RecoveryCodeCount = 10

// NewRecoveryCodes returns fresh codes and their storable hashes.
//
// Losing a phone must not mean losing the account, and a support path that
// involves an operator disabling MFA for someone is a social-engineering
// target. These are the escape hatch instead.
//
// The alphabet omits characters that are misread when copied off a screen
// (0/O, 1/I/l), because these get written down by hand.
func NewRecoveryCodes() (codes []string, hashes []string, err error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	for i := 0; i < RecoveryCodeCount; i++ {
		var b strings.Builder
		for group := 0; group < 2; group++ {
			if group > 0 {
				b.WriteByte('-')
			}
			for j := 0; j < 5; j++ {
				n, err := randIndex(len(alphabet))
				if err != nil {
					return nil, nil, err
				}
				b.WriteByte(alphabet[n])
			}
		}
		code := b.String()
		codes = append(codes, code)
		hashes = append(hashes, HashSecret(NormalizeRecoveryCode(code)))
	}
	return codes, hashes, nil
}

// NormalizeRecoveryCode makes matching forgiving about how it was typed:
// case, spaces and the grouping dash carry no meaning.
func NormalizeRecoveryCode(code string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(code)))
}

// randIndex returns a uniform index without modulo bias.
func randIndex(n int) (int, error) {
	if n <= 0 || n > 256 {
		return 0, fmt.Errorf("alphabet size %d out of range", n)
	}
	// Reject the tail that would skew the distribution.
	limit := 256 - (256 % n)
	var b [1]byte
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		if int(b[0]) < limit {
			return int(b[0]) % n, nil
		}
	}
}

// NumericCodeDigits is the length of a mailed one-time code. Six is what
// people expect to retype; the attempt cap on the challenge is what keeps it
// from being guessable, not the length.
const NumericCodeDigits = 6

// NewNumericCode returns a uniformly random numeric code.
func NewNumericCode() (string, error) {
	var b strings.Builder
	for i := 0; i < NumericCodeDigits; i++ {
		n, err := randIndex(10)
		if err != nil {
			return "", err
		}
		b.WriteByte(byte('0' + n))
	}
	return b.String(), nil
}

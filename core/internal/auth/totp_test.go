package auth

import (
	"strings"
	"testing"
	"time"
)

// RFC 6238's own test vectors, Appendix B. The SHA-1 rows use the ASCII secret
// "12345678901234567890"; the RFC prints 8-digit codes, so the 6-digit code
// this package produces is the last six.
//
// Cross-verifying against the specification's published values is the reason
// this is implemented here rather than taken as a dependency: an authenticator
// that silently accepts a wrong code is not something to take on trust.
func TestRFC6238Vectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	cases := []struct {
		unix int64
		want string // last 6 digits of the RFC's 8-digit value
	}{
		{59, "287082"},          // RFC: 94287082
		{1111111109, "081804"},  // RFC: 07081804
		{1111111111, "050471"},  // RFC: 14050471
		{1234567890, "005924"},  // RFC: 89005924
		{2000000000, "279037"},  // RFC: 69279037
		{20000000000, "353130"}, // RFC: 65353130
	}
	for _, tc := range cases {
		got, err := TOTPCode(secret, time.Unix(tc.unix, 0))
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("t=%d: got %s, want %s", tc.unix, got, tc.want)
		}
	}
}

func TestVerifyAcceptsTheCurrentCode(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := TOTPCode(secret, now)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyTOTP(secret, code, now) {
		t.Error("the current code was rejected")
	}
}

// A phone's clock drifts; one step either way is accepted, and no more.
func TestVerifyToleratesOneStepOfDrift(t *testing.T) {
	secret, _ := NewTOTPSecret()
	now := time.Now()

	for _, skew := range []time.Duration{-TOTPPeriod, 0, TOTPPeriod} {
		code, _ := TOTPCode(secret, now.Add(skew))
		if !VerifyTOTP(secret, code, now) {
			t.Errorf("a code %v away was rejected", skew)
		}
	}
	for _, skew := range []time.Duration{-3 * TOTPPeriod, 3 * TOTPPeriod} {
		code, _ := TOTPCode(secret, now.Add(skew))
		if VerifyTOTP(secret, code, now) {
			t.Errorf("a code %v away was accepted; the window is too wide", skew)
		}
	}
}

func TestVerifyRejectsWrongCodes(t *testing.T) {
	secret, _ := NewTOTPSecret()
	now := time.Now()
	correct, _ := TOTPCode(secret, now)

	for _, bad := range []string{"", "000000", "12345", "1234567", "abcdef", correct + "0"} {
		if bad == correct {
			continue
		}
		if VerifyTOTP(secret, bad, now) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// Another enrolment's code must never work.
func TestCodesDoNotCrossSecrets(t *testing.T) {
	a, _ := NewTOTPSecret()
	b, _ := NewTOTPSecret()
	now := time.Now()
	codeA, _ := TOTPCode(a, now)
	if VerifyTOTP(b, codeA, now) {
		t.Error("one secret's code verified against another")
	}
}

func TestMalformedSecretFailsClosed(t *testing.T) {
	now := time.Now()
	for _, s := range []string{"", "not base32!", "1", "@@@@"} {
		if VerifyTOTP(s, "123456", now) {
			t.Errorf("a malformed secret accepted a code: %q", s)
		}
		if _, err := TOTPCode(s, now); err == nil && s != "" {
			t.Errorf("expected an error for secret %q", s)
		}
	}
}

// Secrets must be unguessable and in the form apps accept.
func TestNewSecretShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := NewTOTPSecret()
		if err != nil {
			t.Fatal(err)
		}
		if seen[s] {
			t.Fatal("a secret repeated")
		}
		seen[s] = true
		// 20 bytes in unpadded base32.
		if len(s) != 32 {
			t.Fatalf("unexpected length %d: %q", len(s), s)
		}
		if strings.Contains(s, "=") {
			t.Errorf("padding would confuse some apps: %q", s)
		}
		if _, err := b32.DecodeString(s); err != nil {
			t.Errorf("secret does not decode: %v", err)
		}
	}
}

func TestURIIsScannable(t *testing.T) {
	uri := TOTPURI("Chiral", "mai@example.com", "JBSWY3DPEHPK3PXP")
	for _, want := range []string{
		"otpauth://totp/",
		"secret=JBSWY3DPEHPK3PXP",
		"issuer=Chiral",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("URI missing %q: %s", want, uri)
		}
	}
	// The label carries issuer:account, which is how apps name the entry.
	if !strings.Contains(uri, "Chiral%3Amai@example.com") &&
		!strings.Contains(uri, "Chiral:mai@example.com") {
		t.Errorf("label is not issuer:account: %s", uri)
	}
}

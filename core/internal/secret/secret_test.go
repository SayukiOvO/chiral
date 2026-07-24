package secret

import (
	"errors"
	"strings"
	"testing"
)

const testKey = "a-sufficiently-long-test-key-0123456789"

func TestRoundTrip(t *testing.T) {
	b, err := NewBox(testKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := b.Seal("reality.private", "PRIVATE-KEY-VALUE")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "PRIVATE-KEY-VALUE") {
		t.Fatal("plaintext is visible in the sealed value")
	}
	got, err := b.Open("reality.private", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got != "PRIVATE-KEY-VALUE" {
		t.Errorf("got %q", got)
	}
}

func TestSealIsNondeterministic(t *testing.T) {
	b, _ := NewBox(testKey)
	a, _ := b.Seal("v", "same")
	c, _ := b.Seal("v", "same")
	if a == c {
		t.Error("sealing the same value twice produced identical ciphertext (nonce reuse)")
	}
}

// A ciphertext must not be movable to a different variable by someone with
// write access to the database.
func TestNameIsBoundIntoCiphertext(t *testing.T) {
	b, _ := NewBox(testKey)
	sealed, _ := b.Seal("reality.private", "V")
	if _, err := b.Open("other.private", sealed); err == nil {
		t.Fatal("opening under a different variable name should fail")
	}
}

func TestTamperingIsDetected(t *testing.T) {
	b, _ := NewBox(testKey)
	sealed, _ := b.Seal("v", "value")
	// Flip one character in the middle of the base64 payload, keeping the
	// prefix, key id and length intact.
	body := []byte(sealed)
	i := len(body) - 4
	if body[i] == 'A' {
		body[i] = 'B'
	} else {
		body[i] = 'A'
	}
	if _, err := b.Open("v", string(body)); err == nil {
		t.Fatal("tampered ciphertext should not open")
	}
}

func TestWrongKeyGivesClearError(t *testing.T) {
	b1, _ := NewBox(testKey)
	b2, _ := NewBox("a-completely-different-key-9876543210")
	sealed, _ := b1.Seal("v", "value")
	_, err := b2.Open("v", sealed)
	if err == nil {
		t.Fatal("expected an error opening with the wrong key")
	}
	if !strings.Contains(err.Error(), "different CHIRAL_SECRET_KEY") {
		t.Errorf("error should point at the key mismatch, got: %v", err)
	}
}

func TestDisabledBoxStoresPlain(t *testing.T) {
	b, err := NewBox("")
	if err != nil {
		t.Fatal(err)
	}
	if b.Enabled() {
		t.Fatal("box with no key should not report enabled")
	}
	sealed, _ := b.Seal("v", "value")
	if IsEncrypted(sealed) {
		t.Error("value should not be marked encrypted")
	}
	got, err := b.Open("v", sealed)
	if err != nil || got != "value" {
		t.Errorf("got %q, %v", got, err)
	}
}

// Turning encryption on later must not break values already stored in clear.
func TestEnablingEncryptionLaterKeepsReadingPlainValues(t *testing.T) {
	plain, _ := NewBox("")
	stored, _ := plain.Seal("v", "old-value")

	encrypted, _ := NewBox(testKey)
	got, err := encrypted.Open("v", stored)
	if err != nil {
		t.Fatal(err)
	}
	if got != "old-value" {
		t.Errorf("got %q", got)
	}
}

func TestOpeningCiphertextWithoutKeyIsExplicit(t *testing.T) {
	b, _ := NewBox(testKey)
	sealed, _ := b.Seal("v", "value")

	none, _ := NewBox("")
	_, err := none.Open("v", sealed)
	if !errors.Is(err, ErrNoKey) {
		t.Errorf("expected ErrNoKey, got %v", err)
	}
}

func TestShortKeyRejected(t *testing.T) {
	if _, err := NewBox("tooshort"); err == nil {
		t.Fatal("expected a short key to be rejected")
	}
}

func TestBareLegacyValueIsReadable(t *testing.T) {
	b, _ := NewBox(testKey)
	got, err := b.Open("v", "bare-value-no-prefix")
	if err != nil || got != "bare-value-no-prefix" {
		t.Errorf("got %q, %v", got, err)
	}
}

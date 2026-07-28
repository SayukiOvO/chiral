package template

import (
	"crypto/ecdh"
	"crypto/mlkem"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
)

// Generator produces one variable group. Xray's own key tooling is inherently
// multi-component (a keypair's private half goes in the server config, the
// public half in the client's), which is exactly why the pool stores groups
// rather than single values: both sides reference components of the SAME
// generated group, so they cannot drift apart.
//
// This matters more than it looks: `xray -test` accepts a well-formed but
// MISMATCHED keypair without complaint — the failure only shows up as a
// silent handshake failure at runtime. Deriving both halves from one group is
// the only mechanism that rules it out.
type Generator string

const (
	GenUUID     Generator = "uuid"     // VLESS id
	GenX25519   Generator = "x25519"   // REALITY keypair
	GenShortID  Generator = "short_id" // REALITY shortId
	GenPassword Generator = "password" // trojan / shadowsocks style secret
	GenMLKEM768 Generator = "mlkem768" // VLESS Encryption, post-quantum KEX
	GenMLDSA65  Generator = "mldsa65"  // REALITY post-quantum signature
)

// NeedsXray reports whether a generator can only run with the Xray binary
// available. ML-DSA-65 has no standard-library implementation, so the panel
// delegates its derivation to Xray (see Xray.GenerateMLDSA65).
func NeedsXray(g Generator) bool { return g == GenMLDSA65 }

// Group is one generated variable group: named components plus which of them
// are secret (server-only).
type Group struct {
	Components map[string]string
	Secret     []string
}

// SecretSet reports the component names that must never reach a client.
func (g Group) SecretSet() map[string]struct{} {
	out := make(map[string]struct{}, len(g.Secret))
	for _, s := range g.Secret {
		out[s] = struct{}{}
	}
	return out
}

// ComponentNames returns component names, sorted.
func (g Group) ComponentNames() []string {
	out := make([]string, 0, len(g.Components))
	for k := range g.Components {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// b64 is the encoding Xray uses for every key it prints or parses
// (`base64.RawURLEncoding`, per `xray help x25519`).
var b64 = base64.RawURLEncoding

// randRead fills b with cryptographic randomness.
func randRead(b []byte) error {
	_, err := rand.Read(b)
	return err
}

// Generate produces a fresh group for the given generator.
func Generate(g Generator) (Group, error) {
	switch g {
	case GenUUID:
		return genUUID()
	case GenX25519:
		return genX25519()
	case GenShortID:
		return genShortID()
	case GenPassword:
		return genPassword()
	case GenMLKEM768:
		return genMLKEM768()
	case GenMLDSA65:
		return Group{}, fmt.Errorf("generator %q requires the Xray binary; use Xray.GenerateMLDSA65", g)
	default:
		return Group{}, fmt.Errorf("unknown generator %q", g)
	}
}

// Generators lists the available generators, for the UI's picker. Those for
// which NeedsXray reports true are only usable when a binary is configured.
func Generators() []Generator {
	return []Generator{GenUUID, GenX25519, GenShortID, GenPassword, GenMLKEM768, GenMLDSA65}
}

func genUUID() (Group, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Group{}, err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	s := fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	// A UUID is a credential, but it is the one the client must present, so it
	// is not secret from the client's point of view.
	return Group{Components: map[string]string{"": s}}, nil
}

// genX25519 mirrors `xray x25519`: a random 32-byte scalar, with the public
// half derived via X25519. Both halves are printed base64.RawURLEncoding.
func genX25519() (Group, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return Group{}, err
	}
	clampX25519(seed)
	priv, err := ecdh.X25519().NewPrivateKey(seed)
	if err != nil {
		return Group{}, fmt.Errorf("derive x25519 key: %w", err)
	}
	return Group{
		Components: map[string]string{
			"private": b64.EncodeToString(seed),
			"public":  b64.EncodeToString(priv.PublicKey().Bytes()),
		},
		Secret: []string{"private"},
	}, nil
}

// clampX25519 puts a scalar into the canonical form of RFC 7748 §5.
//
// This is not a detail. Go's ecdh clamps internally when it derives the public
// half, so an unclamped seed still yields a CORRECT public key — the two halves
// we publish agree with each other, every self-consistency test passes, and
// `xray -test` is perfectly happy. But what we store as the private key is then
// a scalar Xray's REALITY server does not treat the way we assumed, and the
// handshake fails at runtime with the client reporting "received real
// certificate": the server does not recognise it, and falls back to proxying
// the genuine target site. Nothing anywhere says "wrong key".
//
// Measured, not reasoned: a live REALITY inbound rejected every client until
// the stored private key was clamped, and accepted them immediately after. See
// TestX25519MatchesTheXrayBinary, which asks the binary rather than asking
// ourselves — the failure mode here is precisely that our own implementation
// agrees with itself.
func clampX25519(k []byte) {
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
}

// X25519Public derives the public half from an existing private key, so an
// operator can import a keypair they already deployed.
func X25519Public(privateB64 string) (string, error) {
	seed, err := b64.DecodeString(privateB64)
	if err != nil {
		return "", fmt.Errorf("private key must be base64.RawURLEncoding: %w", err)
	}
	if len(seed) != 32 {
		return "", fmt.Errorf("private key must be 32 bytes, got %d", len(seed))
	}
	// An imported key may well be unclamped; clamp before deriving so the
	// public half we return belongs to the scalar that will actually be used.
	clamped := make([]byte, 32)
	copy(clamped, seed)
	clampX25519(clamped)
	priv, err := ecdh.X25519().NewPrivateKey(clamped)
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(priv.PublicKey().Bytes()), nil
}

// genShortID mirrors REALITY shortIds: an even-length hex string, 8 hex chars
// (4 bytes) being the common choice.
func genShortID() (Group, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return Group{}, err
	}
	return Group{Components: map[string]string{"": hex.EncodeToString(b)}}, nil
}

func genPassword() (Group, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return Group{}, err
	}
	return Group{Components: map[string]string{"": b64.EncodeToString(b)}}, nil
}

// genMLKEM768 mirrors `xray mlkem768`: a 64-byte seed and the encapsulation
// key that Xray labels "Client".
func genMLKEM768() (Group, error) {
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		return Group{}, err
	}
	return mlkemGroup(dk), nil
}

// MLKEM768FromSeed rebuilds the group from an existing seed, both for import
// and for cross-checking this implementation against the xray binary.
func MLKEM768FromSeed(seedB64 string) (Group, error) {
	seed, err := b64.DecodeString(seedB64)
	if err != nil {
		return Group{}, fmt.Errorf("seed must be base64.RawURLEncoding: %w", err)
	}
	dk, err := mlkem.NewDecapsulationKey768(seed)
	if err != nil {
		return Group{}, err
	}
	return mlkemGroup(dk), nil
}

func mlkemGroup(dk *mlkem.DecapsulationKey768) Group {
	// seed and client (the encapsulation key) are verified byte-for-byte
	// against `xray mlkem768 -i <seed>` in the tests.
	//
	// Xray also prints a third value, "Hash32", whose derivation we could not
	// reproduce (it is not a plain SHA-256/SHA3-256/SHA-512-256 of the seed or
	// the encapsulation key). We deliberately do not emit a guessed value:
	// `xray -test` accepts wrong-but-well-formed key material silently, so an
	// unverified component would fail only at runtime. Add it when its
	// derivation is confirmed.
	return Group{
		Components: map[string]string{
			"seed":   b64.EncodeToString(dk.Bytes()),
			"client": b64.EncodeToString(dk.EncapsulationKey().Bytes()),
		},
		Secret: []string{"seed"},
	}
}

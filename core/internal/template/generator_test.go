package template

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestGenerateUUIDShape(t *testing.T) {
	g, err := Generate(GenUUID)
	if err != nil {
		t.Fatal(err)
	}
	uuid := g.Components[""]
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(uuid) {
		t.Errorf("not a v4 UUID: %q", uuid)
	}
}

func TestGenerateUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		g, err := Generate(GenUUID)
		if err != nil {
			t.Fatal(err)
		}
		if seen[g.Components[""]] {
			t.Fatal("duplicate UUID generated")
		}
		seen[g.Components[""]] = true
	}
}

func TestX25519GroupShape(t *testing.T) {
	g, err := Generate(GenX25519)
	if err != nil {
		t.Fatal(err)
	}
	if g.Components["private"] == "" || g.Components["public"] == "" {
		t.Fatalf("missing components: %v", g.Components)
	}
	if g.Components["private"] == g.Components["public"] {
		t.Fatal("private and public halves are identical")
	}
	if len(g.Secret) != 1 || g.Secret[0] != "private" {
		t.Errorf("private half must be marked secret, got %v", g.Secret)
	}
	// 32 bytes in base64.RawURLEncoding.
	if len(g.Components["private"]) != 43 || len(g.Components["public"]) != 43 {
		t.Errorf("unexpected key lengths: %v", g.Components)
	}
}

func TestX25519PublicIsDeterministic(t *testing.T) {
	g, err := Generate(GenX25519)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := X25519Public(g.Components["private"])
	if err != nil {
		t.Fatal(err)
	}
	if pub != g.Components["public"] {
		t.Errorf("re-derived public %q != generated %q", pub, g.Components["public"])
	}
}

func TestMLKEM768FromSeedIsDeterministic(t *testing.T) {
	g, err := Generate(GenMLKEM768)
	if err != nil {
		t.Fatal(err)
	}
	again, err := MLKEM768FromSeed(g.Components["seed"])
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"seed", "client"} {
		if again.Components[k] != g.Components[k] {
			t.Errorf("component %q not reproducible from seed", k)
		}
	}
	if len(g.Secret) != 1 || g.Secret[0] != "seed" {
		t.Errorf("seed must be marked secret, got %v", g.Secret)
	}
}

func TestShortIDIsEvenLengthHex(t *testing.T) {
	g, err := Generate(GenShortID)
	if err != nil {
		t.Fatal(err)
	}
	s := g.Components[""]
	if len(s)%2 != 0 || !regexp.MustCompile(`^[0-9a-f]+$`).MatchString(s) {
		t.Errorf("REALITY shortId must be even-length hex, got %q", s)
	}
}

func TestUnknownGenerator(t *testing.T) {
	if _, err := Generate("nope"); err == nil {
		t.Fatal("expected an error for an unknown generator")
	}
}

// --- cross-verification against the real Xray binary ---
//
// `xray -test` accepts a well-formed but mismatched keypair, so a subtle
// difference between our derivation and Xray's would not surface as a config
// error — it would surface as nodes that silently fail to handshake. These
// tests pin our output to the binary's when one is available.

func xrayBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("CHIRAL_XRAY_BIN"); p != "" {
		return p
	}
	p, err := exec.LookPath("xray")
	if err != nil {
		t.Skip("xray binary not found; set CHIRAL_XRAY_BIN to cross-verify key derivation")
	}
	return p
}

func TestX25519MatchesXrayDerivation(t *testing.T) {
	bin := xrayBin(t)
	g, err := Generate(GenX25519)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "x25519", "-i", g.Components["private"]).CombinedOutput()
	if err != nil {
		t.Fatalf("xray x25519 -i failed: %v\n%s", err, out)
	}
	kv := parseKV(string(out))
	// Xray labels the public half "Password (PublicKey)" in current builds and
	// "PublicKey" in older ones; accept either.
	want := kv["Password (PublicKey)"]
	if want == "" {
		want = kv["PublicKey"]
	}
	if want == "" {
		t.Fatalf("could not find the public half in xray output:\n%s", out)
	}
	if g.Components["public"] != want {
		t.Errorf("our public half %q != xray's %q — server and client would not match",
			g.Components["public"], want)
	}
}

func TestMLKEM768MatchesXrayDerivation(t *testing.T) {
	bin := xrayBin(t)
	g, err := Generate(GenMLKEM768)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "mlkem768", "-i", g.Components["seed"]).CombinedOutput()
	if err != nil {
		t.Skipf("this xray build does not support `mlkem768 -i`: %v", err)
	}
	kv := parseKV(string(out))
	if got, want := g.Components["client"], kv["Client"]; want != "" && got != want {
		t.Errorf("encapsulation key mismatch:\n ours %s\nxray %s", got, want)
	}
	// Xray's third value ("Hash32") is intentionally not emitted — see
	// mlkemGroup. Assert we don't start emitting an unverified component.
	if _, present := g.Components["hash32"]; present {
		t.Error("hash32 is emitted but its derivation is unverified against xray")
	}
}

// The cross-check CLAUDE.md §7 demands, and the one that was missing.
//
// Every other x25519 test here asks our own implementation to confirm our own
// implementation: generate a pair, re-derive the public half, compare. Both
// paths go through Go's ecdh, which clamps the scalar internally, so they agree
// with each other no matter what we stored — and for a while what we stored was
// an unclamped seed. The public half was correct. `xray -test` accepted the
// config. A live REALITY server then rejected every client and silently proxied
// them to the real target site, which is the failure mode with no error message
// attached to it.
//
// So this one asks the binary. `xray x25519 -i <private>` echoes the private
// key back in canonical form; if ours differs, ours is not the scalar the
// kernel will use.
func TestX25519MatchesTheXrayBinary(t *testing.T) {
	bin := os.Getenv("CHIRAL_XRAY_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("xray"); err != nil {
			t.Skip("no xray binary; cannot cross-validate key derivation")
		}
	}
	for i := 0; i < 8; i++ {
		g, err := Generate(GenX25519)
		if err != nil {
			t.Fatal(err)
		}
		priv, pub := g.Components["private"], g.Components["public"]

		out, err := exec.Command(bin, "x25519", "-i", priv).Output()
		if err != nil {
			t.Fatalf("xray x25519 -i: %v", err)
		}
		kv := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}

		// The private key must come back byte-identical. Anything else means
		// the kernel normalised it, i.e. it will use a different scalar than
		// the one we handed it — and the public key we published belongs to
		// that other scalar, so no client can complete a handshake.
		gotPriv := firstOf(kv, "PrivateKey", "Private key")
		if gotPriv == "" {
			t.Fatalf("could not parse a private key from:\n%s", out)
		}
		if gotPriv != priv {
			t.Fatalf("xray normalised our private key: we stored %q, it uses %q.\n"+
				"A REALITY server will reject every client and silently proxy them "+
				"to the real target site.", priv, gotPriv)
		}

		gotPub := firstOf(kv, "Password (PublicKey)", "Password", "PublicKey", "Public key")
		if gotPub == "" {
			t.Fatalf("could not parse a public key from:\n%s", out)
		}
		if gotPub != pub {
			t.Fatalf("public half disagrees with the binary: ours %q, xray %q", pub, gotPub)
		}
	}
}

// An imported, unclamped key must still yield the public half of the scalar
// that will actually be used — otherwise importing an existing keypair produces
// a config that cannot handshake.
func TestX25519PublicClampsWhatItIsGiven(t *testing.T) {
	bin := os.Getenv("CHIRAL_XRAY_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("xray"); err != nil {
			t.Skip("no xray binary")
		}
	}
	// A deliberately unclamped scalar: low bits set, top bits wrong.
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i * 7)
	}
	raw[0] |= 0x07
	raw[31] |= 0x80
	privB64 := b64.EncodeToString(raw)

	pub, err := X25519Public(privB64)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "x25519", "-i", privB64).Output()
	if err != nil {
		t.Fatalf("xray x25519 -i: %v", err)
	}
	kv := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	want := firstOf(kv, "Password (PublicKey)", "Password", "PublicKey", "Public key")
	if pub != want {
		t.Fatalf("X25519Public(%q) = %q, xray says %q", privB64, pub, want)
	}
}

func firstOf(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := kv[k]; v != "" {
			return v
		}
	}
	return ""
}

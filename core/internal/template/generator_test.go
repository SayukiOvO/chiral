package template

import (
	"os"
	"os/exec"
	"regexp"
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

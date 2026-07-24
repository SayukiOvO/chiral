package template

import (
	"context"
	"strings"
	"testing"
)

func testXray(t *testing.T) Xray {
	t.Helper()
	return Xray{Bin: xrayBin(t)}
}

func TestXrayUnconfiguredFailsClearly(t *testing.T) {
	var x Xray
	if x.Available() {
		t.Fatal("an unconfigured Xray should not report available")
	}
	if _, err := x.GenerateMLDSA65(); err == nil {
		t.Fatal("expected an error with no binary configured")
	}
	err := x.TestConfig(context.Background(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "CHIRAL_XRAY_BIN") {
		t.Errorf("error should say how to configure the binary, got: %v", err)
	}
}

func TestGenerateMLDSA65NotAvailableWithoutXray(t *testing.T) {
	// The plain generator path must refuse rather than silently produce
	// something wrong.
	if _, err := Generate(GenMLDSA65); err == nil {
		t.Fatal("Generate(mldsa65) should refuse and point at the Xray method")
	}
	if !NeedsXray(GenMLDSA65) || NeedsXray(GenX25519) {
		t.Error("NeedsXray misreports which generators need the binary")
	}
}

func TestMLDSA65Derivation(t *testing.T) {
	x := testXray(t)
	g, err := x.GenerateMLDSA65()
	if err != nil {
		t.Fatal(err)
	}
	if g.Components["seed"] == "" || g.Components["verify"] == "" {
		t.Fatalf("missing components: %v", g.ComponentNames())
	}
	if len(g.Secret) != 1 || g.Secret[0] != "seed" {
		t.Errorf("seed must be secret, got %v", g.Secret)
	}
	// Reproducible: the same seed must yield the same verify half, otherwise
	// a stored variable would silently stop matching the deployed config.
	again, err := x.MLDSA65FromSeed(g.Components["seed"])
	if err != nil {
		t.Fatal(err)
	}
	if again.Components["verify"] != g.Components["verify"] {
		t.Error("verify half is not reproducible from the seed")
	}
}

func TestMLDSA65RejectsBadSeed(t *testing.T) {
	x := testXray(t)
	if _, err := x.MLDSA65FromSeed("!!!not base64!!!"); err == nil {
		t.Fatal("expected a malformed seed to be rejected before exec")
	}
}

func TestTestConfigAcceptsValidAndRejectsInvalid(t *testing.T) {
	x := testXray(t)
	good := []byte(`{"log":{"loglevel":"warning"},
	  "inbounds":[{"tag":"in","listen":"127.0.0.1","port":10800,"protocol":"socks","settings":{"udp":true}}],
	  "outbounds":[{"protocol":"freedom","tag":"direct"}]}`)
	if err := x.TestConfig(context.Background(), good); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	// Malformed value inside REALITY: Xray does catch this class.
	bad := []byte(`{"log":{"loglevel":"warning"},
	  "inbounds":[{"tag":"in","listen":"0.0.0.0","port":443,"protocol":"vless",
	    "settings":{"clients":[],"decryption":"none"},
	    "streamSettings":{"network":"tcp","security":"reality","realitySettings":{
	      "target":"example.com:443","serverNames":["example.com"],
	      "privateKey":"not-a-valid-key","shortIds":["aa"]}}}],
	  "outbounds":[{"protocol":"freedom"}]}`)
	err := x.TestConfig(context.Background(), bad)
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	if !strings.Contains(err.Error(), "xray -test rejected") {
		t.Errorf("error should carry Xray's diagnostic, got: %v", err)
	}
}

func TestTestConfigRejectsMalformedJSON(t *testing.T) {
	x := testXray(t)
	if err := x.TestConfig(context.Background(), []byte(`{"inbounds":`)); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}

func TestXrayVersion(t *testing.T) {
	x := testXray(t)
	if v := x.Version(); v == "" {
		t.Error("expected a version string from the configured binary")
	}
}

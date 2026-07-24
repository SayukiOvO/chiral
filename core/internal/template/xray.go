package template

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Xray wraps the Xray-core binary the panel keeps for two jobs it cannot do
// itself: validating a rendered config before it is pushed, and deriving key
// material for algorithms Go's standard library does not implement.
//
// The panel needs the binary anyway for `-test`, so shelling out for
// ML-DSA-65 costs nothing extra and guarantees the derivation matches
// whatever Xray build is deployed — which matters, because `xray -test`
// cannot detect a mismatched keypair (see docs/template-system.md §6).
type Xray struct {
	// Bin is the binary path or a name resolved via PATH. Empty disables the
	// features that need it, with a clear error rather than a panic.
	Bin string
}

const xrayTimeout = 20 * time.Second

// Available reports whether a usable binary is configured.
func (x Xray) Available() bool {
	if x.Bin == "" {
		return false
	}
	_, err := exec.LookPath(x.Bin)
	return err == nil
}

func (x Xray) require() error {
	if x.Bin == "" {
		return fmt.Errorf("no Xray binary configured; set CHIRAL_XRAY_BIN")
	}
	if _, err := exec.LookPath(x.Bin); err != nil {
		return fmt.Errorf("Xray binary %q not found: %w", x.Bin, err)
	}
	return nil
}

// Version returns the binary's version string, or "" if unavailable.
func (x Xray) Version() string {
	if !x.Available() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), xrayTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, x.Bin, "version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	if f := strings.Fields(line); len(f) >= 2 {
		return f[1]
	}
	return ""
}

// TestConfig runs `xray -test` over a rendered config. A non-nil error
// contains Xray's own diagnostic, which is what the operator needs to see.
//
// Its limits are documented in docs/template-system.md §6: it catches syntax
// errors, malformed values and missing required fields, but NOT typos in
// optional fields, and NOT a well-formed keypair that simply does not match.
func (x Xray) TestConfig(ctx context.Context, configJSON []byte) error {
	if err := x.require(); err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "chiral-test-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	// Xray infers the format from the file extension unless told otherwise;
	// we pass -format json explicitly and do not rely on the name.
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, configJSON, 0o600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, xrayTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, x.Bin, "-test", "-c", path, "-format", "json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("xray -test rejected the config: %s", xrayDiagnostic(string(out)))
	}
	return nil
}

// xrayDiagnostic pulls the useful line out of Xray's chatty output.
func xrayDiagnostic(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if strings.HasPrefix(l, "Failed to start:") || strings.Contains(l, "infra/conf") {
			return l
		}
	}
	if len(lines) > 0 {
		return lines[len(lines)-1]
	}
	return "(no output)"
}

// GenerateMLDSA65 produces a REALITY post-quantum signature group. The seed
// comes from our own CSPRNG; only the derivation is delegated to Xray, so the
// value is reproducible with `xray mldsa65 -i <seed>`.
func (x Xray) GenerateMLDSA65() (Group, error) {
	if err := x.require(); err != nil {
		return Group{}, fmt.Errorf("ML-DSA-65 needs the Xray binary: %w", err)
	}
	seed := make([]byte, 32)
	if err := randRead(seed); err != nil {
		return Group{}, err
	}
	return x.MLDSA65FromSeed(b64.EncodeToString(seed))
}

// MLDSA65FromSeed derives the verify half for an existing seed.
func (x Xray) MLDSA65FromSeed(seedB64 string) (Group, error) {
	if err := x.require(); err != nil {
		return Group{}, fmt.Errorf("ML-DSA-65 needs the Xray binary: %w", err)
	}
	if _, err := base64.RawURLEncoding.DecodeString(seedB64); err != nil {
		return Group{}, fmt.Errorf("seed must be base64.RawURLEncoding: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), xrayTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, x.Bin, "mldsa65", "-i", seedB64).Output()
	if err != nil {
		return Group{}, fmt.Errorf("xray mldsa65 failed: %w", err)
	}
	kv := parseKV(string(out))
	verify := kv["Verify"]
	if verify == "" {
		return Group{}, fmt.Errorf("could not parse a Verify value from `xray mldsa65` output")
	}
	// Trust but verify: the seed we asked for must be the seed it used.
	if got := kv["Seed"]; got != "" && got != seedB64 {
		return Group{}, fmt.Errorf("xray returned a different seed than requested")
	}
	return Group{
		Components: map[string]string{"seed": seedB64, "verify": verify},
		Secret:     []string{"seed"},
	}, nil
}

// parseKV reads the "Label: value" lines Xray's key commands print.
func parseKV(out string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m
}

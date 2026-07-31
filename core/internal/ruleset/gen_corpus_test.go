package ruleset

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Writes a full Clash config per upstream preset so a real kernel can be asked
// whether it loads. Structural assertions written by the same person who wrote
// the renderer only prove self-consistency; mihomo is the authority on what it
// accepts. Off by default — it produces files rather than checking anything.
func TestWriteRenderedCorpus(t *testing.T) {
	dir := os.Getenv("CHIRAL_ACL4SSR_CORPUS")
	out := os.Getenv("CHIRAL_RENDER_TO")
	if dir == "" || out == "" {
		t.Skip("set CHIRAL_ACL4SSR_CORPUS and CHIRAL_RENDER_TO")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.ini"))
	fleet := []string{"香港 01", "香港 02", "日本 · 东京 01"}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseINI(string(src))
		if err != nil {
			t.Fatalf("%s: %v", filepath.Base(f), err)
		}
		r := Render(cfg, fleet, "https://p.example/sub/T/rules")
		var b strings.Builder
		b.WriteString("mixed-port: 7890\nmode: rule\nlog-level: silent\nproxies:\n")
		for _, n := range fleet {
			fmt.Fprintf(&b, "  - {name: %q, type: socks5, server: 127.0.0.1, port: 1080}\n", n)
		}
		b.WriteString(r.ProxyGroups)
		b.WriteString(r.RuleProviders)
		b.WriteString(r.Rules)
		name := strings.TrimSuffix(filepath.Base(f), ".ini") + ".yaml"
		if err := os.WriteFile(filepath.Join(out, name), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("wrote %d configs to %s", len(files), out)
}

package ruleset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every preset upstream publishes, parsed. A grammar written against one
// sample is a grammar that fits one sample; these 33 files are what operators
// will actually paste in, and they are the only honest corpus for it.
//
// Skipped when the corpus is not present, so the suite stays offline-clean.
func TestEveryUpstreamPresetParses(t *testing.T) {
	dir := os.Getenv("CHIRAL_ACL4SSR_CORPUS")
	if dir == "" {
		t.Skip("set CHIRAL_ACL4SSR_CORPUS to a directory of ACL4SSR .ini files")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.ini"))
	if err != nil || len(files) == 0 {
		t.Skipf("no .ini files in %s", dir)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseINI(string(src))
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if len(cfg.Groups) == 0 || len(cfg.Rules) == 0 {
			t.Errorf("%s: parsed to %d groups and %d rules", filepath.Base(f), len(cfg.Groups), len(cfg.Rules))
		}
		// Every rule must reach a group the config defines, or the client
		// gets a rule pointing at a policy that does not exist and refuses
		// the whole config.
		defined := map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true}
		for _, g := range cfg.Groups {
			defined[g.Name] = true
		}
		for _, r := range cfg.Rules {
			if !defined[r.Group] {
				t.Errorf("%s: rule routed to undefined group %q", filepath.Base(f), r.Group)
			}
		}
		for _, g := range cfg.Groups {
			for _, lit := range g.Literals {
				if !defined[lit] {
					t.Errorf("%s: group %q references undefined %q", filepath.Base(f), g.Name, lit)
				}
			}
		}
	}
	t.Logf("parsed %d presets", len(files))
}

// Rendering every upstream preset against a realistic fleet. The parser test
// above proves the grammar is understood; this proves the output is a
// configuration — no empty groups, no member or rule pointing at a name that
// is not defined. Those two are what a client rejects the whole file over.
func TestEveryUpstreamPresetRenders(t *testing.T) {
	dir := os.Getenv("CHIRAL_ACL4SSR_CORPUS")
	if dir == "" {
		t.Skip("set CHIRAL_ACL4SSR_CORPUS to a directory of ACL4SSR .ini files")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.ini"))
	if len(files) == 0 {
		t.Skipf("no .ini files in %s", dir)
	}
	// Deliberately lopsided: nodes in two regions out of the eight or so a
	// preset names, which is what a small fleet looks like and what makes the
	// region groups drop.
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

		defined := map[string]bool{}
		for _, line := range strings.Split(r.ProxyGroups, "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "- name: "); ok {
				defined[strings.Trim(name, `"`)] = true
			}
		}
		// Every emitted group must have at least one member.
		var current string
		members := map[string]int{}
		for _, line := range strings.Split(r.ProxyGroups, "\n") {
			t2 := strings.TrimSpace(line)
			if name, ok := strings.CutPrefix(t2, "- name: "); ok {
				current = strings.Trim(name, `"`)
			} else if strings.HasPrefix(t2, "- ") && current != "" {
				members[current]++
			}
		}
		for g := range defined {
			if members[g] == 0 {
				t.Errorf("%s: group %q emitted with no members", filepath.Base(f), g)
			}
		}
		// Every rule must target something that exists.
		for _, line := range strings.Split(r.Rules, "\n") {
			t2 := strings.TrimSpace(line)
			if !strings.HasPrefix(t2, "- ") {
				continue
			}
			fields := strings.Split(strings.TrimPrefix(t2, "- "), ",")
			target := strings.Trim(fields[len(fields)-1], `"`)
			if !defined[target] && !isBuiltinPolicy(target) {
				t.Errorf("%s: rule targets undefined %q", filepath.Base(f), target)
			}
		}
		if len(r.Providers) == 0 {
			t.Errorf("%s: rendered no rule providers", filepath.Base(f))
		}
	}
	t.Logf("rendered %d presets against a %d-node fleet", len(files), len(fleet))
}

// The built-in list is a copy of upstream's directory, and a copy drifts. Five
// of these keys were wrong when written from memory: three named files that do
// not exist — a preset an operator selects and which then 404s — and two real
// ones were missing entirely.
func TestBuiltInPresetsMatchUpstream(t *testing.T) {
	dir := os.Getenv("CHIRAL_ACL4SSR_CORPUS")
	if dir == "" {
		t.Skip("set CHIRAL_ACL4SSR_CORPUS to a directory of ACL4SSR .ini files")
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.ini"))
	if err != nil || len(files) == 0 {
		t.Skipf("no .ini files in %s", dir)
	}
	upstream := make(map[string]bool, len(files))
	for _, f := range files {
		upstream[strings.TrimSuffix(filepath.Base(f), ".ini")] = true
	}
	ours := make(map[string]bool)
	for _, p := range Presets() {
		ours[p.Key] = true
		if !upstream[p.Key] {
			t.Errorf("built-in %q does not exist upstream", p.Key)
		}
		if p.Name == "" || p.Name == p.Key {
			t.Errorf("built-in %q has no readable name", p.Key)
		}
		if p.Lists == 0 || p.Groups == 0 {
			t.Errorf("built-in %q claims %d groups and %d lists", p.Key, p.Groups, p.Lists)
		}
	}
	for key := range upstream {
		if !ours[key] {
			t.Errorf("upstream preset %q is missing from the built-ins", key)
		}
	}
}

package clashrule

import (
	"os"
	"testing"
)

// The real ACL4SSR_Online_Mini, byte for byte. A hand-written sample would
// only prove the parser handles what the author of the parser imagined.
const miniINI = "[custom]\n" +
	"ruleset=🎯 全球直连,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/LocalAreaNetwork.list\n" +
	"ruleset=🛑 全球拦截,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/BanAD.list\n" +
	"ruleset=🚀 节点选择,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/ProxyLite.list\n" +
	"ruleset=🎯 全球直连,[]GEOIP,CN\n" +
	"ruleset=🐟 漏网之鱼,[]FINAL\n" +
	"custom_proxy_group=🚀 节点选择`select`[]♻️ 自动选择`[]DIRECT`.*\n" +
	"custom_proxy_group=♻️ 自动选择`url-test`.*`http://www.gstatic.com/generate_204`300,,50\n" +
	"custom_proxy_group=🎯 全球直连`select`[]DIRECT`[]🚀 节点选择`[]♻️ 自动选择\n" +
	"custom_proxy_group=🛑 全球拦截`select`[]REJECT`[]DIRECT\n" +
	"custom_proxy_group=🐟 漏网之鱼`select`[]🚀 节点选择`[]🎯 全球直连`[]♻️ 自动选择`.*\n" +
	"enable_rule_generator=true\n" +
	"overwrite_original_rules=true\n"

func TestParseMini(t *testing.T) {
	cfg, err := ParseINI([]byte(miniINI))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rulesets) != 5 {
		t.Fatalf("rulesets = %d, want 5", len(cfg.Rulesets))
	}
	if len(cfg.Groups) != 5 {
		t.Fatalf("groups = %d, want 5", len(cfg.Groups))
	}

	// A builtin carries its own comma; splitting on every comma would turn
	// []GEOIP,CN into a rule against a group called "CN".
	geo := cfg.Rulesets[3]
	if geo.Group != "🎯 全球直连" || geo.Builtin != "GEOIP,CN" || geo.URL != "" {
		t.Errorf("GEOIP ruleset parsed as %+v", geo)
	}
	if fin := cfg.Rulesets[4]; fin.Builtin != "FINAL" {
		t.Errorf("FINAL ruleset parsed as %+v", fin)
	}
	if first := cfg.Rulesets[0]; first.URL == "" || first.Builtin != "" {
		t.Errorf("list ruleset parsed as %+v", first)
	}
}

func TestParseTestingGroupSplitsOffTimingAndURL(t *testing.T) {
	cfg, err := ParseINI([]byte(miniINI))
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.Groups[1]
	if g.Name != "♻️ 自动选择" || g.Type != "url-test" {
		t.Fatalf("got %+v", g)
	}
	if len(g.Members) != 1 || g.Members[0] != ".*" {
		t.Errorf("members = %q, want [.*] — the probe URL must not become a member", g.Members)
	}
	if g.TestURL != "http://www.gstatic.com/generate_204" {
		t.Errorf("test url = %q", g.TestURL)
	}
	if g.Interval != 300 || g.Tolerance != 50 || g.Timeout != 0 {
		t.Errorf("timing = %d,%d,%d, want 300,0,50", g.Interval, g.Timeout, g.Tolerance)
	}
}

// A select group's members are all of them; nothing may be mistaken for
// timing. The last member here is a regex, and the one before it a group ref.
func TestParseSelectKeepsEveryMember(t *testing.T) {
	cfg, _ := ParseINI([]byte(miniINI))
	g := cfg.Groups[0]
	want := []string{"[]♻️ 自动选择", "[]DIRECT", ".*"}
	if len(g.Members) != len(want) {
		t.Fatalf("members = %q, want %q", g.Members, want)
	}
	for i := range want {
		if g.Members[i] != want[i] {
			t.Fatalf("members = %q, want %q", g.Members, want)
		}
	}
	if g.TestURL != "" {
		t.Errorf("select group picked up a test url: %q", g.TestURL)
	}
}

// Ordering decides where traffic goes: clash takes the first matching rule.
func TestParsePreservesRulesetOrder(t *testing.T) {
	cfg, _ := ParseINI([]byte(miniINI))
	if cfg.Rulesets[len(cfg.Rulesets)-1].Builtin != "FINAL" {
		t.Error("FINAL must stay last; anything after it is unreachable")
	}
	if cfg.Rulesets[0].Group != "🎯 全球直连" {
		t.Error("first ruleset moved")
	}
}

func TestParseRejectsWhatIsNotAConfig(t *testing.T) {
	for name, in := range map[string]string{
		"empty":         "",
		"only comments": "# nothing here\n; nor here\n",
		"no groups":     "ruleset=A,https://example.com/x.list\n",
		"no rulesets":   "custom_proxy_group=A`select`[]DIRECT\n",
		"bad ruleset":   "custom_proxy_group=A`select`[]DIRECT\nruleset=A\n",
		"not a url":     "custom_proxy_group=A`select`[]DIRECT\nruleset=A,ftp://x/y.list\n",
		"group no type": "ruleset=A,[]FINAL\ncustom_proxy_group=A``x\n",
	} {
		if _, err := ParseINI([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Parsing whatever the operator actually points at, when it is on disk.
func TestParseRealConfigIfPresent(t *testing.T) {
	b, err := os.ReadFile("testdata/ACL4SSR_Online_Mini.ini")
	if err != nil {
		t.Skip("no testdata copy; the embedded sample covers the format")
	}
	cfg, err := ParseINI(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rulesets) == 0 || len(cfg.Groups) == 0 {
		t.Fatal("parsed nothing out of the real config")
	}
}

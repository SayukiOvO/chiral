package ruleset

import "testing"

// Verbatim from ACL4SSR_Online.ini, because a parser for someone else's format
// should be tested against their bytes rather than against a tidied version of
// them.
const onlineINI = `[custom]
;不要随意改变关键字，否则会导致出错
;acl4SSR规则-在线更新版

ruleset=🎯 全球直连,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/LocalAreaNetwork.list
ruleset=🛑 全球拦截,https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/BanAD.list
;ruleset=🎯 全球直连,[]GEOIP,LAN
ruleset=🎯 全球直连,[]GEOIP,CN
ruleset=🐟 漏网之鱼,[]FINAL

custom_proxy_group=🚀 节点选择` + "`" + `select` + "`" + `[]♻️ 自动选择` + "`" + `[]DIRECT` + "`" + `.*
custom_proxy_group=♻️ 自动选择` + "`" + `url-test` + "`" + `.*` + "`" + `http://www.gstatic.com/generate_204` + "`" + `300,,50
custom_proxy_group=🛑 全球拦截` + "`" + `select` + "`" + `[]REJECT` + "`" + `[]DIRECT
`

func parse(t *testing.T, src string) Config {
	t.Helper()
	cfg, err := ParseINI(src)
	if err != nil {
		t.Fatalf("ParseINI: %v", err)
	}
	return cfg
}

func TestParsesRulesetsAndGroups(t *testing.T) {
	cfg := parse(t, onlineINI)
	if len(cfg.Rules) != 4 {
		t.Fatalf("rules = %d, want 4 (the commented one must not count)", len(cfg.Rules))
	}
	if len(cfg.Groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(cfg.Groups))
	}
}

// `[]GEOIP,CN` is one rule. Splitting on every comma turns it into a group
// called "[]GEOIP" routed to "CN", which is well-formed nonsense: the client
// accepts the config and routes nothing the way the preset intended.
func TestInlineRuleKeepsItsOwnCommas(t *testing.T) {
	cfg := parse(t, onlineINI)
	var found bool
	for _, r := range cfg.Rules {
		if r.Inline == "GEOIP,CN" {
			found = true
			if r.Group != "🎯 全球直连" {
				t.Errorf("GEOIP,CN routed to %q", r.Group)
			}
		}
	}
	if !found {
		t.Fatalf("did not parse `[]GEOIP,CN` as one inline rule: %+v", cfg.Rules)
	}
}

// subconverter's FINAL is Clash's MATCH. Passing FINAL through would leave the
// catch-all unmatched, so everything not named by an earlier rule falls to the
// client's default instead of to 漏网之鱼.
func TestFinalBecomesMatch(t *testing.T) {
	cfg := parse(t, onlineINI)
	last := cfg.Rules[len(cfg.Rules)-1]
	if last.Inline != "MATCH" {
		t.Fatalf("last rule = %q, want MATCH", last.Inline)
	}
	if last.Group != "🐟 漏网之鱼" {
		t.Fatalf("catch-all routed to %q", last.Group)
	}
}

func TestCommentedDirectivesAreIgnored(t *testing.T) {
	for _, r := range parse(t, onlineINI).Rules {
		if r.Inline == "GEOIP,LAN" {
			t.Fatal("a commented-out ruleset was parsed")
		}
	}
}

func TestGroupMembersSplitLiteralsFromPatterns(t *testing.T) {
	cfg := parse(t, onlineINI)
	g := cfg.Groups[0]
	if g.Name != "🚀 节点选择" || g.Type != GroupSelect {
		t.Fatalf("first group = %+v", g)
	}
	want := []string{"♻️ 自动选择", "DIRECT"}
	if len(g.Literals) != len(want) {
		t.Fatalf("literals = %v, want %v", g.Literals, want)
	}
	for i := range want {
		if g.Literals[i] != want[i] {
			t.Fatalf("literals = %v, want %v", g.Literals, want)
		}
	}
	if g.Match == nil || !g.Match.MatchString("anything") {
		t.Fatalf("`.*` did not become a match-everything pattern")
	}
}

// The trailing `url`timings on a probing group are not members. Treated as
// members they become two proxies named after a URL and a number, which the
// client then lists as selectable and cannot dial.
func TestTestParametersAreNotMembers(t *testing.T) {
	cfg := parse(t, onlineINI)
	var auto Group
	for _, g := range cfg.Groups {
		if g.Type == GroupURLTest {
			auto = g
		}
	}
	if auto.Name == "" {
		t.Fatal("no url-test group parsed")
	}
	if len(auto.Literals) != 0 {
		t.Fatalf("url-test group picked up literals: %v", auto.Literals)
	}
	if auto.TestURL != "http://www.gstatic.com/generate_204" {
		t.Errorf("test url = %q", auto.TestURL)
	}
	if auto.Interval != 300 || auto.Tolerance != 50 {
		t.Errorf("timings = interval %d tolerance %d, want 300 and 50", auto.Interval, auto.Tolerance)
	}
	if auto.Timeout != 0 {
		t.Errorf("blank timeout became %d, want 0", auto.Timeout)
	}
}

func TestRegionGroupFiltersByName(t *testing.T) {
	cfg := parse(t, "custom_proxy_group=🇭🇰 香港节点`url-test`(?i)港|HK|Hong`http://x/`300,,50\n")
	g := cfg.Groups[0]
	if g.Match == nil {
		t.Fatal("no pattern")
	}
	for _, in := range []string{"香港 01", "hk-1", "Hong Kong"} {
		if !g.Match.MatchString(in) {
			t.Errorf("%q should match", in)
		}
	}
	if g.Match.MatchString("Tokyo 03") {
		t.Error("Tokyo should not match a Hong Kong group")
	}
}

func TestListURLsAreDeduplicatedInOrder(t *testing.T) {
	cfg := parse(t, "custom_proxy_group=A`select`.*\n"+
		"ruleset=A,https://x/one.list\nruleset=B,https://x/two.list\n"+
		"ruleset=B,https://x/one.list\nruleset=A,[]FINAL\n")
	got := cfg.ListURLs()
	want := []string{"https://x/one.list", "https://x/two.list"}
	if len(got) != len(want) {
		t.Fatalf("urls = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("urls = %v, want %v", got, want)
		}
	}
}

func TestRejectsWhatIsNotAPreset(t *testing.T) {
	for _, src := range []string{
		"",
		"[custom]\n; nothing here\n",
		"ruleset=A,https://x/one.list\n", // rules but no groups
	} {
		if _, err := ParseINI(src); err == nil {
			t.Errorf("accepted %q", src)
		}
	}
	if _, err := ParseINI("custom_proxy_group=A`teleport`.*\n"); err == nil {
		t.Error("accepted an unknown group type")
	}
	if _, err := ParseINI("custom_proxy_group=A`select`[(\n"); err == nil {
		t.Error("accepted a malformed pattern")
	}
}

// Unknown directives are the norm in these files, not an error to report.
func TestUnknownDirectivesAreIgnored(t *testing.T) {
	cfg := parse(t, "enable_rule_generator=true\noverwrite_original_rules=true\n"+
		"custom_proxy_group=A`select`.*\n")
	if len(cfg.Groups) != 1 {
		t.Fatalf("groups = %d", len(cfg.Groups))
	}
}

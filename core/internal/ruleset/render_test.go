package ruleset

import (
	"strings"
	"testing"
)

func render(t *testing.T, ini string, proxies ...string) Rendered {
	t.Helper()
	cfg, err := ParseINI(ini)
	if err != nil {
		t.Fatalf("ParseINI: %v", err)
	}
	return Render(cfg, proxies, "https://panel.example.com/sub/TOKEN/rules")
}

const regionINI = "custom_proxy_group=🚀 节点选择`select`[]♻️ 自动选择`[]🇭🇰 香港`[]🇯🇵 日本`[]DIRECT\n" +
	"custom_proxy_group=♻️ 自动选择`url-test`.*`http://www.gstatic.com/generate_204`300,,50\n" +
	"custom_proxy_group=🇭🇰 香港`url-test`(?i)香港|HK`http://www.gstatic.com/generate_204`300,,50\n" +
	"custom_proxy_group=🇯🇵 日本`url-test`(?i)日本|JP`http://www.gstatic.com/generate_204`300,,50\n" +
	"ruleset=🇯🇵 日本,https://x/jp.list\n" +
	"ruleset=🚀 节点选择,[]FINAL\n"

// Clash refuses a configuration that contains a proxy group with an empty
// proxies list. Every popular preset carries region groups, so on a fleet
// without a node in some region the whole subscription would be rejected —
// not degraded, rejected.
func TestEmptyGroupsAreDroppedNotEmitted(t *testing.T) {
	r := render(t, regionINI, "香港 01", "香港 02")
	if strings.Contains(r.ProxyGroups, "日本") {
		t.Fatalf("kept a group with no members:\n%s", r.ProxyGroups)
	}
	if !strings.Contains(r.ProxyGroups, "香港") {
		t.Fatalf("dropped a group that had members:\n%s", r.ProxyGroups)
	}
	if len(r.Dropped) != 1 || r.Dropped[0] != "🇯🇵 日本" {
		t.Fatalf("Dropped = %v, want just the Japan group", r.Dropped)
	}
	// The dangling member is the part that is easy to miss: a group listing a
	// group that no longer exists fails exactly like the empty one did.
	if strings.Contains(r.ProxyGroups, "- 🇯🇵") {
		t.Fatalf("left a reference to a dropped group:\n%s", r.ProxyGroups)
	}
	// And a rule pointing at it would make the client reject the file too.
	if strings.Contains(r.Rules, "日本") {
		t.Fatalf("left a rule targeting a dropped group:\n%s", r.Rules)
	}
}

// A subscriber with no nodes still gets a loadable file: every region group
// goes, and the selector survives on DIRECT alone. Everything routes direct,
// which is what a subscription with nothing in it should do.
func TestNoProxiesStillProducesALoadableConfig(t *testing.T) {
	r := render(t, regionINI)
	sel := section(r.ProxyGroups, "🚀 节点选择")
	if !strings.Contains(sel, "- DIRECT") {
		t.Fatalf("selector lost its DIRECT fallback:\n%s", r.ProxyGroups)
	}
	if strings.Contains(r.ProxyGroups, "香港") || strings.Contains(r.ProxyGroups, "日本") {
		t.Fatalf("a region group survived with no nodes:\n%s", r.ProxyGroups)
	}
	if !strings.Contains(r.Rules, "- MATCH,🚀 节点选择") {
		t.Fatalf("catch-all should still route to the surviving selector:\n%s", r.Rules)
	}
}

// Dropping cascades: a group whose only members are groups that all drop has
// nothing left either, and one pass would leave it behind pointing at names
// that no longer exist.
func TestDroppingCascades(t *testing.T) {
	ini := "custom_proxy_group=顶层`select`[]地区\n" +
		"custom_proxy_group=地区`select`[]🇯🇵 日本\n" +
		"custom_proxy_group=🇯🇵 日本`url-test`(?i)日本|JP`http://x/`300,,50\n" +
		"ruleset=顶层,[]FINAL\n"
	r := render(t, ini, "香港 01")
	if strings.TrimSpace(r.ProxyGroups) != "proxy-groups:" {
		t.Fatalf("expected all three to drop, got:\n%s", r.ProxyGroups)
	}
	if len(r.Dropped) != 3 {
		t.Fatalf("Dropped = %v, want all three", r.Dropped)
	}
	// The panel's own direct rule stays — it targets DIRECT, which no amount
	// of dropping can remove, and it is the one rule that has to survive so
	// the operator can still reach the console.
	for _, line := range strings.Split(r.Rules, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "rules:" || strings.HasSuffix(line, ",DIRECT") {
			continue
		}
		t.Fatalf("a rule survived with no group to route to: %q", line)
	}
}

func TestProxiesAreMatchedIntoTheirGroups(t *testing.T) {
	r := render(t, regionINI, "香港 01", "日本 01", "US 01")
	for _, want := range []string{"- 香港 01", "- 日本 01", "- US 01"} {
		if !strings.Contains(r.ProxyGroups, want) {
			t.Errorf("missing %q in:\n%s", want, r.ProxyGroups)
		}
	}
	// The Hong Kong group takes only its own.
	hk := section(r.ProxyGroups, "🇭🇰 香港")
	if strings.Contains(hk, "日本 01") || strings.Contains(hk, "US 01") {
		t.Errorf("Hong Kong group took foreign nodes:\n%s", hk)
	}
}

func TestRuleListsBecomeProvidersServedByThePanel(t *testing.T) {
	r := render(t, regionINI, "日本 01")
	if len(r.Providers) != 1 || r.Providers["jp"] != "https://x/jp.list" {
		t.Fatalf("providers = %v", r.Providers)
	}
	for _, want := range []string{
		"rule-providers:",
		"  jp:",
		"    behavior: classical",
		"url: https://panel.example.com/sub/TOKEN/rules/jp.yaml",
	} {
		if !strings.Contains(r.RuleProviders, want) {
			t.Errorf("missing %q in:\n%s", want, r.RuleProviders)
		}
	}
	if !strings.Contains(r.Rules, "- RULE-SET,jp,🇯🇵 日本") {
		t.Errorf("rule does not reference the provider:\n%s", r.Rules)
	}
}

func TestInlineRulesKeepTheirShape(t *testing.T) {
	r := render(t, "custom_proxy_group=A`select`.*\nruleset=A,[]GEOIP,CN\nruleset=A,[]FINAL\n", "n1")
	if !strings.Contains(r.Rules, "- GEOIP,CN,A") {
		t.Errorf("GEOIP rule wrong:\n%s", r.Rules)
	}
	if !strings.Contains(r.Rules, "- MATCH,A") {
		t.Errorf("catch-all wrong:\n%s", r.Rules)
	}
}

// Two lists with the same basename must not collide: the name is the client's
// on-disk cache path, so a collision has one list overwriting the other.
func TestProviderNamesAreUnique(t *testing.T) {
	r := render(t, "custom_proxy_group=A`select`.*\n"+
		"ruleset=A,https://one.example/rules/ads.list\n"+
		"ruleset=A,https://two.example/other/ads.list\n", "n1")
	if len(r.Providers) != 2 {
		t.Fatalf("providers = %v, want two distinct names", r.Providers)
	}
	if _, ok := r.Providers["ads"]; !ok {
		t.Errorf("first list should keep the plain name: %v", r.Providers)
	}
	if _, ok := r.Providers["ads-2"]; !ok {
		t.Errorf("second list should be suffixed: %v", r.Providers)
	}
}

func TestTheSameListIsFetchedOnce(t *testing.T) {
	r := render(t, "custom_proxy_group=A`select`.*\ncustom_proxy_group=B`select`.*\n"+
		"ruleset=A,https://x/one.list\nruleset=B,https://x/one.list\n", "n1")
	if len(r.Providers) != 1 {
		t.Fatalf("providers = %v, want one", r.Providers)
	}
	if strings.Count(r.RuleProviders, "type: http") != 1 {
		t.Errorf("declared the same provider twice:\n%s", r.RuleProviders)
	}
	if strings.Count(r.Rules, "RULE-SET,one,") != 2 {
		t.Errorf("both rules should reference it:\n%s", r.Rules)
	}
}

// A node named "no" or "12" is legal and would otherwise be read as a boolean
// or a number, silently changing which proxy a group refers to.
func TestNamesThatYamlWouldMisread(t *testing.T) {
	r := render(t, "custom_proxy_group=A`select`.*\nruleset=A,[]FINAL\n", "no", "tokyo: 01", "12")
	for _, want := range []string{`- "no"`, `- "tokyo: 01"`, `- 12`} {
		if !strings.Contains(r.ProxyGroups, want) {
			t.Errorf("missing %q in:\n%s", want, r.ProxyGroups)
		}
	}
}

func TestProbingGroupsCarryTheirTimings(t *testing.T) {
	r := render(t, regionINI, "香港 01")
	auto := section(r.ProxyGroups, "♻️ 自动选择")
	for _, want := range []string{"type: url-test", "url: http://www.gstatic.com/generate_204",
		"interval: 300", "tolerance: 50"} {
		if !strings.Contains(auto, want) {
			t.Errorf("missing %q in:\n%s", want, auto)
		}
	}
	// A select group has no business carrying a probe URL.
	if strings.Contains(section(r.ProxyGroups, "🚀 节点选择"), "url:") {
		t.Error("select group got a test url")
	}
}

// section returns the lines of one proxy-group entry, for assertions that must
// not be satisfied by a different group in the same document.
func section(doc, name string) string {
	lines := strings.Split(doc, "\n")
	var out []string
	in := false
	for _, l := range lines {
		if strings.HasPrefix(l, "  - name:") {
			in = strings.Contains(l, name)
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// A .list is one rule per line with comments; a classical rule-provider wants
// a YAML list. One malformed entry rejects the whole provider file, so the
// conversion has to be total rather than mostly right.
func TestProviderYAMLConversion(t *testing.T) {
	got := ProviderYAML(strings.Join([]string{
		"# 直连列表",
		"",
		"; another comment style",
		"// and another",
		"DOMAIN-SUFFIX,example.com",
		"  IP-CIDR,10.0.0.0/8,no-resolve  ",
		"DOMAIN-KEYWORD,it's",
	}, "\n"))
	want := "payload:\n" +
		"  - 'DOMAIN-SUFFIX,example.com'\n" +
		"  - 'IP-CIDR,10.0.0.0/8,no-resolve'\n" +
		"  - 'DOMAIN-KEYWORD,it''s'\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// no-resolve is part of the rule, not a policy: dropping it makes the client
// resolve every domain before matching an IP rule, which is both slower and
// leaks the lookup.
func TestProviderYAMLKeepsNoResolve(t *testing.T) {
	if !strings.Contains(ProviderYAML("IP-CIDR,1.1.1.1/32,no-resolve"), "no-resolve") {
		t.Fatal("dropped no-resolve")
	}
}

func TestProviderYAMLOnCommentsOnlyIsStillValid(t *testing.T) {
	if got := ProviderYAML("# nothing but a comment\n"); got != "payload:\n" {
		t.Fatalf("got %q", got)
	}
}

// The panel must not depend on the proxy working.
//
// Every preset ends in a catch-all, so without this the panel's own hostname
// goes through the proxy — and then a broken proxy takes the console with it,
// at exactly the moment the operator needs the console to fix the proxy. It
// also makes the rules unloadable: the rule lists are served by the panel, so
// fetching them is itself routed by the rules being fetched.
func TestThePanelIsAlwaysDirect(t *testing.T) {
	r := render(t, regionINI, "香港 01")
	first := strings.SplitN(strings.TrimPrefix(r.Rules, "rules:\n"), "\n", 2)[0]
	if first != "  - DOMAIN,panel.example.com,DIRECT" {
		t.Fatalf("first rule = %q, want the panel pinned direct", first)
	}
	// Ahead of the catch-all, or the catch-all wins.
	if strings.Index(r.Rules, "DOMAIN,panel.example.com,DIRECT") > strings.Index(r.Rules, "MATCH,") {
		t.Fatal("the panel rule must come before the catch-all")
	}
}

func TestNoPanelRuleWithoutAUsableBase(t *testing.T) {
	cfg, err := ParseINI(regionINI)
	if err != nil {
		t.Fatal(err)
	}
	r := Render(cfg, []string{"香港 01"}, "")
	if strings.Contains(r.Rules, "DIRECT\n  - DOMAIN") || strings.Contains(r.Rules, "DOMAIN,,DIRECT") {
		t.Fatalf("emitted an empty-host rule:\n%s", r.Rules)
	}
}

// The order inside every group is the order of the proxy list, and nothing
// else. That is what makes "arrange the nodes once" enough: the operator sets
// one list, and 节点选择 — and 自动选择, and each region group — comes out in
// that order without a second setting that could disagree with the first.
//
// Also the reason the first member matters: a select group opens on it, so
// whatever the operator put at the top is what a client picks by default.
func TestGroupMembersFollowTheProxyListOrder(t *testing.T) {
	// Deliberately not alphabetical, and not the order a previous version
	// would have produced by sorting.
	proxies := []string{"香港 03", "日本 01", "香港 01"}
	r := render(t, regionINI, proxies...)

	members := groupMembers(t, r.ProxyGroups, "🚀 节点选择")
	// The literals the preset names come first, then the pattern's matches in
	// list order.
	want := []string{"♻️ 自动选择", "🇭🇰 香港", "🇯🇵 日本", "DIRECT"}
	for i := range want {
		if members[i] != want[i] {
			t.Fatalf("节点选择 = %v, want it to start %v", members, want)
		}
	}

	auto := groupMembers(t, r.ProxyGroups, "♻️ 自动选择")
	for i, want := range proxies {
		if auto[i] != want {
			t.Fatalf("自动选择 = %v, want the proxy list order %v", auto, proxies)
		}
	}

	hk := groupMembers(t, r.ProxyGroups, "🇭🇰 香港")
	if len(hk) != 2 || hk[0] != "香港 03" || hk[1] != "香港 01" {
		t.Fatalf("香港 = %v, want the two in list order (03 before 01)", hk)
	}
}

// groupMembers reads one group's proxies back out of the rendered YAML, so the
// assertion is about what a client will actually load.
func groupMembers(t *testing.T, groups, name string) []string {
	t.Helper()
	var out []string
	inGroup := false
	inProxies := false
	for _, line := range strings.Split(groups, "\n") {
		switch {
		case strings.HasPrefix(line, "  - name: "):
			inGroup = strings.Contains(line, name)
			inProxies = false
		case inGroup && strings.HasPrefix(line, "    proxies:"):
			inProxies = true
		case inGroup && inProxies && strings.HasPrefix(line, "      - "):
			out = append(out, strings.Trim(strings.TrimPrefix(line, "      - "), `"`))
		}
	}
	if out == nil {
		t.Fatalf("group %q not found in:\n%s", name, groups)
	}
	return out
}

package clashrule

import (
	"strings"
	"testing"
)

func mini(t *testing.T) Config {
	t.Helper()
	cfg, err := ParseINI([]byte(miniINI))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func render(t *testing.T, cfg Config, names []string) (Rendered, error) {
	t.Helper()
	return Render(cfg, names,
		func(string) bool { return true },
		func(n string) string { return "https://panel.example.com/sub/tok/rules/" + n + ".yaml" })
}

func TestRenderMini(t *testing.T) {
	out, err := render(t, mini(t), []string{"东京 01", "大阪 02"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"proxy-groups:", "rule-providers:", "rules:",
		"MATCH,🐟 漏网之鱼",
		"GEOIP,CN,🎯 全球直连",
		"RULE-SET,LocalAreaNetwork,🎯 全球直连",
		"behavior: classical",
	} {
		if !strings.Contains(out.ProxyGroups+out.RuleProviders+out.Rules, want) {
			t.Errorf("missing %q", want)
		}
	}
	// `.*` expands to every proxy, in subscription order.
	if !strings.Contains(out.ProxyGroups, "- 东京 01\n") || !strings.Contains(out.ProxyGroups, "- 大阪 02\n") {
		t.Errorf("proxies not expanded into the groups:\n%s", out.ProxyGroups)
	}
	if len(out.Providers) != 3 {
		t.Errorf("providers = %d, want 3", len(out.Providers))
	}
}

// A url-test group carries its probe settings; a select group must not.
func TestRenderTestingGroupKeepsItsProbe(t *testing.T) {
	out, _ := render(t, mini(t), []string{"A"})
	auto := section(out.ProxyGroups, "♻️ 自动选择")
	if !strings.Contains(auto, "url: http://www.gstatic.com/generate_204") ||
		!strings.Contains(auto, "interval: 300") || !strings.Contains(auto, "tolerance: 50") {
		t.Errorf("url-test group lost its probe settings:\n%s", auto)
	}
	if sel := section(out.ProxyGroups, "🚀 节点选择"); strings.Contains(sel, "interval:") {
		t.Errorf("select group got probe settings:\n%s", sel)
	}
}

// The whole rule set is refused when a list is missing rather than quietly
// leaving that traffic to a later rule. A hole in a rule set routes silently.
func TestRenderRefusesAnUnfetchedList(t *testing.T) {
	_, err := Render(mini(t), []string{"A"},
		func(u string) bool { return !strings.Contains(u, "BanAD") },
		func(n string) string { return "https://p/" + n })
	if err == nil {
		t.Fatal("rendered with a list missing from the cache")
	}
	if !strings.Contains(err.Error(), "BanAD") {
		t.Errorf("error should name the missing list, got: %v", err)
	}
}

// The country-splitting variants filter proxies by name. With names that carry
// no country they match nothing, and clash refuses to load a group with no
// members — so this has to fail here, where it can be explained.
func TestRenderRefusesAGroupThatMatchesNoProxy(t *testing.T) {
	cfg := Config{
		Rulesets: []Ruleset{{Group: "🇭🇰 香港", Builtin: "FINAL"}},
		Groups:   []Group{{Name: "🇭🇰 香港", Type: "select", Members: []string{"(?i)(香港|HK)"}}},
	}
	_, err := render(t, cfg, []string{"东京 01"})
	if err == nil {
		t.Fatal("rendered a group with no members")
	}
	if !strings.Contains(err.Error(), "香港") {
		t.Errorf("error should name the group, got: %v", err)
	}
}

func TestRenderRefusesWithoutFinal(t *testing.T) {
	cfg := Config{
		Rulesets: []Ruleset{{Group: "A", URL: "https://x/y.list"}},
		Groups:   []Group{{Name: "A", Type: "select", Members: []string{"[]DIRECT"}}},
	}
	if _, err := render(t, cfg, []string{"n"}); err == nil {
		t.Fatal("rendered a rule set with no MATCH")
	}
}

func TestRenderRefusesRulesAfterFinal(t *testing.T) {
	cfg := Config{
		Rulesets: []Ruleset{
			{Group: "A", Builtin: "FINAL"},
			{Group: "A", Builtin: "GEOIP,CN"},
		},
		Groups: []Group{{Name: "A", Type: "select", Members: []string{"[]DIRECT"}}},
	}
	if _, err := render(t, cfg, []string{"n"}); err == nil {
		t.Fatal("accepted a rule after MATCH, which can never fire")
	}
}

func TestRenderRefusesAnUndeclaredGroup(t *testing.T) {
	cfg := Config{
		Rulesets: []Ruleset{{Group: "ghost", Builtin: "FINAL"}},
		Groups:   []Group{{Name: "A", Type: "select", Members: []string{"[]DIRECT"}}},
	}
	if _, err := render(t, cfg, []string{"n"}); err == nil {
		t.Fatal("accepted a ruleset pointing at an undeclared group")
	}
}

// Two lists whose basenames collide is a real shape: ACL4SSR keeps some under
// Clash/ and others under Clash/Ruleset/.
func TestProviderNamesAreUnique(t *testing.T) {
	cfg := Config{
		Rulesets: []Ruleset{
			{Group: "A", URL: "https://x/Clash/Steam.list"},
			{Group: "A", URL: "https://x/Clash/Ruleset/Steam.list"},
			{Group: "A", Builtin: "FINAL"},
		},
		Groups: []Group{{Name: "A", Type: "select", Members: []string{"[]DIRECT"}}},
	}
	out, err := render(t, cfg, []string{"n"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Providers) != 2 || out.Providers[0].Name == out.Providers[1].Name {
		t.Fatalf("colliding basenames produced %v", out.Providers)
	}
}

func TestToPayloadKeepsRulesAndDropsComments(t *testing.T) {
	list := "# 本地/局域网地址\n\nDOMAIN-SUFFIX,lan\nIP-CIDR,10.0.0.0/8,no-resolve\n" +
		"DOMAIN,example.com # trailing note\n"
	got, n := ToPayload([]byte(list))
	if n != 3 {
		t.Fatalf("rule count = %d, want 3", n)
	}
	if !strings.HasPrefix(got, "payload:\n") {
		t.Error("missing the payload: header a provider needs")
	}
	// no-resolve stays put and no target is spliced in: in a classical
	// provider the destination comes from the RULE-SET rule.
	if !strings.Contains(got, "- IP-CIDR,10.0.0.0/8,no-resolve\n") {
		t.Errorf("IP-CIDR line altered:\n%s", got)
	}
	if strings.Contains(got, "trailing note") {
		t.Errorf("trailing comment kept:\n%s", got)
	}
}

// section returns the lines of one proxy-group entry, for assertions.
func section(groups, name string) string {
	lines := strings.Split(groups, "\n")
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

// Quoting rules, both directions. Over-quoting is not merely untidy: these
// payloads run to tens of thousands of lines.
func TestYamlScalarQuotesOnlyWhatNeedsIt(t *testing.T) {
	bare := []string{
		"IP-CIDR,10.0.0.0/8,no-resolve", // commas are flow-context only
		"DOMAIN-SUFFIX,acl4.ssr",
		"🚀 节点选择", // emoji are not indicators
		"东京 01",
		"PROCESS-NAME,v2ray",
	}
	for _, s := range bare {
		if got := yamlScalar(s); got != s {
			t.Errorf("quoted %q unnecessarily -> %s", s, got)
		}
	}
	quoted := []string{
		"- leading dash", "*anchor", "&ref", "#comment", "%directive",
		"key: value", "trailing ", " leading", "has #hash", "ends:", "",
	}
	for _, s := range quoted {
		if got := yamlScalar(s); !strings.HasPrefix(got, `"`) {
			t.Errorf("left %q bare -> %s", s, got)
		}
	}
}

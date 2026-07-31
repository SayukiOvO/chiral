package clashrule

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// ProviderInterval is how often a client re-fetches a rule list, in seconds.
// A day: these lists change on the order of weeks, and every subscriber
// re-fetching every list more often than that is load the panel carries for no
// benefit.
const ProviderInterval = 86400

// Provider is one rule list as the subscription refers to it.
type Provider struct {
	// Name is the key under rule-providers, derived from the list's filename.
	Name string
	// URL is where the list came from upstream; the panel serves its own copy.
	URL string
}

// Rendered is the three sections a clash-family subscription gains.
type Rendered struct {
	ProxyGroups   string
	RuleProviders string
	Rules         string
	// Providers in the order they are referenced, for the caller to serve.
	Providers []Provider
}

// Render builds the sections for one subscriber.
//
// proxyNames are the proxies already rendered into the subscription, in order.
// providerURL maps a list's provider name to the address this panel serves it
// at — the lists are cached here rather than fetched from GitHub by every
// client, since reaching GitHub is frequently the thing the subscriber is
// using this proxy to do.
//
// available reports whether a list is in the cache. A list that is missing
// makes the whole thing fail rather than silently dropping rules: a rule set
// with a hole in it does not announce itself, it just sends some traffic
// somewhere else.
func Render(cfg Config, proxyNames []string, available func(url string) bool, providerURL func(name string) string) (Rendered, error) {
	if len(proxyNames) == 0 {
		return Rendered{}, fmt.Errorf("no proxies to put in the groups")
	}

	groups, err := expandGroups(cfg.Groups, proxyNames)
	if err != nil {
		return Rendered{}, err
	}

	declared := make(map[string]bool, len(cfg.Groups))
	for _, g := range cfg.Groups {
		declared[g.Name] = true
	}

	var out Rendered
	names := providerNames(cfg.Rulesets)
	seen := make(map[string]bool)
	var rules strings.Builder
	var providers strings.Builder
	providers.WriteString("rule-providers:\n")
	sawFinal := false

	for i, rs := range cfg.Rulesets {
		if !declared[rs.Group] && rs.Group != "DIRECT" && rs.Group != "REJECT" {
			return Rendered{}, fmt.Errorf("ruleset points at group %q, which no custom_proxy_group declares", rs.Group)
		}
		switch {
		case rs.Builtin == "FINAL", rs.Builtin == "MATCH":
			// Must be last: clash stops at the first match, so a rule after
			// MATCH can never fire.
			if i != len(cfg.Rulesets)-1 {
				return Rendered{}, fmt.Errorf("[]FINAL is not the last ruleset; everything after it is unreachable")
			}
			fmt.Fprintf(&rules, "  - MATCH,%s\n", rs.Group)
			sawFinal = true
		case rs.Builtin != "":
			fmt.Fprintf(&rules, "  - %s,%s\n", rs.Builtin, rs.Group)
		default:
			name := names[rs.URL]
			if !available(rs.URL) {
				return Rendered{}, fmt.Errorf("rule list %q has not been fetched yet", name)
			}
			if !seen[name] {
				seen[name] = true
				out.Providers = append(out.Providers, Provider{Name: name, URL: rs.URL})
				fmt.Fprintf(&providers, "  %s:\n    type: http\n    behavior: classical\n"+
					"    url: %q\n    path: ./chiral/%s.yaml\n    interval: %d\n",
					name, providerURL(name), name, ProviderInterval)
			}
			fmt.Fprintf(&rules, "  - RULE-SET,%s,%s\n", name, rs.Group)
		}
	}
	if !sawFinal {
		// Without one, traffic that matches nothing follows clash's own
		// default rather than the operator's intent, which is a leak that
		// looks like it works.
		return Rendered{}, fmt.Errorf("no []FINAL ruleset: unmatched traffic would have no destination")
	}

	out.ProxyGroups = groups
	out.Rules = "rules:\n" + rules.String()
	if len(out.Providers) > 0 {
		out.RuleProviders = providers.String()
	}
	return out, nil
}

// expandGroups turns the declared groups into YAML, resolving each member.
func expandGroups(gs []Group, proxyNames []string) (string, error) {
	declared := make(map[string]bool, len(gs))
	for _, g := range gs {
		declared[g.Name] = true
	}

	var b strings.Builder
	b.WriteString("proxy-groups:\n")
	for _, g := range gs {
		members, err := expandMembers(g, proxyNames, declared)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "  - name: %s\n    type: %s\n", yamlScalar(g.Name), g.Type)
		if testingTypes[g.Type] {
			if g.TestURL != "" {
				fmt.Fprintf(&b, "    url: %s\n", g.TestURL)
			}
			if g.Interval > 0 {
				fmt.Fprintf(&b, "    interval: %d\n", g.Interval)
			}
			if g.Tolerance > 0 {
				fmt.Fprintf(&b, "    tolerance: %d\n", g.Tolerance)
			}
		}
		b.WriteString("    proxies:\n")
		for _, m := range members {
			fmt.Fprintf(&b, "      - %s\n", yamlScalar(m))
		}
	}
	return b.String(), nil
}

// expandMembers resolves one group's member list.
//
// A "[]"-prefixed entry is a reference — to another group, or to DIRECT or
// REJECT. Anything else is a regular expression over proxy names, which is how
// the country-splitting variants of ACL4SSR work; with names that carry no
// country, those match nothing, and an empty group is a config clash refuses
// to load. Better to say so here than to ship it.
func expandMembers(g Group, proxyNames []string, declared map[string]bool) ([]string, error) {
	var out []string
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, m := range g.Members {
		if ref, isRef := strings.CutPrefix(m, "[]"); isRef {
			if ref != "DIRECT" && ref != "REJECT" && !declared[ref] {
				return nil, fmt.Errorf("group %q references %q, which is not declared", g.Name, ref)
			}
			add(ref)
			continue
		}
		re, err := regexp.Compile(m)
		if err != nil {
			return nil, fmt.Errorf("group %q: %q is not a valid filter: %w", g.Name, m, err)
		}
		for _, n := range proxyNames {
			if re.MatchString(n) {
				add(n)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("group %q matched no proxies and references nothing; "+
			"clash refuses a group with no members — this config's filters expect node names it cannot find", g.Name)
	}
	return out, nil
}

// providerNames assigns each list URL a key for rule-providers, derived from
// its filename and made unique. Two lists with the same basename in different
// directories is a real shape in ACL4SSR (Clash/ and Clash/Ruleset/).
func providerNames(rs []Ruleset) map[string]string {
	out := make(map[string]string, len(rs))
	taken := make(map[string]bool)
	for _, r := range rs {
		if r.URL == "" || out[r.URL] != "" {
			continue
		}
		base := strings.TrimSuffix(path.Base(r.URL), path.Ext(path.Base(r.URL)))
		base = sanitise(base)
		if base == "" {
			base = "list"
		}
		name := base
		for i := 2; taken[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		taken[name] = true
		out[r.URL] = name
	}
	return out
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func sanitise(s string) string { return unsafeName.ReplaceAllString(s, "_") }

// yamlScalar quotes a value only when leaving it bare would change its
// meaning. Group names are emoji-led strings out of someone else's config, and
// rule lines are full of commas and colons.
//
// Quoting on any comma would be the safe-looking choice and it is wrong twice:
// a comma is only special in flow context (`[a, b]`), never in a block
// sequence, and quoting every rule line inflates a payload that already runs
// to tens of thousands of entries. What actually needs quoting is an indicator
// at the START of the scalar, a ": " or " #" anywhere in it, and surrounding
// whitespace.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if strings.TrimSpace(s) != s || strings.Contains(s, ": ") ||
		strings.Contains(s, " #") || strings.ContainsAny(s, "\n\r\t") ||
		strings.HasSuffix(s, ":") ||
		strings.ContainsAny(s[:1], "-?:,[]{}#&*!|>'\"%@`") {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}

// ToPayload converts a rule list into the payload a clash rule-provider
// expects. The lists are bare rules with comments; a provider wants a YAML
// list under `payload:`.
//
// The target is deliberately not appended: in a classical provider the
// destination comes from the RULE-SET rule that references it. Splicing it in
// here is where the inline form goes wrong — `IP-CIDR,10.0.0.0/8,no-resolve`
// has to become `IP-CIDR,10.0.0.0/8,GROUP,no-resolve`, target before flag, and
// getting that order wrong yields a rule clash accepts and never matches.
func ToPayload(list []byte) (string, int) {
	var b strings.Builder
	b.WriteString("payload:\n")
	n := 0
	for _, raw := range strings.Split(string(list), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if i := strings.Index(line, " #"); i > 0 {
			line = strings.TrimSpace(line[:i])
		}
		fmt.Fprintf(&b, "  - %s\n", yamlScalar(line))
		n++
	}
	return b.String(), n
}

// Package ruleset turns an ACL4SSR-style remote configuration into the parts
// of a Clash config that describe routing: the proxy groups and the rules that
// select between them.
//
// The format is subconverter's, which is what every ACL4SSR preset is written
// in. Two directives carry everything:
//
//	ruleset=<group>,<url>                     a rule list, routed to <group>
//	ruleset=<group>,[]<rule>                  a single inline rule
//	custom_proxy_group=<name>`<type>`<member>`<member>...
//
// A member is either `[]Name` — a literal proxy, group or policy — or a
// regular expression matched against the names of the subscriber's own
// proxies, which is how the region groups ("香港节点" and friends) are built.
// The test-based types carry a trailing `<url>`<interval>,<timeout>,<tolerance>.
//
// Parsing is deliberately separate from fetching and from rendering: this file
// is pure, so the grammar can be tested without a network or a database, and
// the presets can be checked against it as data.
package ruleset

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// GroupType is a Clash proxy-group type.
type GroupType string

const (
	GroupSelect      GroupType = "select"
	GroupURLTest     GroupType = "url-test"
	GroupFallback    GroupType = "fallback"
	GroupLoadBalance GroupType = "load-balance"
)

// Group is one entry of the Clash proxy-groups list, before the subscriber's
// own proxies have been matched into it.
type Group struct {
	Name string
	Type GroupType
	// Literals are members named outright: another group, DIRECT, REJECT.
	// Order is preserved, and matters — it is the order the client shows.
	Literals []string
	// Match filters the subscriber's proxies by name. Nil when the group
	// takes no proxies of its own. `.*` (every proxy) is the common case.
	Match *regexp.Regexp
	// MatchSource is the pattern as written, kept for diagnostics: a preset
	// that selects on names no node has produces an empty group, and the
	// operator needs to see which pattern did that.
	MatchSource string

	// Test parameters, for the types that probe. Zero for select.
	TestURL   string
	Interval  int
	Timeout   int
	Tolerance int
}

// Rule is one routing rule: either a list of rules fetched from URL, or a
// single rule written inline in the preset.
type Rule struct {
	// Group is the policy the matching traffic is sent to.
	Group string
	// URL is the rule list to fetch; empty for an inline rule.
	URL string
	// Inline is the rule text as it appears in a Clash rules list, e.g.
	// "GEOIP,CN" or "MATCH". Empty when URL is set.
	Inline string
}

// Config is a parsed preset.
type Config struct {
	Groups []Group
	Rules  []Rule
}

// ListURLs returns every distinct rule-list URL the config references, in
// order of first appearance, so a fetcher can walk them without re-deriving
// the set.
func (c Config) ListURLs() []string {
	seen := make(map[string]struct{}, len(c.Rules))
	out := make([]string, 0, len(c.Rules))
	for _, r := range c.Rules {
		if r.URL == "" {
			continue
		}
		if _, dup := seen[r.URL]; dup {
			continue
		}
		seen[r.URL] = struct{}{}
		out = append(out, r.URL)
	}
	return out
}

// ParseINI reads a subconverter remote configuration.
//
// Unknown directives are ignored rather than rejected: the presets carry
// several this panel has no use for (emoji, rename, include_remarks), and
// failing on them would mean tracking upstream's whole feature set to render
// its routing.
func ParseINI(src string) (Config, error) {
	var cfg Config
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		// `;` and `#` both comment in these files, and a section header is
		// not a directive.
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "[") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "ruleset":
			r, err := parseRuleset(value)
			if err != nil {
				return Config{}, fmt.Errorf("line %d: %w", i+1, err)
			}
			cfg.Rules = append(cfg.Rules, r)
		case "custom_proxy_group":
			g, err := parseGroup(value)
			if err != nil {
				return Config{}, fmt.Errorf("line %d: %w", i+1, err)
			}
			cfg.Groups = append(cfg.Groups, g)
		}
	}
	if len(cfg.Groups) == 0 {
		return Config{}, fmt.Errorf("no custom_proxy_group directives; this does not look like an ACL4SSR preset")
	}
	return cfg, nil
}

// parseRuleset reads `<group>,<url or []rule>`.
//
// Splitting on the FIRST comma is required rather than convenient: an inline
// rule may contain commas of its own — `[]GEOIP,CN` is one rule, not a group
// named "[]GEOIP" and a rule "CN".
func parseRuleset(value string) (Rule, error) {
	group, rest, ok := strings.Cut(strings.TrimSpace(value), ",")
	if !ok {
		return Rule{}, fmt.Errorf("ruleset needs <group>,<url>: %q", value)
	}
	group = strings.TrimSpace(group)
	rest = strings.TrimSpace(rest)
	if group == "" || rest == "" {
		return Rule{}, fmt.Errorf("ruleset has an empty group or target: %q", value)
	}
	if inline, found := strings.CutPrefix(rest, "[]"); found {
		// FINAL is subconverter's name for what Clash calls MATCH.
		if strings.EqualFold(inline, "FINAL") {
			inline = "MATCH"
		}
		return Rule{Group: group, Inline: inline}, nil
	}
	return Rule{Group: group, URL: resolveListPath(rest)}, nil
}

// subconverterRulesPrefix is where a subconverter installation keeps its
// bundled copy of the rule repositories.
const subconverterRulesPrefix = "rules/ACL4SSR/"

// aclRawBase is that copy's origin.
const aclRawBase = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/"

// resolveListPath turns a rule-list reference into something fetchable.
//
// Roughly half the presets — every one that is not an "Online" variant — point
// at paths like `rules/ACL4SSR/Clash/BanAD.list`. Those are files inside a
// subconverter installation, which is a mirror of the ACL4SSR repository, so
// the same path under the repository's raw URL is the same file. Without this
// those presets parse cleanly and then resolve to nothing: a subscription with
// rule providers that 404, which the client reports as an empty rule set
// rather than as a broken configuration.
func resolveListPath(ref string) string {
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return ref
	}
	if rest, found := strings.CutPrefix(ref, subconverterRulesPrefix); found {
		return aclRawBase + rest
	}
	return ref
}

// parseGroup reads `<name>`<type>`<member>`...[`<url>`<interval>,<timeout>,<tolerance>]`.
func parseGroup(value string) (Group, error) {
	parts := strings.Split(strings.TrimSpace(value), "`")
	if len(parts) < 3 {
		return Group{}, fmt.Errorf("custom_proxy_group needs at least <name>`<type>`<member>: %q", value)
	}
	g := Group{Name: strings.TrimSpace(parts[0])}
	switch t := GroupType(strings.TrimSpace(parts[1])); t {
	case GroupSelect, GroupURLTest, GroupFallback, GroupLoadBalance:
		g.Type = t
	default:
		return Group{}, fmt.Errorf("unknown proxy group type %q", parts[1])
	}
	if g.Name == "" {
		return Group{}, fmt.Errorf("custom_proxy_group has no name: %q", value)
	}

	members := parts[2:]
	// For the probing types the last two fields are the test URL and its
	// timings, not members. Recognised by shape rather than by position,
	// because a select group can also be the last thing on the line.
	if g.Type != GroupSelect && len(members) >= 2 {
		timings := members[len(members)-1]
		url := members[len(members)-2]
		if strings.HasPrefix(url, "http") {
			g.TestURL = url
			g.Interval, g.Timeout, g.Tolerance = parseTimings(timings)
			members = members[:len(members)-2]
		}
	}

	for _, m := range members {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if lit, found := strings.CutPrefix(m, "[]"); found {
			g.Literals = append(g.Literals, lit)
			continue
		}
		// Everything else filters the subscriber's own proxies by name. Only
		// the first such pattern is used; no preset carries two, and merging
		// them would invent semantics upstream does not define.
		if g.Match != nil {
			continue
		}
		re, err := regexp.Compile(m)
		if err != nil {
			return Group{}, fmt.Errorf("group %q: pattern %q: %w", g.Name, m, err)
		}
		g.Match, g.MatchSource = re, m
	}
	return g, nil
}

// parseTimings reads `<interval>,<timeout>,<tolerance>`, any of which may be
// blank — the presets write "300,,50".
func parseTimings(s string) (interval, timeout, tolerance int) {
	f := strings.Split(s, ",")
	get := func(i int) int {
		if i >= len(f) {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(f[i]))
		if err != nil {
			return 0
		}
		return n
	}
	return get(0), get(1), get(2)
}

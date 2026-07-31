// Package clashrule turns an ACL4SSR-style rule configuration into the
// proxy-groups, rule-providers and rules of a clash-family subscription.
//
// The input format is subconverter's "remote config": an ini whose [custom]
// section maps rule lists to proxy groups and declares the groups themselves.
// This package implements the part of it ACL4SSR actually uses, and nothing
// else — an unrecognised directive is ignored rather than guessed at.
//
// Why in Go rather than a template: the template engine substitutes variables
// and does not loop (see docs/template-system.md). A rule set is a few dozen
// lists expanded against however many nodes a subscriber has, which is exactly
// the iteration the engine deliberately does not do.
package clashrule

import (
	"fmt"
	"strconv"
	"strings"
)

// Ruleset is one `ruleset=` line: a rule list, and the group traffic matching
// it should go to.
type Ruleset struct {
	Group string
	// URL of a rule list, empty for a builtin.
	URL string
	// Builtin is the `[]`-prefixed form: "FINAL" becomes a MATCH rule, and
	// anything else ("GEOIP,CN") is emitted as a literal rule with the group
	// appended.
	Builtin string
}

// Group is one `custom_proxy_group=` line.
type Group struct {
	Name string
	Type string // select | url-test | fallback | load-balance | relay
	// Members in declaration order. A "[]"-prefixed entry names another group
	// or DIRECT/REJECT; anything else is a regular expression matched against
	// proxy names.
	Members   []string
	TestURL   string
	Interval  int
	Timeout   int
	Tolerance int
}

// Config is a parsed rule configuration.
type Config struct {
	Rulesets []Ruleset
	Groups   []Group
}

// ParseINI reads a subconverter remote config.
//
// Order is preserved and load-bearing: clash takes the first rule that
// matches, so a ruleset moved up or down changes where traffic goes.
func ParseINI(b []byte) (Config, error) {
	var cfg Config
	for n, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") ||
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
				return Config{}, fmt.Errorf("line %d: %w", n+1, err)
			}
			cfg.Rulesets = append(cfg.Rulesets, r)
		case "custom_proxy_group":
			g, err := parseGroup(value)
			if err != nil {
				return Config{}, fmt.Errorf("line %d: %w", n+1, err)
			}
			cfg.Groups = append(cfg.Groups, g)
		}
		// Everything else — enable_rule_generator, overwrite_original_rules,
		// the emoji and rename directives — describes what subconverter should
		// do with an upstream subscription it was handed. There is no upstream
		// subscription here; the proxies are ours.
	}
	if len(cfg.Groups) == 0 {
		return Config{}, fmt.Errorf("no custom_proxy_group lines: not an ACL4SSR-style config")
	}
	if len(cfg.Rulesets) == 0 {
		return Config{}, fmt.Errorf("no ruleset lines: nothing to route")
	}
	return cfg, nil
}

// parseRuleset reads `<group>,<url>` or `<group>,[]<builtin>`.
//
// Split on the FIRST comma only: a builtin carries its own commas
// ("[]GEOIP,CN"), and a group name may not contain one.
func parseRuleset(v string) (Ruleset, error) {
	group, rest, ok := strings.Cut(strings.TrimSpace(v), ",")
	if !ok {
		return Ruleset{}, fmt.Errorf("ruleset needs <group>,<url|[]builtin>: %q", v)
	}
	group = strings.TrimSpace(group)
	rest = strings.TrimSpace(rest)
	if group == "" || rest == "" {
		return Ruleset{}, fmt.Errorf("ruleset has an empty field: %q", v)
	}
	if b, isBuiltin := strings.CutPrefix(rest, "[]"); isBuiltin {
		return Ruleset{Group: group, Builtin: b}, nil
	}
	if !strings.HasPrefix(rest, "http://") && !strings.HasPrefix(rest, "https://") {
		return Ruleset{}, fmt.Errorf("ruleset points at neither a URL nor a []builtin: %q", rest)
	}
	return Ruleset{Group: group, URL: rest}, nil
}

// testingTypes are the group types whose last two fields are a probe URL and
// an interval,timeout,tolerance triple rather than members.
var testingTypes = map[string]bool{"url-test": true, "fallback": true, "load-balance": true}

// parseGroup reads name`type`member`member`…[`url`interval,timeout,tolerance].
func parseGroup(v string) (Group, error) {
	f := strings.Split(strings.TrimSpace(v), "`")
	if len(f) < 3 {
		return Group{}, fmt.Errorf("custom_proxy_group needs at least name`type`member: %q", v)
	}
	g := Group{Name: strings.TrimSpace(f[0]), Type: strings.TrimSpace(f[1])}
	if g.Name == "" || g.Type == "" {
		return Group{}, fmt.Errorf("custom_proxy_group has an empty name or type: %q", v)
	}
	rest := f[2:]

	// A testing group ends with the probe URL and the timing triple. Detected
	// by shape rather than by position count, because the member list in
	// between is any length — and a select group whose last member happens to
	// look like a URL must not lose it.
	if testingTypes[g.Type] && len(rest) >= 2 && isTiming(rest[len(rest)-1]) {
		g.TestURL = strings.TrimSpace(rest[len(rest)-2])
		g.Interval, g.Timeout, g.Tolerance = parseTiming(rest[len(rest)-1])
		rest = rest[:len(rest)-2]
	}
	for _, m := range rest {
		if m = strings.TrimSpace(m); m != "" {
			g.Members = append(g.Members, m)
		}
	}
	if len(g.Members) == 0 {
		return Group{}, fmt.Errorf("custom_proxy_group %q has no members", g.Name)
	}
	return g, nil
}

// isTiming reports the "300,,50" shape: comma-separated, and every field that
// is present is a number.
func isTiming(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, ",") {
		return false
	}
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(part)); err != nil {
			return false
		}
	}
	return true
}

// parseTiming reads "interval,timeout,tolerance", any of which may be blank.
func parseTiming(s string) (interval, timeout, tolerance int) {
	parts := strings.Split(strings.TrimSpace(s), ",")
	get := func(i int) int {
		if i >= len(parts) {
			return 0
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return 0
		}
		return n
	}
	return get(0), get(1), get(2)
}

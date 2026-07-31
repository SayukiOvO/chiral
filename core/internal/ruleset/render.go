package ruleset

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

// Rendered is the routing half of a Clash configuration: the sections that
// describe where traffic goes, ready to append to the proxies the subscription
// already carries.
type Rendered struct {
	ProxyGroups string
	// RuleProviders is empty when the config has no fetched lists.
	RuleProviders string
	Rules         string
	// Providers maps the provider name used in the YAML to the upstream URL
	// its contents came from, so the panel can serve each one.
	Providers map[string]string
	// Dropped names the groups that ended up with no members and were
	// removed. Not an error — a preset with region groups always drops the
	// regions a fleet has no nodes in — but the operator should be able to
	// see it rather than wonder where a group went.
	Dropped []string
}

// providerInterval is how often a client refetches a rule list, in seconds.
// A day: these lists change on the order of weeks, and the panel is the one
// serving them, so a shorter interval spends the subscriber's battery to learn
// nothing.
const providerInterval = 86400

// Render turns a parsed preset and the subscriber's own proxy names into Clash
// routing. providerBase is the URL prefix the client fetches rule lists from,
// with no trailing slash.
//
// Groups that match none of the proxies are removed, along with every
// reference to them. Clash refuses a configuration containing a proxy group
// with an empty proxies list, so a preset with region groups — and every
// popular one has them — would otherwise be unusable on any fleet that does
// not happen to have a node in each of those regions. Removing the reference
// as well as the group is the part that is easy to miss: a member pointing at
// a group that no longer exists fails the same way.
func Render(cfg Config, proxyNames []string, providerBase string) Rendered {
	live, dropped := resolveGroups(cfg.Groups, proxyNames)

	out := Rendered{Providers: map[string]string{}, Dropped: dropped}

	var gb strings.Builder
	gb.WriteString("proxy-groups:\n")
	for _, g := range live {
		fmt.Fprintf(&gb, "  - name: %s\n    type: %s\n", yamlScalar(g.Name), g.Type)
		if g.Type != GroupSelect {
			url := g.TestURL
			if url == "" {
				url = "http://www.gstatic.com/generate_204"
			}
			fmt.Fprintf(&gb, "    url: %s\n", url)
			if g.Interval > 0 {
				fmt.Fprintf(&gb, "    interval: %d\n", g.Interval)
			}
			if g.Tolerance > 0 {
				fmt.Fprintf(&gb, "    tolerance: %d\n", g.Tolerance)
			}
		}
		gb.WriteString("    proxies:\n")
		for _, m := range g.members {
			fmt.Fprintf(&gb, "      - %s\n", yamlScalar(m))
		}
	}
	out.ProxyGroups = gb.String()

	defined := make(map[string]bool, len(live))
	for _, g := range live {
		defined[g.Name] = true
	}

	var pb, rb strings.Builder
	rb.WriteString("rules:\n")
	// The panel itself, always direct, ahead of everything else.
	//
	// Without this the preset's catch-all sends the panel through the proxy,
	// and two things follow. The operator loses the console exactly when the
	// proxy breaks — which is the moment they need it — and the client cannot
	// load these rules at all, because the rule lists live on the panel and
	// fetching them is itself routed by the rules being fetched. The observed
	// form is a client logging "--> panel:443 match Match using <proxy>" for
	// its own provider requests.
	if host := providerHost(providerBase); host != "" {
		fmt.Fprintf(&rb, "  - DOMAIN,%s,DIRECT\n", yamlScalar(host))
	}
	names := newNamer()
	for _, r := range cfg.Rules {
		// A rule whose policy was dropped has nowhere to go. Keeping it would
		// make the client reject the file.
		if !defined[r.Group] && !isBuiltinPolicy(r.Group) {
			continue
		}
		if r.URL == "" {
			fmt.Fprintf(&rb, "  - %s,%s\n", r.Inline, yamlScalar(r.Group))
			continue
		}
		name, fresh := names.of(r.URL)
		if fresh {
			out.Providers[name] = r.URL
			if pb.Len() == 0 {
				pb.WriteString("rule-providers:\n")
			}
			fmt.Fprintf(&pb, "  %s:\n    type: http\n    behavior: classical\n"+
				"    format: yaml\n    url: %s/%s.yaml\n    path: ./chiral/%s.yaml\n    interval: %d\n",
				name, providerBase, name, name, providerInterval)
		}
		fmt.Fprintf(&rb, "  - RULE-SET,%s,%s\n", name, yamlScalar(r.Group))
	}
	out.RuleProviders = pb.String()
	out.Rules = rb.String()
	return out
}

// resolved is a group with its membership worked out.
type resolved struct {
	Group
	members []string
}

// resolveGroups expands each group's members and removes the ones left empty,
// repeatedly: dropping a region group can empty the selector that referenced
// it, which can in turn empty another.
func resolveGroups(groups []Group, proxyNames []string) ([]resolved, []string) {
	alive := make(map[string]bool, len(groups))
	for _, g := range groups {
		alive[g.Name] = true
	}

	var dropped []string
	for {
		changed := false
		for _, g := range groups {
			if !alive[g.Name] {
				continue
			}
			if len(expand(g, proxyNames, alive)) == 0 {
				alive[g.Name] = false
				dropped = append(dropped, g.Name)
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	out := make([]resolved, 0, len(groups))
	for _, g := range groups {
		if !alive[g.Name] {
			continue
		}
		out = append(out, resolved{Group: g, members: expand(g, proxyNames, alive)})
	}
	sort.Strings(dropped)
	return out, dropped
}

// expand lists a group's members: its literals that still exist, then the
// proxies its pattern selects.
func expand(g Group, proxyNames []string, alive map[string]bool) []string {
	out := make([]string, 0, len(g.Literals)+len(proxyNames))
	for _, lit := range g.Literals {
		if isBuiltinPolicy(lit) || alive[lit] {
			out = append(out, lit)
		}
	}
	if g.Match != nil {
		for _, p := range proxyNames {
			if g.Match.MatchString(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// isBuiltinPolicy reports the outcomes Clash provides itself, which are always
// available as group members and rule targets.
func isBuiltinPolicy(name string) bool {
	switch strings.ToUpper(name) {
	case "DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE":
		return true
	}
	return false
}

// namer turns rule-list URLs into stable, unique, filesystem-safe provider
// names. Stable because the name appears in the client's on-disk cache path,
// and a name that changes between fetches leaves orphans behind.
type namer struct {
	byURL map[string]string
	used  map[string]bool
}

func newNamer() *namer {
	return &namer{byURL: map[string]string{}, used: map[string]bool{}}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (n *namer) of(url string) (string, bool) {
	if got, ok := n.byURL[url]; ok {
		return got, false
	}
	base := strings.TrimSuffix(path.Base(url), path.Ext(url))
	base = unsafeName.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "rules"
	}
	name := base
	for i := 2; n.used[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	n.used[name] = true
	n.byURL[url] = name
	return name, true
}

// yamlScalar quotes a value when YAML would otherwise read it as something
// else. The group names in these presets start with emoji, which is fine
// unquoted, but a name that begins with punctuation or looks like a number is
// not — and a mis-parsed group name is a config the client loads with the
// wrong routing rather than one it refuses.
func yamlScalar(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ":#{}[],&*?|<>=!%@`\"'\n") || strings.TrimSpace(s) != s {
		return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	switch strings.ToLower(s) {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return `"` + s + `"`
	}
	return s
}

// providerHost is the panel's own hostname, taken from the URL its rule lists
// are served under.
func providerHost(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

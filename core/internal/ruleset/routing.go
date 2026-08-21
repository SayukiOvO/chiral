package ruleset

import (
	"fmt"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// For renders a subscriber's routing sections. Satisfies subscription.Routing.
//
// Everything comes out of the database. Rendering a subscription never
// fetches: the subscriber is often on the network the proxy exists to get them
// off, so a render that reaches for GitHub is a render that hangs.
func (s *Service) For(u store.User, proxyNames []string, providerBase string) (groups, providers, rules string, err error) {
	if u.EffectiveRulesetID == "" {
		return "", "", "", nil
	}
	r, err := s.st.GetRuleset(u.EffectiveRulesetID)
	if err != nil {
		return "", "", "", err
	}
	if r.INI == "" {
		return "", "", "", fmt.Errorf("ruleset %q has never been fetched", r.Name)
	}
	cfg, err := ParseINI(r.INI)
	if err != nil {
		return "", "", "", fmt.Errorf("ruleset %q: %w", r.Name, err)
	}
	out := Render(cfg, proxyNames, providerBase)
	return out.ProxyGroups, out.RuleProviders, out.Rules, nil
}

// ListFor resolves one provider name for a subscriber to the cached list body,
// converted to a Clash provider payload.
//
// Resolved through the subscriber's own ruleset rather than from a global
// name table: the provider name is derived per render, so the same name means
// different lists for two subscribers on different rulesets. Looking it up any
// other way would serve one subscriber another's rules.
func (s *Service) ListFor(u store.User, name string) (string, bool, error) {
	if u.EffectiveRulesetID == "" {
		return "", false, nil
	}
	r, err := s.st.GetRuleset(u.EffectiveRulesetID)
	if err != nil {
		return "", false, err
	}
	if r.INI == "" {
		return "", false, nil
	}
	cfg, err := ParseINI(r.INI)
	if err != nil {
		return "", false, err
	}
	// Names are assigned in the same order Render assigns them, so the same
	// input produces the same mapping.
	names := newNamer()
	for _, rule := range cfg.Rules {
		if rule.URL == "" {
			continue
		}
		got, _ := names.of(rule.URL)
		if got != name {
			continue
		}
		list, err := s.st.GetRuleList(rule.URL)
		if err != nil {
			// Referenced but not cached: the ruleset was fetched and this
			// list was not. An empty payload is the honest answer — the rule
			// simply matches nothing — and it keeps the client's config
			// loadable instead of leaving it with a provider that 500s.
			return "payload:\n", true, nil
		}
		return ProviderYAML(list.Body), true, nil
	}
	return "", false, nil
}

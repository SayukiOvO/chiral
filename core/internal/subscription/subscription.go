// Package subscription renders a user's access points into whatever format
// their client speaks.
//
// There is no conversion here and never will be: each client kind has a
// hand-written template per profile, so a config using xhttp up/down split or
// post-quantum key exchange comes out exactly as written. See
// docs/template-system.md.
package subscription

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// Client kinds, matching the keys profiles store templates under.
const (
	ClientXrayJSON = "xray-json"
	ClientClash    = "clash"
	ClientVlessURI = "vless-uri"
	ClientStash    = "stash"
)

// Kinds lists the client kinds the panel knows about, for the UI's picker.
func Kinds() []string {
	return []string{ClientXrayJSON, ClientClash, ClientVlessURI, ClientStash}
}

// Contexts supplies render contexts. Implemented by the profile service, which
// owns variable resolution; declared here so this package does not reach into
// it for anything else.
type Contexts interface {
	ClientContext(profileID, nodeID string) (*template.Context, error)
}

// Routing renders a subscriber's routing rules. Implemented by the ruleset
// service; an interface so this package neither fetches nor parses anything.
type Routing interface {
	// For returns the proxy-groups, rule-providers and rules sections for a
	// subscriber, given the proxy names their subscription contains and the
	// base URL their client should fetch rule lists from. An empty first
	// return means the subscriber has no ruleset.
	For(u store.User, proxyNames []string, providerBase string) (groups, providers, rules string, err error)
}

type Service struct {
	st  *store.Store
	ctx Contexts
	// routing is nil until a ruleset service is wired in, in which case
	// subscriptions carry the two groups this package builds itself.
	routing Routing
	// externals contributes proxies from other people's subscriptions. Nil
	// leaves subscriptions carrying only this fleet's own nodes.
	externals Externals
	// publicURL is where a client reaches this panel, for the rule-provider
	// URLs. Empty leaves rules out entirely rather than emitting providers
	// pointing at a relative path no client can resolve.
	publicURL string
}

func NewService(st *store.Store, ctx Contexts) *Service {
	return &Service{st: st, ctx: ctx}
}

// Externals contributes proxies this panel does not run. Implemented by the
// external service.
type Externals interface {
	// Fragments returns clash proxy entries. chainName maps a fleet node id to
	// the name that node carries in this subscription, because a chained proxy
	// has to reference a name the same document defines.
	Fragments(chainName func(nodeID string) string) ([]string, error)
}

// EnableRouting wires per-subscriber routing rules. Called at startup.
func (s *Service) EnableRouting(r Routing, publicURL string) {
	s.routing, s.publicURL = r, publicURL
}

// EnableExternals wires in other people's nodes. Called at startup.
func (s *Service) EnableExternals(e Externals) { s.externals = e }

// Result is a rendered subscription, ready to serve.
type Result struct {
	Client string
	Body   string
	// ContentType and Filename are what the client expects to receive.
	ContentType string
	Filename    string
	// Fragments is how many access points made it in, for logging and for
	// telling an operator that a subscription came out empty.
	Fragments int
	// Skipped explains each access point that could not be rendered. A
	// subscription with fragments AND skips is degraded rather than broken,
	// and the operator is the only one who can tell the difference.
	Skipped []string
}

// Render assembles the subscription for a user.
//
// It walks every profile the user is entitled to and every node bound to that
// profile, rendering one fragment per pair. A profile with no template for the
// requested client is skipped rather than failing the whole subscription: a
// client that cannot express one access point should still receive the others.
func (s *Service) Render(u store.User, client, token string) (Result, error) {
	profileIDs, err := s.st.UserProfileIDs(u.ID)
	if err != nil {
		return Result{}, err
	}
	sort.Strings(profileIDs)

	var fragments []string
	// Access points that could not be rendered, with the reason. Not an error:
	// see the skip below.
	var skipped []string
	for _, pid := range profileIDs {
		tmpl, err := s.templateFor(pid, client)
		if err != nil {
			return Result{}, err
		}
		if tmpl == "" {
			continue
		}
		nodeIDs, err := s.st.ProfileNodeIDs(pid)
		if err != nil {
			return Result{}, err
		}
		sort.Strings(nodeIDs)
		for _, nid := range nodeIDs {
			cred, err := s.st.FindCredential(u.ID, pid, nid)
			if err != nil {
				// No credential yet means this access point has not been
				// assembled; it is simply not available to the user.
				if store.IsNotFound(err) {
					continue
				}
				return Result{}, err
			}
			ctx, err := s.ctx.ClientContext(pid, nid)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s on %s: %v", pid, nid, err))
				continue
			}
			// ClientContext has already stripped secret components, so a
			// template referencing a private key fails here rather than
			// leaking it into a subscription.
			//
			// One unrenderable access point is skipped, not fatal — the same
			// treatment the missing credential above already gets, and for the
			// same reason. A subscription is a LIST of access points, and one
			// bad template in one profile used to blank the whole list: every
			// healthy node the customer was entitled to disappeared along with
			// it, for every user bound to that profile. Losing one line is a
			// degraded subscription; losing all of them is an outage.
			//
			// The reasons are collected and returned so the caller can say so
			// rather than serve a quietly short list.
			body, err := ctx.With(user.CredentialVars(cred)).Render(tmpl)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s on %s: %v", pid, nid, err))
				continue
			}
			fragments = append(fragments, strings.TrimSpace(body))
		}
	}

	r := s.assemble(u, token, client, fragments)
	r.Skipped = skipped
	return r, nil
}

func (s *Service) templateFor(profileID, client string) (string, error) {
	templates, err := s.st.ClientTemplates(profileID)
	if err != nil {
		return "", err
	}
	return templates[client], nil
}

// assemble joins the fragments the way each client expects to receive them.
func (s *Service) assemble(u store.User, token, client string, fragments []string) Result {
	r := Result{Client: client, Fragments: len(fragments)}
	switch client {
	case ClientVlessURI:
		// One share link per line. Some clients additionally expect base64;
		// the raw list is what modern v2rayN and friends accept.
		r.Body = strings.Join(fragments, "\n")
		r.ContentType = "text/plain; charset=utf-8"
		r.Filename = s.subscriptionName()
	case ClientClash, ClientStash:
		// The templates carry YAML proxy entries; the surrounding document is
		// the panel's job so a client gets a usable file rather than a
		// fragment. Indentation matters, so each entry is emitted as a list
		// item with its lines shifted.
		// Nodes this panel does not run, added before the groups are built so
		// they are selectable like any other. A chained one names a node of
		// this fleet, which has to be one this subscription actually carries —
		// hence the lookup over the fragments already rendered.
		fragments = append(fragments, s.externalFragments(fragments)...)

		var b strings.Builder
		b.WriteString("proxies:\n")
		for _, f := range fragments {
			b.WriteString(indentAsListItem(f))
		}
		// A ruleset replaces the two groups this package builds: it brings its
		// own, and emitting both would give the client two competing sets of
		// policies for the same proxies.
		groups, providers, rules := s.routingFor(u, token, fragments)
		if groups != "" {
			b.WriteString(groups)
			b.WriteString(providers)
			b.WriteString(rules)
		} else {
			b.WriteString(proxyGroupSection(fragments))
		}
		r.Body = b.String()
		r.ContentType = "text/yaml; charset=utf-8"
		r.Filename = s.subscriptionName()
	default: // xray-json
		// A full Xray client config: the fragments are outbounds.
		var b strings.Builder
		b.WriteString("{\n  \"outbounds\": [\n")
		for i, f := range fragments {
			b.WriteString(indentLines(f, "    "))
			if i < len(fragments)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("  ]\n}\n")
		r.Body = b.String()
		r.ContentType = "application/json; charset=utf-8"
		r.Filename = s.subscriptionName()
	}
	return r
}

// indentAsListItem renders one YAML mapping as an item of the proxies list.
//
// Relative indentation is preserved, not flattened: a proxy entry routinely
// nests (reality-opts, ws-opts, headers), and trimming every line to the same
// depth would silently reparent those keys onto the proxy itself — still
// valid YAML, but a different and broken config.
func indentAsListItem(fragment string) string {
	lines := strings.Split(fragment, "\n")

	// The base indent is the first non-blank line's; everything else is
	// re-anchored relative to it.
	base := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		base = len(l) - len(strings.TrimLeft(l, " \t"))
		break
	}
	if base < 0 {
		return ""
	}

	var b strings.Builder
	first := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		indent := len(l) - len(strings.TrimLeft(l, " \t"))
		rel := indent - base
		if rel < 0 {
			rel = 0
		}
		if first {
			b.WriteString("  - " + strings.TrimSpace(l) + "\n")
			first = false
			continue
		}
		b.WriteString("    " + strings.Repeat(" ", rel) + strings.TrimSpace(l) + "\n")
	}
	return b.String()
}

// autoTestURL is what the url-test group probes. Chosen because it answers 204
// with an empty body from almost everywhere and is the de facto default across
// clash-family clients, so a user comparing Chiral's subscription against
// another one sees the same latency numbers rather than a different endpoint's.
const autoTestURL = "http://www.gstatic.com/generate_204"

// autoTestInterval is how often a client re-probes, in seconds. Five minutes:
// often enough to notice a node going bad, rare enough that a subscriber with
// twenty nodes is not generating a probe every few seconds all day.
const autoTestInterval = 300

// proxyGroupSection gives clash-family clients something selectable, which
// they need in order to use the proxies at all.
//
// Two groups, and the order matters: "Chiral" comes first and stays a manual
// `select`, because a subscriber who has learned which node works for them must
// not have that taken away by an upgrade. "Chiral 自动" is a `url-test` nested
// inside it, so latency-based selection is one tap away for everyone else.
//
// Nesting the automatic group as a member of the manual one, rather than
// putting them side by side, is what makes "auto" a choice within the same
// control instead of a second control the user has to know about.
func proxyGroupSection(fragments []string) string {
	names := make([]string, 0, len(fragments))
	for _, f := range fragments {
		if n := yamlName(f); n != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	const autoName = "Chiral 自动"
	var b strings.Builder
	b.WriteString("proxy-groups:\n")

	b.WriteString("  - name: Chiral\n    type: select\n    proxies:\n")
	b.WriteString("      - " + quoteName(autoName) + "\n")
	for _, n := range names {
		b.WriteString("      - " + quoteName(n) + "\n")
	}

	b.WriteString("  - name: " + quoteName(autoName) + "\n    type: url-test\n")
	fmt.Fprintf(&b, "    url: %s\n    interval: %d\n    tolerance: 50\n", autoTestURL, autoTestInterval)
	b.WriteString("    proxies:\n")
	for _, n := range names {
		b.WriteString("      - " + quoteName(n) + "\n")
	}
	return b.String()
}

// quoteName wraps a proxy name so YAML reads it as the name it is.
//
// A group member is written as a bare scalar, which is fine for the emoji and
// spaces these names are full of and wrong the moment one contains a colon:
// "Tokyo: 01" as a list item is a mapping, not a string, and the client then
// cannot find a proxy by that name. Single quotes because YAML processes no
// escapes inside them.
func quoteName(s string) string {
	if s == "" {
		return `''`
	}
	if !strings.ContainsAny(s, ":#{}[],&*?|<>=!%@`\"'\\\n") && strings.TrimSpace(s) == s {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// yamlName pulls the `name:` out of a rendered proxy entry, in either the
// block form (`name: x`) or the inline-mapping form (`{name: x, ...}`).
func yamlName(fragment string) string {
	for _, line := range strings.Split(fragment, "\n") {
		t := strings.TrimSpace(line)
		// An inline mapping puts the name after the brace, and ends the value
		// at the first comma.
		inline := false
		if after, found := strings.CutPrefix(t, "{"); found {
			t = strings.TrimSpace(after)
			inline = true
		}
		rest, found := strings.CutPrefix(t, "name:")
		if !found {
			continue
		}
		if inline {
			rest = strings.SplitN(rest, ",", 2)[0]
		}
		rest = strings.TrimSuffix(strings.TrimSpace(rest), "}")
		return strings.Trim(strings.TrimSpace(rest), `"'`)
	}
	return ""
}

func indentLines(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

// DetectClient decides which template to render for a request: an explicit
// ?client= wins, otherwise the User-Agent is matched against the clients we
// know. Anything unrecognised gets xray-json, the most complete format.
func DetectClient(r *http.Request) string {
	if c := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("client"))); c != "" {
		if known(c) {
			return c
		}
	}
	return ClientForUserAgent(r.UserAgent())
}

func known(c string) bool {
	for _, k := range Kinds() {
		if k == c {
			return true
		}
	}
	return false
}

// ClientForUserAgent maps a subscription fetcher's UA to a client kind.
//
// Order matters: stash identifies itself with a UA that also mentions clash,
// so the more specific match has to come first.
func ClientForUserAgent(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "stash"):
		return ClientStash
	case strings.Contains(l, "clash"), strings.Contains(l, "mihomo"),
		strings.Contains(l, "meta"):
		return ClientClash
	case strings.Contains(l, "v2rayn"), strings.Contains(l, "v2rayng"),
		strings.Contains(l, "nekobox"), strings.Contains(l, "v2rayu"):
		return ClientVlessURI
	default:
		return ClientXrayJSON
	}
}

// routingFor renders the subscriber's ruleset, or nothing when they have none.
//
// Failures here degrade rather than propagate: a subscription without rules is
// usable and a subscription that 500s is not, and the cause — an unfetched
// ruleset, a panel with no public URL — is something only the operator can
// fix, so it is reported to them rather than to the subscriber's client.
func (s *Service) routingFor(u store.User, token string, fragments []string) (groups, providers, rules string) {
	if s.routing == nil || s.publicURL == "" || u.RulesetID == "" || token == "" {
		return "", "", ""
	}
	names := make([]string, 0, len(fragments))
	for _, f := range fragments {
		if n := yamlName(f); n != "" {
			names = append(names, n)
		}
	}
	base := strings.TrimSuffix(s.publicURL, "/") + "/sub/" + token + "/rules"
	g, p, rl, err := s.routing.For(u, names, base)
	if err != nil {
		return "", "", ""
	}
	return g, p, rl
}

// externalFragments renders the external proxies for this subscription.
//
// The chain resolver looks the node up among the fragments this subscription
// already carries: a dialer-proxy naming something the document does not
// define makes the whole configuration unloadable, so a chain through a node
// the subscriber is not entitled to has to drop the proxy rather than emit a
// dangling reference. That is the external service's decision; this supplies
// the lookup it needs to make it.
func (s *Service) externalFragments(own []string) []string {
	if s.externals == nil {
		return nil
	}
	names := make(map[string]string, len(own))
	for _, f := range own {
		if n := yamlName(f); n != "" {
			names[n] = n
		}
	}
	out, err := s.externals.Fragments(func(nodeID string) string {
		return s.nodeProxyName(nodeID, names)
	})
	if err != nil {
		return nil
	}
	return out
}

// nodeProxyName resolves a fleet node id to the name it appears under here.
func (s *Service) nodeProxyName(nodeID string, present map[string]string) string {
	n, err := s.st.GetNode(nodeID)
	if err != nil {
		return ""
	}
	for _, candidate := range []string{n.DisplayName, n.Name} {
		if candidate == "" {
			continue
		}
		if got, ok := present[candidate]; ok {
			return got
		}
	}
	return ""
}

// defaultSubscriptionName is used until an operator picks one.
const defaultSubscriptionName = "chiral"

// subscriptionName is what a client shows the subscription under.
//
// Clash-family clients take the profile's name from the Content-Disposition
// filename and show it verbatim, extension and all. So there is no extension:
// the operator picks a name and that is the name, rather than a name with
// ".yaml" stuck on the end of it in every subscriber's profile list.
func (s *Service) subscriptionName() string {
	// A Service with no store has no settings to read, and therefore the
	// default. Assembly used to be a free function and several tests still
	// construct a bare Service to exercise it.
	name := defaultSubscriptionName
	if s.st != nil {
		name = sanitiseFilename(s.st.Setting(store.SettingSubscriptionName, defaultSubscriptionName))
	}
	if name == "" {
		name = defaultSubscriptionName
	}
	return name
}

// sanitiseFilename keeps a name usable in a Content-Disposition header and as
// a file on the subscriber's disk.
//
// Quotes and backslashes would terminate or escape the header's quoted string,
// and path separators would make the client write outside the directory it
// meant to. Everything else — spaces, emoji, Chinese — is left alone, because
// this is a label a person chose and mangling it would be the wrong kind of
// safe.
func sanitiseFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Map(func(r rune) rune {
		switch r {
		case '"', '\\', '/', '\n', '\r', 0:
			return -1
		}
		return r
	}, name)
	return strings.TrimSpace(name)
}

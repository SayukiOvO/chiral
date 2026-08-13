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

	"github.com/SayukiOvO/chiral/core/internal/external"
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
	// Fragments returns clash proxy entries, each carrying the place the
	// operator gave it. chainName maps a fleet node id to the name that node
	// carries in this subscription, because a chained proxy has to reference a
	// name the same document defines.
	Fragments(denied map[string]struct{}, chainName func(nodeID string) string) ([]external.Fragment, error)
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

	// Nodes this subscriber has been denied. Empty for everyone until an
	// operator says otherwise, so a fleet that does not need per-user control
	// pays nothing for it.
	denied, err := s.st.UserNodeDenies(u.ID)
	if err != nil {
		return Result{}, err
	}
	deniedProxies, err := s.st.UserExternalDenies(u.ID)
	if err != nil {
		return Result{}, err
	}
	deniedRelays, err := s.st.UserRelayDenies(u.ID)
	if err != nil {
		return Result{}, err
	}
	// Relayed exits take their place in the one ordered list like anything
	// else, keyed by the external proxy they stand for.
	exitOrder, exitTie := map[string]int{}, map[string]string{}
	if all, err := s.st.EnabledExternalProxies(); err == nil {
		for i, p := range all {
			exitOrder[p.ID] = p.SortOrder
			exitTie[p.ID] = fmt.Sprintf("1:%08d", i)
		}
	}
	// And so do lines out through another of our nodes.
	relayOrder, relayTie := map[string]int{}, map[string]string{}
	if all, err := s.st.ListNodeRelays(); err == nil {
		for i, rl := range all {
			relayOrder[rl.ID] = rl.SortOrder
			relayTie[rl.ID] = fmt.Sprintf("2:%08d", i)
		}
	}

	// The operator's arrangement, by node. Absent means never placed, which is
	// 0, which sorts to the end — the zero value is the right answer here.
	nodes, err := s.st.ListNodes()
	if err != nil {
		return Result{}, err
	}
	nodeOrder := make(map[string]int, len(nodes))
	// The tie is what decides the never-placed tail, and it has to agree with
	// the console's list exactly — a subscription that disagrees with the page
	// the operator is looking at is the thing this feature exists to fix. So it
	// is the node's rank in the same query the console reads, prefixed to keep
	// every fleet node ahead of every external one, rather than an id: ids are
	// random hex, and sorting by them interleaved the two kinds arbitrarily.
	nodeTie := make(map[string]string, len(nodes))
	for i, n := range nodes {
		nodeOrder[n.ID] = n.SortOrder
		nodeTie[n.ID] = fmt.Sprintf("0:%08d", i)
	}

	// placed carries each rendered access point with the position the operator
	// gave it, because the fleet's and the external nodes' are one sequence and
	// the two are produced by different loops.
	var placed []placedFragment
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
		// Sorted only to keep this loop deterministic; the order that reaches
		// the subscriber is decided below, by the operator's list.
		sort.Strings(nodeIDs)
		for _, nid := range nodeIDs {
			if _, no := denied[nid]; no {
				continue
			}
			cred, err := s.st.FindCredential(u.ID, pid, nid, "")
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
			placed = append(placed, placedFragment{
				body:  strings.TrimSpace(body),
				order: nodeOrder[nid],
				tie:   nodeTie[nid],
			})

			// One more entry per exit this node relays for. Same node, same
			// transport, same template — a different credential, and the
			// exit's name in place of the node's, so the subscriber sees a
			// line that goes where they think it goes and never learns whose
			// machine is at the far end.
			exits, err := s.relayedExits(nid)
			if err != nil {
				return Result{}, err
			}
			for _, ex := range exits {
				if _, no := deniedProxies[ex.ID]; no {
					continue
				}
				ec, err := s.st.FindCredential(u.ID, pid, nid, ex.ID)
				if err != nil {
					if store.IsNotFound(err) {
						continue
					}
					return Result{}, err
				}
				label := ex.Label()
				vars := user.CredentialVars(ec)
				vars["node.display_name"] = label
				vars["node.name"] = label
				body, err := ctx.With(vars).Render(tmpl)
				if err != nil {
					skipped = append(skipped, fmt.Sprintf("%s via %s: %v", pid, label, err))
					continue
				}
				placed = append(placed, placedFragment{
					body:  strings.TrimSpace(body),
					order: exitOrder[ex.ID],
					tie:   exitTie[ex.ID],
				})
			}

			// And one per line that starts here and lands on another of our
			// nodes. Same reasoning as above, with one difference worth
			// stating: nothing is being hidden here — both ends are ours and
			// the subscriber can see the exit as its own line too. What the
			// relay adds is a way IN to that exit for someone whose route to
			// it directly is bad, so it is a separate line with a name of its
			// own rather than a replacement for either node's.
			//
			// A denied entry node has already skipped this whole loop, which
			// is right: handing someone a line through a box means handing
			// them that box's address and a credential on it.
			relays, err := s.st.RelaysFromEntry(nid)
			if err != nil {
				return Result{}, err
			}
			for _, rl := range relays {
				if _, no := deniedRelays[rl.ID]; no {
					continue
				}
				rc, err := s.st.FindCredentialForExit(u.ID, pid, nid, "", rl.ID)
				if err != nil {
					if store.IsNotFound(err) {
						continue
					}
					return Result{}, err
				}
				vars := user.CredentialVars(rc)
				vars["node.display_name"] = rl.Label
				vars["node.name"] = rl.Label
				body, err := ctx.With(vars).Render(tmpl)
				if err != nil {
					skipped = append(skipped, fmt.Sprintf("%s via %s: %v", pid, rl.Label, err))
					continue
				}
				placed = append(placed, placedFragment{
					body:  strings.TrimSpace(body),
					order: relayOrder[rl.ID],
					tie:   relayTie[rl.ID],
				})
			}
		}
	}

	r := s.assemble(u, token, client, placed)
	r.Skipped = skipped
	return r, nil
}

// templateFor picks the template a SUBSCRIBER gets, which is the served set,
// not the stored set. A template can exist purely as machinery — the relay
// dial leg, the upgrade probe — without every entitled user's subscription
// growing a line through that access point.
func (s *Service) templateFor(profileID, client string) (string, error) {
	templates, err := s.st.ServedClientTemplates(profileID)
	if err != nil {
		return "", err
	}
	return templates[client], nil
}

// relayedExits lists the external nodes a given node relays for.
//
// These are the ones whose chain target is that node. They do NOT appear in a
// subscription as proxies of their own — the subscriber is not supposed to
// learn the provider's address, which is the whole point of relaying — unless
// the operator has said otherwise for one of them.
func (s *Service) relayedExits(nodeID string) ([]store.ExternalProxy, error) {
	all, err := s.st.EnabledExternalProxies()
	if err != nil {
		return nil, err
	}
	var out []store.ExternalProxy
	for _, p := range all {
		if p.ChainNodeID == nodeID {
			out = append(out, p)
		}
	}
	return out, nil
}

// placedFragment is one rendered access point and where the operator put it.
type placedFragment struct {
	body string
	// order is the operator's position, 0 for never placed.
	order int
	// tie keeps the never-placed tail in a stable, reproducible order.
	tie string
}

// orderFragments sorts one list of access points into the order the subscriber
// sees, and returns just the bodies.
//
// This is the only ordering there is. The proxy-group generator walks the
// rendered proxy list and takes what its pattern matches, in the order it finds
// it, so the members of 节点选择 and of every other group come out in this order
// too. There is deliberately no second setting for that: two settings for one
// arrangement is two settings that can disagree.
//
// Never-placed entries go after every placed one rather than before, so adding
// a node puts it at the end of the list instead of the front of it.
func orderFragments(in []placedFragment) []string {
	sorted := make([]placedFragment, len(in))
	copy(sorted, in)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if (a.order == 0) != (b.order == 0) {
			return b.order == 0
		}
		if a.order != b.order {
			return a.order < b.order
		}
		return a.tie < b.tie
	})
	out := make([]string, 0, len(sorted))
	for _, f := range sorted {
		out = append(out, f.body)
	}
	return out
}

// assemble joins the fragments the way each client expects to receive them.
func (s *Service) assemble(u store.User, token, client string, placed []placedFragment) Result {
	fragments := orderFragments(placed)
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
		// Merged into the one sequence rather than appended after it, so an
		// external exit can sit next to the fleet node it is chained through.
		placed = append(placed, s.externalFragments(u, fragments)...)
		fragments = orderFragments(placed)
		// Counted after they are merged, not before. A subscriber whose only
		// remaining access is external — every fleet node denied, or none
		// bound yet — otherwise looked empty to the caller and was refused
		// with "no access point available" while holding a perfectly good
		// list of them.
		r.Fragments = len(fragments)

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
func (s *Service) externalFragments(u store.User, own []string) []placedFragment {
	if s.externals == nil {
		return nil
	}
	denied, err := s.st.UserExternalDenies(u.ID)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(own))
	for _, f := range own {
		if n := yamlName(f); n != "" {
			names[n] = n
		}
	}
	out, err := s.externals.Fragments(denied, func(nodeID string) string {
		return s.nodeProxyName(nodeID, names)
	})
	if err != nil {
		return nil
	}
	placed := make([]placedFragment, 0, len(out))
	for _, f := range out {
		placed = append(placed, placedFragment{body: f.Body, order: f.Order, tie: f.Tie})
	}
	return placed
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

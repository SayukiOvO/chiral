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

type Service struct {
	st  *store.Store
	ctx Contexts
}

func NewService(st *store.Store, ctx Contexts) *Service {
	return &Service{st: st, ctx: ctx}
}

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
}

// Render assembles the subscription for a user.
//
// It walks every profile the user is entitled to and every node bound to that
// profile, rendering one fragment per pair. A profile with no template for the
// requested client is skipped rather than failing the whole subscription: a
// client that cannot express one access point should still receive the others.
func (s *Service) Render(u store.User, client string) (Result, error) {
	profileIDs, err := s.st.UserProfileIDs(u.ID)
	if err != nil {
		return Result{}, err
	}
	sort.Strings(profileIDs)

	var fragments []string
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
				return Result{}, err
			}
			// ClientContext has already stripped secret components, so a
			// template referencing a private key fails loudly here rather
			// than leaking it into a subscription.
			body, err := ctx.With(user.CredentialVars(cred)).Render(tmpl)
			if err != nil {
				return Result{}, fmt.Errorf("rendering %s fragment: %w", client, err)
			}
			fragments = append(fragments, strings.TrimSpace(body))
		}
	}

	return assemble(client, fragments), nil
}

func (s *Service) templateFor(profileID, client string) (string, error) {
	templates, err := s.st.ClientTemplates(profileID)
	if err != nil {
		return "", err
	}
	return templates[client], nil
}

// assemble joins the fragments the way each client expects to receive them.
func assemble(client string, fragments []string) Result {
	r := Result{Client: client, Fragments: len(fragments)}
	switch client {
	case ClientVlessURI:
		// One share link per line. Some clients additionally expect base64;
		// the raw list is what modern v2rayN and friends accept.
		r.Body = strings.Join(fragments, "\n")
		r.ContentType = "text/plain; charset=utf-8"
		r.Filename = "chiral.txt"
	case ClientClash, ClientStash:
		// The templates carry YAML proxy entries; the surrounding document is
		// the panel's job so a client gets a usable file rather than a
		// fragment. Indentation matters, so each entry is emitted as a list
		// item with its lines shifted.
		var b strings.Builder
		b.WriteString("proxies:\n")
		for _, f := range fragments {
			b.WriteString(indentAsListItem(f))
		}
		b.WriteString(proxyGroupSection(fragments))
		r.Body = b.String()
		r.ContentType = "text/yaml; charset=utf-8"
		r.Filename = "chiral.yaml"
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
		r.Filename = "chiral.json"
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
	b.WriteString("      - " + autoName + "\n")
	for _, n := range names {
		b.WriteString("      - " + n + "\n")
	}

	b.WriteString("  - name: " + autoName + "\n    type: url-test\n")
	fmt.Fprintf(&b, "    url: %s\n    interval: %d\n    tolerance: 50\n", autoTestURL, autoTestInterval)
	b.WriteString("    proxies:\n")
	for _, n := range names {
		b.WriteString("      - " + n + "\n")
	}
	return b.String()
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

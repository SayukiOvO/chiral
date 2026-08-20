package profile

import (
	"encoding/json"
	"fmt"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// One node of this fleet leaving through another.
//
// Mechanically identical to relaying somebody else's provider: the entry gets
// an outbound and a routing rule per subscriber email, and the subscriber
// connects to the entry as usual. The one thing that is new is where the
// outbound comes from. An external provider hands us a clash proxy to
// translate; here the far end is ours, and the panel already holds the one
// artefact that describes how to dial it — the exit profile's xray-json client
// template, the same one a subscription is rendered from.
//
// Which is worth stating plainly, because the alternative is worse: deriving a
// client outbound from a server inbound means re-deriving REALITY public keys
// and mirroring every transport setting, in a second place that has to be kept
// in step with the first. Reusing the template means a relay dials the exit
// exactly the way a customer's client does, and cannot drift from it.

// RelayTag names a relay's outbound on the entry node. From the id, not the
// label, which the operator renames.
func RelayTag(relayID string) string { return "relay-" + relayID }

// relayOutbound renders the outbound the entry dials the exit with.
//
// The link's own credential, not a subscriber's: an Xray outbound is static,
// so what it presents cannot depend on whose connection it is carrying. That
// is also why accounting happens at the entry — see docs/user-management.md.
func (s *Service) relayOutbound(r store.NodeRelay) (string, error) {
	templates, err := s.st.ClientTemplates(r.ProfileID)
	if err != nil {
		return "", err
	}
	tmpl := templates[clientKindXrayJSON]
	if tmpl == "" {
		p, err := s.st.GetProfile(r.ProfileID)
		name := r.ProfileID
		if err == nil {
			name = p.Name
		}
		return "", fmt.Errorf("接入配置 %q 没有 xray-json 客户端模板，节点无法据此建立连接", name)
	}
	ctx, err := s.ClientContext(r.ProfileID, r.ExitNodeID)
	if err != nil {
		return "", err
	}
	// ClientContext has stripped the secret components, so a template that
	// reaches for a private key fails here rather than writing one into a
	// config that is pushed to a different machine than the key belongs to.
	body, err := ctx.With(user.RelayVars(r, r.ProfileID, r.ExitNodeID)).Render(tmpl)
	if err != nil {
		return "", err
	}
	var ob map[string]any
	if err := json.Unmarshal([]byte(body), &ob); err != nil {
		return "", fmt.Errorf("xray-json 模板未渲染出单个 JSON 对象：%w", err)
	}
	// Pin the tag: the routing rule points at it by name, and a template that
	// names itself something else would leave the rule dangling.
	ob["tag"] = RelayTag(r.ID)
	pinned, err := json.Marshal(ob)
	if err != nil {
		return "", err
	}
	return string(pinned), nil
}

// allowedOnRelay is the set of subscribers who may take one line. Denials are
// stored, and creating a line denies everyone, so this is empty until the
// operator says otherwise.
func (s *Service) allowedOnRelay(relayID string) (map[string]bool, error) {
	denied, err := s.st.NodeRelayDenies(relayID)
	if err != nil {
		return nil, err
	}
	users, err := s.st.ListUsers()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(users))
	for _, u := range users {
		if _, no := denied[u.ID]; !no {
			out[u.ID] = true
		}
	}
	return out, nil
}

// relayClientEntries are the client entries the EXIT node must carry: one per
// line that lands here on this profile.
//
// To the exit node these are ordinary clients. Nothing marks them as machines,
// and nothing should: the exit's job is to accept a credential and let the
// traffic out, which is the same job whether the far end is a customer's phone
// or another of our nodes.
func (s *Service) relayClientEntries(p store.Profile, nodeID string, ctx *template.Context) ([]string, error) {
	if p.ClientEntry == "" {
		return nil, nil
	}
	relays, err := s.st.RelaysToExit(nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(relays))
	for _, r := range relays {
		if r.ProfileID != p.ID {
			continue
		}
		rendered, err := ctx.With(user.RelayVars(r, p.ID, nodeID)).Render(p.ClientEntry)
		if err != nil {
			return nil, fmt.Errorf("profile %q client entry for relay %q: %w", p.Name, r.Label, err)
		}
		out = append(out, rendered)
	}
	return out, nil
}

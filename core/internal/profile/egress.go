package profile

import (
	"encoding/json"
	"fmt"

	"github.com/SayukiOvO/chiral/core/internal/external"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// Per-node egress rules: "Netflix leaves through the Japanese line, the rest
// goes direct". See docs/node-egress.md.
//
// All three landings reuse machinery that already exists — the node's own
// freedom outbound, the outbound "经由" builds from an external provider, and
// the outbound a relay line dials another of our nodes with. The only new
// thing is a machine credential for the third, and it is the same idea as a
// relay's: one per rule, belonging to nobody.

// EgressTag names a rule's outbound. From the id, not the label, which the
// operator renames.
func EgressTag(ruleID string) string { return "egress-" + ruleID }

// egressFor builds the EgressSources for one node, in the operator's order.
//
// exitTags is the set of outbound tags the caller has already contributed
// (relayed exits, relay lines). A rule landing on the same external provider
// as an existing exit reuses that outbound rather than dialling it twice —
// two outbounds to one provider would double the connections and split the
// picture of what that line carries.
func (s *Service) egressFor(nodeID string, existing map[string]string) ([]template.EgressSource, error) {
	rules, err := s.st.EgressRulesOn(nodeID)
	if err != nil {
		return nil, err
	}
	var out []template.EgressSource
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		src := template.EgressSource{Domains: r.Domains, IPs: r.IPs}
		switch r.TargetKind {
		case store.EgressDirect:
			// The skeleton's own outbound. Named rather than assumed: an
			// operator who renamed it gets a dangling rule, which the panel's
			// own xray -test catches before anything is pushed.
			src.Tag = "direct"
		case store.EgressExternal:
			p, err := s.st.GetExternalProxy(r.TargetProxyID)
			if err != nil {
				return nil, fmt.Errorf("出站分流 %q 的外部节点不存在了：%w", r.Label, err)
			}
			src.Tag = ExitTag(p.ID)
			if _, dup := existing[src.Tag]; !dup {
				ob, err := external.XrayOutbound(p.Config, src.Tag)
				if err != nil {
					return nil, fmt.Errorf("出站分流 %q 无法用这个外部节点：%w", r.Label, err)
				}
				src.Outbound = ob
				existing[src.Tag] = ob
			}
		case store.EgressNode:
			src.Tag = EgressTag(r.ID)
			ob, err := s.egressOutbound(r)
			if err != nil {
				return nil, fmt.Errorf("出站分流 %q 无法拨号：%w", r.Label, err)
			}
			src.Outbound = ob
			existing[src.Tag] = ob
		default:
			return nil, fmt.Errorf("出站分流 %q 的落点类型未知：%s", r.Label, r.TargetKind)
		}
		out = append(out, src)
	}
	return out, nil
}

// egressOutbound renders the outbound this node dials another of our nodes
// with, from the target profile's xray-json client template — the same
// artefact a relay line and a customer's client use, for the same reason:
// deriving a client config from a server inbound is a second implementation
// that has to be kept in step with the first.
func (s *Service) egressOutbound(r store.EgressRule) (string, error) {
	templates, err := s.st.ClientTemplates(r.TargetProfileID)
	if err != nil {
		return "", err
	}
	tmpl := templates[clientKindXrayJSON]
	if tmpl == "" {
		name := r.TargetProfileID
		if p, err := s.st.GetProfile(r.TargetProfileID); err == nil {
			name = p.Name
		}
		return "", fmt.Errorf("接入配置 %q 没有 xray-json 客户端模板，节点无从拨号", name)
	}
	ctx, err := s.ClientContext(r.TargetProfileID, r.TargetNodeID)
	if err != nil {
		return "", err
	}
	body, err := ctx.With(user.EgressVars(r)).Render(tmpl)
	if err != nil {
		return "", err
	}
	var ob map[string]any
	if err := json.Unmarshal([]byte(body), &ob); err != nil {
		return "", fmt.Errorf("xray-json 模板没有渲染出单个 JSON 对象：%w", err)
	}
	ob["tag"] = EgressTag(r.ID)
	pinned, err := json.Marshal(ob)
	if err != nil {
		return "", err
	}
	return string(pinned), nil
}

// egressClientEntries are the client entries the TARGET node must carry: one
// per egress rule landing here on this profile. Ordinary clients as far as
// Xray is concerned, exactly like a relay line's.
func (s *Service) egressClientEntries(p store.Profile, nodeID string, ctx *template.Context) ([]string, error) {
	if p.ClientEntry == "" {
		return nil, nil
	}
	rules, err := s.st.EgressRulesLandingOn(nodeID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		if r.TargetProfileID != p.ID {
			continue
		}
		rendered, err := ctx.With(user.EgressVars(r)).Render(p.ClientEntry)
		if err != nil {
			return nil, fmt.Errorf("profile %q client entry for egress %q: %w", p.Name, r.Label, err)
		}
		out = append(out, rendered)
	}
	return out, nil
}

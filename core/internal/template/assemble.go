package template

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DefaultSkeleton is the node config a freshly-added node starts from. The
// inbounds array is filled in from the profiles bound to that node.
const DefaultSkeleton = `{
  "log": { "loglevel": "warning" },
  "inbounds": [],
  "outbounds": [
    { "protocol": "freedom", "tag": "direct" }
  ]
}`

// InboundSource is one profile's server inbound, with the context to render
// it against (global + profile + node variables already merged).
type InboundSource struct {
	ProfileID   string
	ProfileName string
	Template    string
	Ctx         *Context
	// Clients are the already-rendered client-entry objects for the users
	// entitled to this profile on this node — one per credential. They are
	// spliced into the inbound's settings.clients.
	Clients []string
}

// ExitSource is one relayed exit: somebody else's node, dialled by this one.
//
// The subscriber connects to an inbound of ours holding a credential we
// minted, and this is what their traffic leaves through. They never learn the
// provider's address — which is the point, and also what makes per-user
// isolation, accounting and revocation apply to a node we do not own.
type ExitSource struct {
	// Tag names the outbound and is what the routing rule points at.
	Tag string
	// Outbound is the rendered Xray outbound, already translated out of the
	// provider's clash shape.
	Outbound string
	// Emails are the credentials whose traffic leaves this way. The routing
	// rule matches on them because the email is the only thing distinguishing
	// one exit from another on a shared inbound.
	Emails []string
}

// BlockSource is one restricted destination applying on this node: traffic
// from the named credentials to the named destinations goes to a blackhole.
//
// The entry of a connection always knows its destination — the proxy target
// arrives in the protocol — so this needs no sniffing to work. What it does
// need is ORDER: these rules must precede the relay rules, because a relay
// rule matches its users' every destination, and a blocked destination that
// escapes down a line is enforced nowhere after that — the exit sees only the
// line's machine credential.
type BlockSource struct {
	// CIDRs and Domains describe the destination; either may be empty.
	CIDRs   []string
	Domains []string
	// Emails are the credentials NOT allowed there. An empty list means
	// nobody is barred, and the caller must omit the block entirely — an
	// empty user list in a rule matches every user, not none.
	Emails []string
}

// BlackholeTag names the shared outbound blocked traffic is sent to.
const BlackholeTag = "chiral-blocked"

// AssembleNode renders each profile's inbound and splices the results into
// the node's config skeleton.
//
// Inbounds written by hand in the skeleton are preserved and the rendered
// ones are appended, so a node can carry both profile-managed access points
// and one-off manual ones.
func AssembleNode(skeleton string, sources []InboundSource) ([]byte, error) {
	return AssembleNodeFull(skeleton, sources, nil, nil)
}

// AssembleNodeWithExits is the same with relayed exits spliced in.
func AssembleNodeWithExits(skeleton string, sources []InboundSource, exits []ExitSource) ([]byte, error) {
	return AssembleNodeFull(skeleton, sources, exits, nil)
}

// AssembleNodeFull is the same with restricted destinations enforced too.
func AssembleNodeFull(skeleton string, sources []InboundSource, exits []ExitSource, blocks []BlockSource) ([]byte, error) {
	if skeleton == "" {
		skeleton = DefaultSkeleton
	}
	// Decode into an ordered-agnostic map; Xray does not care about key order
	// and neither do we.
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal([]byte(skeleton), &cfg); err != nil {
		return nil, fmt.Errorf("node config skeleton is not a JSON object: %w", err)
	}
	// JSON `null` unmarshals into a nil map without error; assigning to it
	// later would panic. Reject it here with a message that names the cause.
	if cfg == nil {
		return nil, fmt.Errorf("node config skeleton must be a JSON object, got null")
	}

	var inbounds []json.RawMessage
	if raw, ok := cfg["inbounds"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &inbounds); err != nil {
			return nil, fmt.Errorf(`skeleton "inbounds" is not an array: %w`, err)
		}
	}

	for _, src := range sources {
		rendered, err := src.Ctx.Render(src.Template)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", src.ProfileName, err)
		}
		// Parse before splicing: a template that renders to malformed or
		// non-object JSON must fail here with a pointed message, not produce
		// a corrupt config that fails later with an opaque one.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(rendered), &obj); err != nil {
			return nil, fmt.Errorf("profile %q inbound is not a JSON object after rendering: %w", src.ProfileName, err)
		}
		if err := spliceClients(obj, src.Clients); err != nil {
			return nil, fmt.Errorf("profile %q: %w", src.ProfileName, err)
		}
		compact, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, compact)
	}

	merged, err := json.Marshal(inbounds)
	if err != nil {
		return nil, err
	}
	cfg["inbounds"] = merged

	if err := spliceBlocksAndExits(cfg, blocks, exits); err != nil {
		return nil, err
	}

	// Add the management API last, so its inbound and routing rule sit ahead
	// of everything the profiles contributed.
	if _, err := EnsureAPI(cfg); err != nil {
		return nil, err
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	// Indent so an operator reading the stored version sees something legible.
	var buf bytes.Buffer
	if err := json.Indent(&buf, out, "", "  "); err != nil {
		return out, nil
	}
	return buf.Bytes(), nil
}

// spliceClients puts the rendered per-user entries into an inbound's
// settings.clients, keeping any the template author wrote by hand — the same
// courtesy manual inbounds get in the skeleton.
func spliceClients(inbound map[string]json.RawMessage, entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	var settings map[string]json.RawMessage
	if raw, ok := inbound["settings"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf(`inbound "settings" is not an object: %w`, err)
		}
	}
	if settings == nil {
		settings = map[string]json.RawMessage{}
	}
	var clients []json.RawMessage
	if raw, ok := settings["clients"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &clients); err != nil {
			return fmt.Errorf(`inbound "settings.clients" is not an array: %w`, err)
		}
	}
	for _, e := range entries {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(e), &obj); err != nil {
			return fmt.Errorf("client entry is not a JSON object after rendering: %w", err)
		}
		compact, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		clients = append(clients, compact)
	}
	merged, err := json.Marshal(clients)
	if err != nil {
		return err
	}
	settings["clients"] = merged
	out, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	inbound["settings"] = out
	return nil
}

// InboundTags extracts the "tag" of each inbound in an assembled config, for
// showing an operator what a node is actually serving.
func InboundTags(configJSON []byte) ([]string, error) {
	var cfg struct {
		Inbounds []struct {
			Tag string `json:"tag"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cfg.Inbounds))
	for _, in := range cfg.Inbounds {
		out = append(out, in.Tag)
	}
	return out, nil
}

// spliceBlocksAndExits appends each exit's outbound with the rule that selects
// it, and each block's blackhole rules ahead of those.
//
// All of it goes FIRST, ahead of whatever the operator wrote. Routing is
// first-match, and an operator's own catch-all — "everything to direct", which
// is the shape of every skeleton in the wild — would otherwise swallow the
// relayed traffic and send it out of this node instead of through the
// provider. That failure is silent and looks exactly like the relay working,
// because the connection succeeds; only the exit address is wrong.
//
// Within the fresh rules, blocks precede exits, and this is load-bearing: a
// relay rule matches its users' EVERY destination, so a block behind it would
// let restricted traffic slip down the line — past the last point where users
// can still be told apart.
func spliceBlocksAndExits(cfg map[string]json.RawMessage, blocks []BlockSource, exits []ExitSource) error {
	if len(exits) == 0 && len(blocks) == 0 {
		return nil
	}
	var outbounds []json.RawMessage
	if raw, ok := cfg["outbounds"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return fmt.Errorf(`skeleton "outbounds" is not an array: %w`, err)
		}
	}
	var routing map[string]json.RawMessage
	if raw, ok := cfg["routing"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &routing); err != nil {
			return fmt.Errorf(`skeleton "routing" is not an object: %w`, err)
		}
	}
	if routing == nil {
		routing = map[string]json.RawMessage{}
	}
	var rules []json.RawMessage
	if raw, ok := routing["rules"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &rules); err != nil {
			return fmt.Errorf(`skeleton "routing.rules" is not an array: %w`, err)
		}
	}

	var fresh []json.RawMessage
	blocked := false
	for _, b := range blocks {
		// A block with nobody barred must contribute nothing: an empty user
		// list matches everyone, and "nobody" and "everybody" must never be
		// one omission apart in the output.
		if len(b.Emails) == 0 {
			continue
		}
		emails, err := json.Marshal(b.Emails)
		if err != nil {
			return err
		}
		// ip and domain live in separate rules because conditions within one
		// rule are AND-ed: a single rule naming both would match only traffic
		// that somehow satisfied both at once, which is to say nothing.
		if len(b.CIDRs) > 0 {
			cidrs, err := json.Marshal(b.CIDRs)
			if err != nil {
				return err
			}
			fresh = append(fresh, json.RawMessage(fmt.Sprintf(
				`{"type":"field","ip":%s,"user":%s,"outboundTag":%q}`, cidrs, emails, BlackholeTag)))
			blocked = true
		}
		if len(b.Domains) > 0 {
			suffixes := make([]string, 0, len(b.Domains))
			for _, d := range b.Domains {
				suffixes = append(suffixes, "domain:"+d)
			}
			domains, err := json.Marshal(suffixes)
			if err != nil {
				return err
			}
			fresh = append(fresh, json.RawMessage(fmt.Sprintf(
				`{"type":"field","domain":%s,"user":%s,"outboundTag":%q}`, domains, emails, BlackholeTag)))
			blocked = true
		}
	}
	if blocked {
		outbounds = append(outbounds, json.RawMessage(
			fmt.Sprintf(`{"protocol":"blackhole","tag":%q}`, BlackholeTag)))
	}
	for _, e := range exits {
		var ob map[string]json.RawMessage
		if err := json.Unmarshal([]byte(e.Outbound), &ob); err != nil {
			return fmt.Errorf("exit %q: outbound is not a JSON object: %w", e.Tag, err)
		}
		outbounds = append(outbounds, json.RawMessage(e.Outbound))
		// An exit nobody may use still gets its outbound — the operator can
		// see it is configured — but no rule, because a rule with an empty
		// user list matches every user rather than none.
		if len(e.Emails) == 0 {
			continue
		}
		emails, err := json.Marshal(e.Emails)
		if err != nil {
			return err
		}
		fresh = append(fresh, json.RawMessage(fmt.Sprintf(
			`{"type":"field","user":%s,"outboundTag":%q}`, emails, e.Tag)))
	}

	merged, err := json.Marshal(append(fresh, rules...))
	if err != nil {
		return err
	}
	routing["rules"] = merged
	routingRaw, err := json.Marshal(routing)
	if err != nil {
		return err
	}
	cfg["routing"] = routingRaw

	obRaw, err := json.Marshal(outbounds)
	if err != nil {
		return err
	}
	cfg["outbounds"] = obRaw
	return nil
}

// ReplaceBlocks strips every restricted-destination rule (and the blackhole
// outbound) from an assembled config and splices the given blocks in fresh.
//
// It exists for Rollback. Block rules are derived state, like the probe: a
// version stored before a destination was scoped carries no rules at all, and
// one stored under an older allow list bars the wrong people. Reviving a
// rolled-back config must not revive last month's access policy with it.
func ReplaceBlocks(configJSON []byte, blocks []BlockSource) ([]byte, error) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return nil, err
	}

	// Drop the old blackhole outbound; splice re-adds it when needed.
	var outbounds []json.RawMessage
	if raw, ok := cfg["outbounds"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &outbounds); err != nil {
			return nil, fmt.Errorf(`config "outbounds" is not an array: %w`, err)
		}
	}
	kept := outbounds[:0]
	for _, ob := range outbounds {
		var probe struct {
			Tag string `json:"tag"`
		}
		if json.Unmarshal(ob, &probe) == nil && probe.Tag == BlackholeTag {
			continue
		}
		kept = append(kept, ob)
	}
	obRaw, err := json.Marshal(kept)
	if err != nil {
		return nil, err
	}
	cfg["outbounds"] = obRaw

	// Drop the old block rules, keeping everything else in place.
	var routing map[string]json.RawMessage
	if raw, ok := cfg["routing"]; ok && len(raw) > 0 {
		if err := json.Unmarshal(raw, &routing); err != nil {
			return nil, fmt.Errorf(`config "routing" is not an object: %w`, err)
		}
	}
	if routing != nil {
		var rules []json.RawMessage
		if raw, ok := routing["rules"]; ok && len(raw) > 0 {
			if err := json.Unmarshal(raw, &rules); err != nil {
				return nil, fmt.Errorf(`config "routing.rules" is not an array: %w`, err)
			}
		}
		keptRules := rules[:0]
		for _, r := range rules {
			var probe struct {
				OutboundTag string `json:"outboundTag"`
			}
			if json.Unmarshal(r, &probe) == nil && probe.OutboundTag == BlackholeTag {
				continue
			}
			keptRules = append(keptRules, r)
		}
		rulesRaw, err := json.Marshal(keptRules)
		if err != nil {
			return nil, err
		}
		routing["rules"] = rulesRaw
		routingRaw, err := json.Marshal(routing)
		if err != nil {
			return nil, err
		}
		cfg["routing"] = routingRaw
	}

	// Fresh blocks go in through the same splice as assembly, which prepends
	// them ahead of every rule already there — including the config's own
	// relay rules, which is the order that matters.
	if err := spliceBlocksAndExits(cfg, blocks, nil); err != nil {
		return nil, err
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, out, "", "  "); err != nil {
		return out, nil
	}
	return buf.Bytes(), nil
}

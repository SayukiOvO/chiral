// Package profile ties the variable pool, the template engine and node
// delivery together: it resolves a node's variables, renders the inbounds of
// every profile bound to it, validates the assembled config with `xray -test`
// and pushes it.
//
// It exists so that neither `template` (which stays pure and testable) nor
// `store` (which stays persistence-only) has to know about the other.
package profile

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"

	"github.com/SayukiOvO/chiral/core/internal/external"
	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// Pusher delivers a config to a live node. Implemented by node.Manager;
// declared here so this package does not depend on the gRPC layer.
type Pusher interface {
	PushConfig(nodeID string, version int64, configJSON, probeOutbound []byte) error
}

// Kernels resolves a version to the binary that should judge it. Satisfied by
// *kernel.Registry; an interface here so this package does not have to own the
// on-disk layout.
type Kernels interface {
	For(version string) kernel.Resolution
	Baked() template.Xray
}

type Service struct {
	st      *store.Store
	kernels Kernels
	push    Pusher
	userOps UserOpPusher
	users   *user.Service
	logger  *slog.Logger
}

func NewService(st *store.Store, kernels Kernels, push Pusher, userOps UserOpPusher, users *user.Service, logger *slog.Logger) *Service {
	return &Service{st: st, kernels: kernels, push: push, userOps: userOps, users: users, logger: logger}
}

// kernelFor picks the binary to validate a node's config with: the one that
// node's Xray will actually be started from.
//
// It reads xray_installed_version, not xray_version. The question here is
// "which kernel will run this config", and after a swap that is the installed
// one — a config pushed now is applied by restarting into it. Using the
// running version would validate against the build that is about to be
// replaced, which is exactly backwards during the window that matters.
func (s *Service) kernelFor(nodeID string) kernel.Resolution {
	n, err := s.st.GetNode(nodeID)
	if err != nil {
		// A node we cannot read gets the floor and an honest Exact=false.
		return s.kernels.For("")
	}
	want := n.XrayInstalledVersion
	if want == "" {
		want = n.XrayVersion
	}
	return s.kernels.For(want)
}

// contextFor builds the render context for one profile on one node, merging
// the scopes in the documented precedence: node > profile > global.
//
// Components are exposed as `name.component`, or bare `name` for
// single-valued variables, so `{{port}}` and `{{reality.private}}` come from
// the same storage shape.
func (s *Service) contextFor(profileID, nodeID string) (*template.Context, error) {
	globals, err := s.st.GlobalVariables()
	if err != nil {
		return nil, err
	}
	profileVars, err := s.st.ProfileVariables(profileID)
	if err != nil {
		return nil, err
	}
	nodeVars, err := s.st.NodeVariables(nodeID)
	if err != nil {
		return nil, err
	}

	var secrets []string
	flatten := func(vars []store.Variable) map[string]string {
		out := make(map[string]string)
		for _, v := range vars {
			for _, c := range v.Components {
				name := v.Name
				if c.Name != "" {
					name = v.Name + "." + c.Name
				}
				out[name] = c.Value
				if c.Secret {
					secrets = append(secrets, name)
				}
			}
		}
		return out
	}

	merged := template.Merge(flatten(globals), flatten(profileVars), flatten(nodeVars), nil)

	// Node metadata is always available, and cannot be shadowed by a
	// hand-written variable: these describe the machine, not a preference.
	n, err := s.st.GetNode(nodeID)
	if err != nil {
		return nil, err
	}
	merged["node.name"] = n.Name
	// Dialable rather than PublicIP: the detected peer address is wrong
	// whenever a NAT sits between agent and panel, and a client config built
	// from it points at an address nobody can reach.
	merged["node.address"] = n.Dialable()
	merged["node.hostname"] = n.Hostname
	// What a subscriber should see. Until this existed a client template had
	// only node.name to work with, so every subscription named the box the way
	// the operator names it — which is usually the provider and the datacentre.
	// The customer-facing name exists precisely so that does not happen, and it
	// was reachable from the portal and from nowhere else.
	//
	// Unset falls back to a number, never to the internal name: an operator who
	// has not filled it in should get an anonymous line, not their hosting
	// arrangement published. Same rule the portal applies, and the same
	// ordering, so the two agree on which line is 01.
	merged["node.display_name"] = s.customerName(n)

	return template.NewContext(merged, secrets), nil
}

// customerName is the label a subscriber sees for a node.
//
// The number comes from the node's position in the fleet by creation order, so
// it is stable across renders rather than shifting when another node is added
// before it alphabetically.
func (s *Service) customerName(n store.Node) string {
	if n.DisplayName != "" {
		return n.DisplayName
	}
	nodes, err := s.st.ListNodes()
	if err != nil {
		return "线路"
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].CreatedAt < nodes[j].CreatedAt })
	for i, other := range nodes {
		if other.ID == n.ID {
			return fmt.Sprintf("线路 %02d", i+1)
		}
	}
	return "线路"
}

// ClientContext is the render context for a client-side template: the same
// variables as the server side, minus every secret component. A client
// template that references a private key fails to render rather than leaking
// it into somebody's subscription.
func (s *Service) ClientContext(profileID, nodeID string) (*template.Context, error) {
	ctx, err := s.contextFor(profileID, nodeID)
	if err != nil {
		return nil, err
	}
	return ctx.ForClient(), nil
}

// AssembleNode renders the node's full config.json from the profiles bound to
// it. It does not persist or push anything — used both for preview and as the
// first half of Apply.
func (s *Service) AssembleNode(nodeID string) ([]byte, error) {
	n, err := s.st.GetNode(nodeID)
	if err != nil {
		return nil, err
	}
	profileIDs, err := s.st.NodeProfileIDs(nodeID)
	if err != nil {
		return nil, err
	}

	// The external nodes this one relays for. "经由 <自有节点>" means a
	// server-side relay now, not a chain the subscriber's client assembles:
	// the provider becomes an outbound here and the subscriber never sees it.
	relayed, err := s.relayedExits(nodeID)
	if err != nil {
		return nil, err
	}
	// The lines that leave through another of our nodes. Same shape: this node
	// is the entry, so it needs an outbound to each exit and a rule per
	// subscriber allowed to take it.
	relays, err := s.st.RelaysFromEntry(nodeID)
	if err != nil {
		return nil, err
	}

	sources := make([]template.InboundSource, 0, len(profileIDs))
	exitEmails := map[string][]string{}
	relayEmails := map[string][]string{}
	for _, pid := range profileIDs {
		p, err := s.st.GetProfile(pid)
		if err != nil {
			return nil, fmt.Errorf("loading profile %s: %w", pid, err)
		}
		if p.InboundTemplate == "" {
			return nil, fmt.Errorf("profile %q has no inbound template", p.Name)
		}
		ctx, err := s.contextFor(pid, nodeID)
		if err != nil {
			return nil, err
		}
		// Mint (or reuse) each entitled user's credential for this access
		// point and render it into the inbound's clients array.
		clients, err := s.renderClients(p, nodeID, ctx)
		if err != nil {
			return nil, err
		}
		// The credentials belonging to lines that LAND here. Not a
		// subscriber's — one per line, presented by the entry node itself.
		machine, err := s.relayClientEntries(p, nodeID, ctx)
		if err != nil {
			return nil, err
		}
		clients = append(clients, machine...)
		// And the credentials of egress rules on OTHER nodes that land here.
		egressIn, err := s.egressClientEntries(p, nodeID, ctx)
		if err != nil {
			return nil, err
		}
		clients = append(clients, egressIn...)
		// One more credential per relayed exit, rendered into the same inbound.
		// They are ordinary clients as far as Xray is concerned; what makes
		// them an exit is the routing rule that matches their email.
		for _, ex := range relayed {
			allowed, err := s.allowedUsers(ex.ID)
			if err != nil {
				return nil, err
			}
			creds, err := s.users.EnsureCredentialsForExit(pid, nodeID, ex.ID, allowed)
			if err != nil {
				return nil, err
			}
			rendered, err := s.renderCredentials(p, ctx, creds)
			if err != nil {
				return nil, err
			}
			clients = append(clients, rendered...)
			for _, c := range creds {
				exitEmails[ex.ID] = append(exitEmails[ex.ID], c.Email)
			}
		}
		// And one per line that STARTS here, for each subscriber allowed on it.
		for _, rl := range relays {
			allowed, err := s.allowedOnRelay(rl.ID)
			if err != nil {
				return nil, err
			}
			creds, err := s.users.EnsureCredentialsForRelay(pid, nodeID, rl.ID, allowed)
			if err != nil {
				return nil, err
			}
			rendered, err := s.renderCredentials(p, ctx, creds)
			if err != nil {
				return nil, err
			}
			clients = append(clients, rendered...)
			for _, c := range creds {
				relayEmails[rl.ID] = append(relayEmails[rl.ID], c.Email)
			}
		}
		sources = append(sources, template.InboundSource{
			ProfileID:   pid,
			ProfileName: p.Name,
			Template:    p.InboundTemplate,
			Ctx:         ctx,
			Clients:     clients,
		})
	}

	exits := make([]template.ExitSource, 0, len(relayed)+len(relays))
	for _, rl := range relays {
		ob, err := s.relayOutbound(rl)
		if err != nil {
			return nil, fmt.Errorf("中转线路 %q 无法建立连接：%w", rl.Label, err)
		}
		exits = append(exits, template.ExitSource{
			Tag: RelayTag(rl.ID), Outbound: ob, Emails: relayEmails[rl.ID],
		})
	}
	for _, ex := range relayed {
		ob, err := external.XrayOutbound(ex.Config, ExitTag(ex.ID))
		if err != nil {
			return nil, fmt.Errorf("外部节点 %q 无法用作中继出口：%w", ex.Label(), err)
		}
		exits = append(exits, template.ExitSource{
			Tag: ExitTag(ex.ID), Outbound: ob, Emails: exitEmails[ex.ID],
		})
	}
	// Built AFTER the credential loops above: barred users are matched by
	// their credential emails, and this round may have minted new ones.
	blocks, err := s.blocksFor(nodeID)
	if err != nil {
		return nil, err
	}
	// Egress rules may land on an external provider this node already relays
	// for; the tag set lets them share that one outbound instead of dialling
	// the same provider twice.
	built := make(map[string]string, len(exits))
	for _, e := range exits {
		built[e.Tag] = e.Outbound
	}
	egress, err := s.egressFor(nodeID, built)
	if err != nil {
		return nil, err
	}
	return template.AssembleNodeWithEgress(n.ConfigSkeleton, sources, exits, blocks, egress)
}

// ExitTag names an exit's outbound. Derived from the id rather than the label,
// which the operator renames and the provider rewrites.
func ExitTag(proxyID string) string { return "exit-" + proxyID }

// relayedExits lists the external nodes this node relays for: the ones whose
// chain target is this node.
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

// allowedUsers is the set who may leave through one exit. Denials are stored,
// so this is every subscriber minus the ones told no.
func (s *Service) allowedUsers(proxyID string) (map[string]bool, error) {
	denied, err := s.st.ExternalProxyDenies(proxyID)
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

// renderClients turns the credentials entitled to this profile on this node
// into rendered clients[] entries.
func (s *Service) renderClients(p store.Profile, nodeID string, ctx *template.Context) ([]string, error) {
	if p.ClientEntry == "" {
		// No per-user entry means the profile serves no users yet; the inbound
		// is still valid, just empty.
		return nil, nil
	}
	creds, err := s.users.EnsureCredentials(p.ID, nodeID)
	if err != nil {
		return nil, err
	}
	return s.renderCredentials(p, ctx, creds)
}

// renderCredentials turns credentials into client-entry objects. Split out
// because a relayed exit produces more of them for the same inbound.
func (s *Service) renderCredentials(p store.Profile, ctx *template.Context, creds []store.Credential) ([]string, error) {
	if p.ClientEntry == "" {
		return nil, nil
	}
	out := make([]string, 0, len(creds))
	for _, c := range creds {
		rendered, err := ctx.With(user.CredentialVars(c)).Render(p.ClientEntry)
		if err != nil {
			return nil, fmt.Errorf("profile %q client entry for %s: %w", p.Name, c.Email, err)
		}
		out = append(out, rendered)
	}
	return out, nil
}

// Preview assembles and validates without persisting, so an operator can see
// exactly what a change would produce before committing to it.
type Preview struct {
	Config      []byte
	InboundTags []string
	// TestError is Xray's complaint, if validation ran and failed. Assembly
	// errors are returned as a normal error instead.
	TestError string
	// Tested reports whether `xray -test` actually ran; it is skipped when no
	// binary is configured, and a preview that was not tested must not be
	// presented as verified.
	Tested bool
	// KernelVersion is the version that judged it, and KernelExact whether that
	// is the version the node itself runs.
	//
	// Carried out to the caller rather than kept as a log line because "passed
	// validation" means two different things depending on this flag, and the
	// weaker one — tested against a build the node does not have — is the one
	// an operator mid-upgrade most needs to not mistake for the stronger.
	KernelVersion string
	KernelExact   bool
	// KernelNote explains an inexact match in words, empty when exact.
	KernelNote string
	// Advisories are configurations that pass validation and still will not
	// work for somebody. See template.Advisories.
	Advisories []string
}

func (s *Service) Preview(ctx context.Context, nodeID string) (Preview, error) {
	cfg, err := s.AssembleNode(nodeID)
	if err != nil {
		return Preview{}, err
	}
	tags, _ := template.InboundTags(cfg)
	p := Preview{Config: cfg, InboundTags: tags}
	res := s.kernelFor(nodeID)
	p.KernelVersion, p.KernelExact = res.Version, res.Exact
	if !res.Exact {
		p.KernelNote = res.Describe()
	}
	if res.Xray.Available() {
		p.Tested = true
		if err := res.Xray.TestConfig(ctx, cfg); err != nil {
			p.TestError = err.Error()
		}
	}
	// The tags this node's egress rules land on, so an advisory can tell our
	// rules apart from the operator's own — see blockAdvisories.
	var egressTags []string
	if rules, err := s.st.EgressRulesOn(nodeID); err == nil {
		for _, r := range rules {
			if !r.Enabled {
				continue
			}
			switch r.TargetKind {
			case store.EgressDirect:
				egressTags = append(egressTags, "direct")
			case store.EgressExternal:
				egressTags = append(egressTags, ExitTag(r.TargetProxyID))
			case store.EgressNode:
				egressTags = append(egressTags, EgressTag(r.ID))
			}
		}
	}
	p.Advisories = template.Advisories(cfg, s.clientKindsOn(nodeID), egressTags)
	return p, nil
}

// clientKindsOn lists the client templates every profile bound to this node
// offers, so an advisory can tell whether anybody is being served a config the
// inbound will refuse.
func (s *Service) clientKindsOn(nodeID string) []string {
	profileIDs, err := s.st.NodeProfileIDs(nodeID)
	if err != nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, pid := range profileIDs {
		// Served, not stored: the question is whether anybody is BEING SERVED
		// a config the inbound will refuse, and a held-back template serves
		// nobody.
		templates, err := s.st.ServedClientTemplates(pid)
		if err != nil {
			continue
		}
		kinds := make([]string, 0, len(templates))
		for k := range templates {
			kinds = append(kinds, k)
		}
		for _, k := range kinds {
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			out = append(out, k)
		}
	}
	return out
}

// Apply assembles, validates, stores a new version and pushes it to the node.
//
// A config that fails `xray -test` is never stored or pushed: the point of
// validating panel-side is that a broken render cannot take a node down.
func (s *Service) Apply(ctx context.Context, nodeID string) (int64, error) {
	cfg, err := s.AssembleNode(nodeID)
	if err != nil {
		return 0, err
	}
	res := s.kernelFor(nodeID)
	switch {
	case res.Xray.Available():
		if err := res.Xray.TestConfig(ctx, cfg); err != nil {
			return 0, err
		}
		// An inexact match is not a reason to refuse. The agent runs
		// `xray -test` again with the node's own binary and will not apply a
		// config that fails, so that check — not this one — is the guarantee;
		// this one is an early warning that saves a round trip and keeps most
		// broken renders out of the version history. Refusing here would
		// instead strand any node the panel has not caught up with, which is
		// every node in the middle of an upgrade.
		if !res.Exact {
			s.logger.Warn("validated against a different kernel than the node runs",
				"node", nodeID, "detail", res.Describe())
		}
	default:
		s.logger.Warn("no panel-side Xray binary; pushing without pre-validation", "node", nodeID)
	}

	// Rendered from the same templates as the config it accompanies, and
	// stored with it, so a node can never hold a probe that belongs to a
	// different version of its own access points.
	probe, why := s.ProbeOutbound(nodeID)
	if probe == "" {
		s.logger.Warn("no data-path probe for this node; a kernel upgrade here cannot be confirmed to carry traffic",
			"node", nodeID, "reason", why)
	}

	c, err := s.st.InsertConfig(nodeID, string(cfg), probe)
	if err != nil {
		return 0, err
	}
	if err := s.push.PushConfig(nodeID, c.Version, []byte(c.Config), []byte(c.ProbeOutbound)); err != nil {
		// Not fatal: the version is stored, and heartbeat reconciliation
		// pushes it as soon as the node reconnects.
		s.logger.Warn("config stored but not pushed", "node", nodeID, "version", c.Version, "err", err)
	}
	return c.Version, nil
}

// Rollback re-pushes an older version's content as a NEW version.
//
// Not a distinct agent instruction, by design (CLAUDE.md decision 7): the
// agent keeps no history and does not need to understand the word. What it
// receives is an ordinary ConfigPush whose bytes happen to be old, so the
// stored config stays the single source of truth and a reconnect reconciles
// to it like any other.
//
// The old content is validated again rather than trusted. It passed once, but
// the panel's Xray may have been upgraded since, and pushing a config the
// current kernel rejects would take the node down for as long as it takes
// someone to notice.
func (s *Service) Rollback(ctx context.Context, nodeID string, version int64) (int64, error) {
	old, err := s.st.ConfigAt(nodeID, version)
	if err != nil {
		return 0, err
	}
	// Restricted-destination rules are re-derived rather than revived: they
	// are access policy, not configuration, and the old version's rules bar
	// whoever was barred THEN — a destination scoped since then is absent, a
	// user barred since then is free. Same reasoning as the probe below.
	blocks, err := s.blocksFor(nodeID)
	if err != nil {
		return 0, err
	}
	restored, err := template.ReplaceBlocks([]byte(old.Config), blocks)
	if err != nil {
		return 0, fmt.Errorf("re-deriving restricted rules for version %d: %w", version, err)
	}
	old.Config = string(restored)
	if res := s.kernelFor(nodeID); res.Xray.Available() {
		if err := res.Xray.TestConfig(ctx, []byte(old.Config)); err != nil {
			return 0, fmt.Errorf("version %d no longer passes xray -test on %s: %w", version, res.Describe(), err)
		}
	}
	// The probe is re-rendered rather than copied from the old version. It
	// carries a credential and an address, and the old one's subscriber may
	// since have been deleted — reviving a rolled-back config must not revive a
	// credential with it.
	probe, why := s.ProbeOutbound(nodeID)
	if probe == "" {
		s.logger.Warn("rolling back without a data-path probe", "node", nodeID, "reason", why)
	}
	c, err := s.st.InsertConfig(nodeID, old.Config, probe)
	if err != nil {
		return 0, err
	}
	if err := s.push.PushConfig(nodeID, c.Version, []byte(c.Config), []byte(c.ProbeOutbound)); err != nil {
		s.logger.Warn("rollback stored but not pushed", "node", nodeID, "version", c.Version, "err", err)
	}
	s.logger.Info("rolled back", "node", nodeID, "from_version", version, "new_version", c.Version)
	return c.Version, nil
}

// ApplyBoundNodes re-applies every node bound to a profile — what you want
// after editing that profile's template or variables.
func (s *Service) ApplyBoundNodes(ctx context.Context, profileID string) (map[string]error, error) {
	nodeIDs, err := s.st.ProfileNodeIDs(profileID)
	if err != nil {
		return nil, err
	}
	results := make(map[string]error, len(nodeIDs))
	for _, id := range nodeIDs {
		_, err := s.Apply(ctx, id)
		results[id] = err
	}
	return results, nil
}

// GenerateVariable creates a variable from a generator, storing every
// component of the group with its secrecy already decided by the generator.
func (s *Service) GenerateVariable(name, scope string, profileID, nodeID sql.NullString, gen template.Generator) (store.Variable, error) {
	var group template.Group
	var err error
	if template.NeedsXray(gen) {
		group, err = s.kernels.Baked().GenerateMLDSA65()
	} else {
		group, err = template.Generate(gen)
	}
	if err != nil {
		return store.Variable{}, err
	}
	return s.putGroup(name, scope, profileID, nodeID, gen, group)
}

// ImportVariable stores a group built around a key the operator already runs,
// so pointing this panel at an existing deployment keeps every client
// configuration that was handed out before it.
//
// Only the private half is accepted; the rest is derived. See
// template.SecretComponent.
func (s *Service) ImportVariable(name, scope string, profileID, nodeID sql.NullString, gen template.Generator, secret string) (store.Variable, error) {
	var group template.Group
	var err error
	if template.NeedsXray(gen) {
		group, err = s.kernels.Baked().MLDSA65FromSeed(secret)
	} else {
		group, err = template.Import(gen, secret)
	}
	if err != nil {
		return store.Variable{}, err
	}
	return s.putGroup(name, scope, profileID, nodeID, gen, group)
}

func (s *Service) putGroup(name, scope string, profileID, nodeID sql.NullString, gen template.Generator, group template.Group) (store.Variable, error) {
	secretSet := group.SecretSet()
	components := make([]store.Component, 0, len(group.Components))
	for _, cname := range group.ComponentNames() {
		_, isSecret := secretSet[cname]
		components = append(components, store.Component{
			Name:   cname,
			Value:  group.Components[cname],
			Secret: isSecret,
		})
	}
	return s.st.PutVariable(store.Variable{
		Name:       name,
		Scope:      scope,
		ProfileID:  profileID,
		NodeID:     nodeID,
		Generator:  string(gen),
		Components: components,
	})
}

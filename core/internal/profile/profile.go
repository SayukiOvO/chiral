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
	merged["node.address"] = n.PublicIP
	merged["node.hostname"] = n.Hostname

	return template.NewContext(merged, secrets), nil
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

	sources := make([]template.InboundSource, 0, len(profileIDs))
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
		sources = append(sources, template.InboundSource{
			ProfileID:   pid,
			ProfileName: p.Name,
			Template:    p.InboundTemplate,
			Ctx:         ctx,
			Clients:     clients,
		})
	}
	return template.AssembleNode(n.ConfigSkeleton, sources)
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
	return p, nil
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

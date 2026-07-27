// Package upgrade drives runtime Xray-core upgrades: what is available, what a
// node should install, and serving the bytes to nodes that cannot fetch them
// themselves.
//
// The ordering constraint that shapes everything here: Core installs a version
// for itself BEFORE telling any node to install it. That is what keeps
// "versions the panel can validate configs for" and "versions running in the
// fleet" the same set — the invariant core/internal/kernel exists to serve. A
// node running a build the panel does not have is a node whose configs can only
// be checked by proxy.
package upgrade

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/release"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// Installer sends an install instruction to a live node. Implemented by
// node.Manager.
type Installer interface {
	SendXrayInstall(nodeID string, in *chiralv1.XrayInstall) error
	SendXrayChunk(nodeID string, c *chiralv1.XrayChunk) error
	IsOnline(nodeID string) bool
}

// releaseCacheTTL bounds how stale the "what is available" answer may be.
// GitHub allows 60 unauthenticated requests an hour; a panel with a console
// open would burn that in minutes without this.
const releaseCacheTTL = 15 * time.Minute

type Service struct {
	st       *store.Store
	reg      *kernel.Registry
	fetch    *kernel.Fetcher
	releases release.Client
	nodes    Installer
	logger   *slog.Logger
	// alerts is nil until wired; rollbacks are then logged but nobody is told.
	alerts Announcer

	// base outlives any HTTP request. Fetching a release archive takes minutes
	// on a poor uplink, and tying that to a request context means the browser
	// giving up kills the download — which is not a hypothetical: it is the
	// first thing that happened when this was wired to a real release.
	base   context.Context
	cancel context.CancelFunc

	// IncludePrerelease follows CLAUDE.md decision 9: the features the
	// templates are built around exist only in snapshots, and stable tags are
	// sparse enough that following them means following nothing for months.
	IncludePrerelease bool

	mu       sync.Mutex
	cached   release.Release
	cachedAt time.Time
}

func NewService(st *store.Store, reg *kernel.Registry, releases release.Client, nodes Installer, logger *slog.Logger) *Service {
	base, cancel := context.WithCancel(context.Background())
	return &Service{
		base:              base,
		cancel:            cancel,
		st:                st,
		reg:               reg,
		fetch:             kernel.NewFetcher(reg),
		releases:          releases,
		nodes:             nodes,
		logger:            logger,
		IncludePrerelease: true,
	}
}

// Close stops any background work. Called at shutdown.
func (s *Service) Close() { s.cancel() }

// Prewarm fetches the panel's own copy of the newest release, so pressing
// "upgrade" is instant rather than a multi-minute wait behind a download the
// operator cannot see.
//
// Best effort and quiet on failure: a panel with no egress to GitHub is a
// supported configuration, and it should not log an error every hour about a
// thing it was never going to do.
func (s *Service) Prewarm(ctx context.Context) {
	rel, err := s.Latest(ctx)
	if err != nil {
		s.logger.Debug("prewarm: could not reach upstream", "err", err)
		return
	}
	if s.reg.Has(rel.Version) {
		return
	}
	archive, digest, err := rel.AssetFor(kernel.Platform())
	if err != nil {
		s.logger.Debug("prewarm: no asset for the panel's platform", "err", err)
		return
	}
	sum, err := s.releases.Checksum(ctx, digest)
	if err != nil {
		return
	}
	if err := s.fetch.Install(ctx, rel.Version, archive, sum); err != nil {
		s.logger.Warn("prewarm: could not install the newest kernel for validation",
			"version", rel.Version, "err", err)
		return
	}
	s.logger.Info("prewarmed a kernel for validation", "version", rel.Version)
}

// RunPrewarm keeps the panel's newest kernel warm until ctx ends.
func (s *Service) RunPrewarm(ctx context.Context, every time.Duration) {
	s.Prewarm(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.Prewarm(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// Latest returns the newest upstream release, cached briefly.
func (s *Service) Latest(ctx context.Context) (release.Release, error) {
	s.mu.Lock()
	if s.cached.Version != "" && time.Since(s.cachedAt) < releaseCacheTTL {
		r := s.cached
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()

	r, err := s.releases.Latest(ctx, s.IncludePrerelease)
	if err != nil {
		return release.Release{}, err
	}
	s.mu.Lock()
	s.cached, s.cachedAt = r, time.Now()
	s.mu.Unlock()
	return r, nil
}

// Available describes what the fleet could move to.
type Available struct {
	Version    string `json:"version"`
	Tag        string `json:"tag"`
	Prerelease bool   `json:"prerelease"`
	// PanelHas reports whether Core can already validate configs for it.
	PanelHas bool `json:"panel_has"`
	// PanelVersions is every version Core can validate for.
	PanelVersions []string `json:"panel_versions"`
}

func (s *Service) Available(ctx context.Context) (Available, error) {
	r, err := s.Latest(ctx)
	if err != nil {
		return Available{}, err
	}
	return Available{
		Version:       r.Version,
		Tag:           r.Tag,
		Prerelease:    r.Prerelease,
		PanelHas:      s.reg.Has(r.Version),
		PanelVersions: s.reg.Versions(),
	}, nil
}

// InstallOn tells one node to fetch and (optionally) switch to a version.
//
// Core fetches the version for itself first and refuses if that fails. Skipping
// that would let a node run a build the panel cannot validate configs against,
// which is the exact hole core/internal/kernel was built to close — and it
// would fail silently, days later, as an unexplained rejected push.
func (s *Service) InstallOn(ctx context.Context, nodeID, version string, activate bool) (store.XrayInstall, error) {
	version = template.NormalizeVersion(version)
	n, err := s.st.GetNode(nodeID)
	if err != nil {
		return store.XrayInstall{}, err
	}
	platform := strings.TrimSpace(n.Platform)
	if platform == "" {
		return store.XrayInstall{}, fmt.Errorf(
			"node %s has not reported its platform yet; it needs to connect with an agent new enough to send it", n.Name)
	}

	rel, err := s.releaseFor(ctx, version)
	if err != nil {
		return store.XrayInstall{}, err
	}
	archiveURL, digestURL, err := rel.AssetFor(platform)
	if err != nil {
		return store.XrayInstall{}, err
	}
	sum, err := s.releases.Checksum(ctx, digestURL)
	if err != nil {
		return store.XrayInstall{}, err
	}

	// Core's own copy, for validation. Its platform, not the node's.
	if !s.reg.Has(version) {
		coreArchive, coreDigest, err := rel.AssetFor(kernel.Platform())
		if err != nil {
			return store.XrayInstall{}, fmt.Errorf("the panel cannot validate for %s: %w", version, err)
		}
		coreSum, err := s.releases.Checksum(ctx, coreDigest)
		if err != nil {
			return store.XrayInstall{}, err
		}
		if err := s.fetch.Install(ctx, version, coreArchive, coreSum); err != nil {
			return store.XrayInstall{}, fmt.Errorf("the panel could not install %s for validation: %w", version, err)
		}
		s.logger.Info("panel installed a kernel for validation", "version", version)
	}

	in := store.XrayInstall{
		NodeID:      nodeID,
		Version:     version,
		DownloadURL: archiveURL,
		SHA256:      sum,
		Activate:    activate,
		Phase:       phaseName(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_UNSPECIFIED),
	}
	if err := s.st.StartXrayInstall(in, time.Now()); err != nil {
		return store.XrayInstall{}, err
	}
	if err := s.nodes.SendXrayInstall(nodeID, &chiralv1.XrayInstall{
		Version:     version,
		DownloadUrl: archiveURL,
		Sha256:      sum,
		// The relay is offered because Core has the archive for Core's
		// platform — but the node may need a different one, and fetching that
		// is deferred to the first relay request rather than paid for by every
		// node that will never need it.
		RelayAvailable: true,
		Activate:       activate,
	}); err != nil {
		// The row stays: a node that was offline when the button was pressed
		// still shows the attempt, rather than the operator wondering whether
		// the click registered.
		return in, fmt.Errorf("node is not reachable: %w", err)
	}
	return in, nil
}

// releaseFor finds a specific version among recent releases.
func (s *Service) releaseFor(ctx context.Context, version string) (release.Release, error) {
	if latest, err := s.Latest(ctx); err == nil && latest.Version == version {
		return latest, nil
	}
	all, err := s.releases.List(ctx, 30)
	if err != nil {
		return release.Release{}, err
	}
	for _, r := range all {
		if r.Version == version {
			return r, nil
		}
	}
	return release.Release{}, fmt.Errorf("no %s release named %s in the last 30", release.Repo, version)
}

// HandleStatus records a phase report from an agent.
func (s *Service) HandleStatus(nodeID string, st *chiralv1.XrayStatus) {
	version := template.NormalizeVersion(st.GetVersion())
	ok, err := s.st.UpdateXrayInstall(nodeID, version, phaseName(st.GetPhase()), st.GetMessage(), time.Now())
	if err != nil {
		s.logger.Error("recording an install status failed", "node", nodeID, "err", err)
		return
	}
	if !ok {
		// A status for a version this node is no longer installing. Dropped on
		// purpose: writing it would stamp the current attempt with a superseded
		// one's outcome.
		s.logger.Warn("ignoring a status for a superseded install",
			"node", nodeID, "version", version, "phase", st.GetPhase())
		return
	}
	if _, err := s.st.SetXrayVersions(nodeID, st.GetRunningVersion(), st.GetInstalledVersion()); err != nil {
		s.logger.Error("recording versions from an install status failed", "node", nodeID, "err", err)
	}
	level := slog.LevelInfo
	switch st.GetPhase() {
	case chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED,
		chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK:
		level = slog.LevelError
	}
	s.logger.Log(context.Background(), level, "kernel install status",
		"node", nodeID, "version", version, "phase", phaseName(st.GetPhase()), "detail", st.GetMessage())

	// Move the fleet state machine on the agent's own report rather than a
	// timer: a timer would have to guess how long a 21 MB download takes on an
	// unknown uplink, and would call an upgrade failed for being slow.
	s.advance(nodeID, st)
}

// HandleRelayRequest serves one chunk of an archive to a node that could not
// fetch it directly.
//
// One chunk per request, because the agent asks for the next one only after
// writing this one. That caps the relay at a single frame in flight per node —
// the node's send queue holds sixteen and carries config pushes and user
// operations too, so a pushed 21 MB archive would either be dropped or would
// crowd those out.
func (s *Service) HandleRelayRequest(ctx context.Context, nodeID string, req *chiralv1.XrayRelayRequest) {
	version := template.NormalizeVersion(req.GetVersion())
	fail := func(msg string) {
		s.logger.Warn("relay refused", "node", nodeID, "version", version, "reason", msg)
		s.nodes.SendXrayChunk(nodeID, &chiralv1.XrayChunk{
			Version: version, Offset: req.GetOffset(), Error: msg,
		})
	}

	in, err := s.st.XrayInstallFor(nodeID)
	if err != nil {
		fail("this node has no install in progress")
		return
	}
	// The node may only relay what it was told to install. Without this a
	// compromised agent could ask the panel to fetch and hand back an arbitrary
	// URL, turning it into a download proxy.
	if in.Version != version {
		fail(fmt.Sprintf("this node was told to install %s, not %s", in.Version, version))
		return
	}
	n, err := s.st.GetNode(nodeID)
	if err != nil || n.Platform == "" {
		fail("the node's platform is unknown")
		return
	}

	path, err := s.fetch.EnsureArchive(ctx, version, n.Platform, in.DownloadURL, in.SHA256)
	if err != nil {
		fail("the panel could not obtain the archive: " + err.Error())
		return
	}
	data, last, err := kernel.ReadChunk(path, req.GetOffset(), relayChunkSize)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := s.nodes.SendXrayChunk(nodeID, &chiralv1.XrayChunk{
		Version: version, Offset: req.GetOffset(), Data: data, Last: last,
	}); err != nil {
		s.logger.Warn("could not send a relay chunk", "node", nodeID, "err", err)
	}
}

// relayChunkSize matches the agent's request size. Comfortably under gRPC's
// 4 MB default message limit.
const relayChunkSize = 256 << 10

// phaseName renders a phase as the bare name stored in the database.
func phaseName(p chiralv1.XrayInstallPhase) string {
	return strings.TrimPrefix(p.String(), "XRAY_INSTALL_PHASE_")
}

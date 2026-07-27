package xray

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/internal/xrayarchive"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// Fetching, verifying and installing a kernel version on the node.
//
// Versions live side by side under <stateDir>/kernels/<version>/, never
// overwriting each other. That is what makes a rollback a path change rather
// than a second download: the binary being replaced is still on disk, intact,
// and putting it back cannot fail for want of network.

const (
	// downloadTimeout bounds one direct fetch. The archive is ~21 MB and a
	// node's uplink may be poor; generous, but not unbounded — a fetch that
	// never finishes must eventually become a reportable failure rather than a
	// permanently in-progress upgrade.
	downloadTimeout = 15 * time.Minute
	// relayChunk is how much is asked for per round trip. Comfortably under
	// gRPC's 4 MB default message limit, and large enough that a 21 MB archive
	// is ~84 round trips rather than thousands.
	relayChunk = 256 << 10
)

// Relay fetches a slice of an archive from Core, for nodes that cannot reach
// GitHub. Implemented by the client, which owns the stream.
type Relay interface {
	Chunk(ctx context.Context, version string, offset int64) (data []byte, last bool, err error)
}

// ProgressFunc reports a phase change. Called from the install goroutine.
type ProgressFunc func(phase chiralv1.XrayInstallPhase, message string)

// Installer owns the per-version kernel directory.
type Installer struct {
	dir string
	mgr *Manager
	// HTTP is the client used for direct fetches; nil means a default one.
	HTTP *http.Client
}

func NewInstaller(stateDir string, mgr *Manager) *Installer {
	return &Installer{dir: filepath.Join(stateDir, "kernels"), mgr: mgr}
}

// Dir is the kernel install root.
func (in *Installer) Dir() string { return in.dir }

// VersionDir is where a given version lives once installed.
func (in *Installer) VersionDir(version string) string {
	return filepath.Join(in.dir, version)
}

// BinaryFor returns the installed binary path for a version, or "" if it is not
// installed.
func (in *Installer) BinaryFor(version string) string {
	bin := filepath.Join(in.VersionDir(version), xrayarchive.BinaryName)
	if st, err := os.Stat(bin); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
		return bin
	}
	return ""
}

// Install carries out one XrayInstall instruction end to end and reports the
// terminal phase.
//
// Progress is emitted at every step, not only at the end: an upgrade stalled
// halfway through a download and one that never arrived look identical from
// Core, and the difference is what decides whether an operator waits or
// intervenes.
func (in *Installer) Install(ctx context.Context, req *chiralv1.XrayInstall, relay Relay, progress ProgressFunc) (chiralv1.XrayInstallPhase, string) {
	version := strings.TrimPrefix(strings.TrimSpace(req.GetVersion()), "v")
	if version == "" {
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED, "the install carries no version"
	}
	if len(req.GetSha256()) != 64 {
		// Never negotiable. The relay is not a trusted path either: it is the
		// same bytes through a different pipe, and the panel is exactly the
		// machine an attacker would want to be standing on.
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED,
			"refusing to install without a SHA-256"
	}

	bin := in.BinaryFor(version)
	if bin == "" {
		var err error
		bin, err = in.fetchAndUnpack(ctx, version, req, relay, progress)
		if err != nil {
			return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED, err.Error()
		}
	}

	// The binary must be the version it claims. A URL that points at the wrong
	// build hashes correctly and installs cleanly; only asking it settles the
	// question, and getting this wrong would make Core validate configs against
	// a version no node is running.
	if got := versionOf(bin); got != version {
		os.RemoveAll(in.VersionDir(version))
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED,
			fmt.Sprintf("the downloaded binary reports version %q, not %q", got, version)
	}

	if !req.GetActivate() {
		progress(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED, "staged on disk, not activated")
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED, "staged on disk, not activated"
	}

	progress(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVATING, "restarting into "+version)
	outcome, msg, err := in.mgr.Activate(ctx, bin)
	if err != nil {
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED, err.Error()
	}
	switch outcome {
	case OutcomeActive, OutcomeInconclusive:
		// Both mean the new kernel is the one running, so a restart must come
		// back to it rather than to the image's.
		if err := in.SetActive(version); err != nil {
			in.mgr.logger.Warn("could not record the active kernel version", "err", err)
		}
		if outcome == OutcomeActive {
			return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE, msg
		}
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INCONCLUSIVE, msg
	default:
		// Activate has already put the previous binary back and restarted it.
		// ROLLED_BACK rather than FAILED says the node is serving again, which
		// is the difference between "look at this now" and "look at this
		// today".
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK, msg
	}
}

func (in *Installer) fetchAndUnpack(ctx context.Context, version string, req *chiralv1.XrayInstall, relay Relay, progress ProgressFunc) (string, error) {
	if err := os.MkdirAll(in.dir, 0o755); err != nil {
		return "", err
	}
	archive := filepath.Join(in.dir, version+".zip.part")
	defer os.Remove(archive)

	progress(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_DOWNLOADING, "fetching "+version)
	directErr := in.download(ctx, req.GetDownloadUrl(), archive)
	if directErr != nil {
		if !req.GetRelayAvailable() || relay == nil {
			// Say which route was tried and that the other does not exist,
			// rather than letting a node with no egress look like a transient
			// network blip forever.
			return "", fmt.Errorf("direct download failed and the panel is not offering a relay: %w", directErr)
		}
		progress(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_DOWNLOADING,
			fmt.Sprintf("direct download failed (%v); falling back to the panel relay", directErr))
		if err := in.downloadViaRelay(ctx, version, archive, relay); err != nil {
			return "", fmt.Errorf("direct download failed (%v) and the relay failed too: %w", directErr, err)
		}
	}

	progress(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_VERIFYING, "checking the archive")
	if err := xrayarchive.Verify(archive, req.GetSha256()); err != nil {
		return "", err
	}

	// Unpack into a staging directory and rename it into place, so a version
	// directory never exists in a half-populated state — something else may be
	// asked to run out of it the moment it appears.
	staging := in.VersionDir(version) + ".staging"
	os.RemoveAll(staging)
	defer os.RemoveAll(staging)
	if _, err := xrayarchive.Unpack(archive, staging); err != nil {
		return "", err
	}
	final := in.VersionDir(version)
	os.RemoveAll(final)
	if err := os.Rename(staging, final); err != nil {
		return "", err
	}
	return filepath.Join(final, xrayarchive.BinaryName), nil
}

func (in *Installer) download(ctx context.Context, url, dest string) error {
	if url == "" {
		return fmt.Errorf("no download URL")
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := in.HTTP
	if client == nil {
		client = &http.Client{Timeout: downloadTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned %s", url, resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// downloadViaRelay pulls the archive from Core one chunk at a time.
//
// The agent asks; Core never pushes. A node's send queue holds sixteen frames
// and carries config pushes and user operations too, so a pushed 21 MB archive
// would either be dropped or would crowd those out. Asking caps the transfer at
// one frame in flight per node — no rate limit to tune, and nothing to starve.
func (in *Installer) downloadViaRelay(ctx context.Context, version, dest string, relay Relay) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	var offset int64
	for {
		data, last, err := relay.Chunk(ctx, version, offset)
		if err != nil {
			return err
		}
		if len(data) > 0 {
			if _, err := f.Write(data); err != nil {
				return err
			}
			offset += int64(len(data))
		}
		if last {
			return f.Sync()
		}
		if len(data) == 0 {
			// Neither progress nor an end. Stopping beats looping forever
			// against a peer that has nothing more to give.
			return fmt.Errorf("the relay returned an empty chunk at offset %d without ending the transfer", offset)
		}
	}
}

// activeFile records which installed version the agent switched to, so a
// restart does not silently revert to the binary baked into the image.
//
// Without it, a container restart after an upgrade puts the node back on the
// image's kernel while Core still believes the new one is installed — a
// downgrade nobody asked for and nobody is told about. The image's binary stays
// the floor: it is what runs when there is no record, or when the recorded
// version is not actually on disk.
const activeFile = "active"

// SetActive records the version now running.
func (in *Installer) SetActive(version string) error {
	if err := os.MkdirAll(in.dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(in.dir, activeFile), []byte(version+"\n"), 0o644)
}

// ActiveBinary returns the binary the agent should start with, or "" to use the
// configured one.
//
// Deliberately verifies rather than trusts: the recorded version must still be
// installed AND the binary there must report that same version. A pointer to
// something absent or mislabelled is worse than no pointer, because it would
// silently run a build nobody named.
func (in *Installer) ActiveBinary() string {
	raw, err := os.ReadFile(filepath.Join(in.dir, activeFile))
	if err != nil {
		return ""
	}
	version := strings.TrimSpace(string(raw))
	if version == "" {
		return ""
	}
	bin := in.BinaryFor(version)
	if bin == "" {
		return ""
	}
	if versionOf(bin) != version {
		return ""
	}
	return bin
}

// Installed lists the versions present on disk.
func (in *Installer) Installed() []string {
	entries, err := os.ReadDir(in.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && in.BinaryFor(e.Name()) != "" {
			out = append(out, e.Name())
		}
	}
	return out
}

// Prune removes installed versions other than the ones named.
//
// Each version is ~66 MB unpacked, so a node that has followed prereleases for
// a year would otherwise be carrying gigabytes of kernels nothing will ever
// start again. Keeping the running one and the one before it is what makes a
// rollback a path change instead of a download.
func (in *Installer) Prune(keep ...string) {
	keeping := make(map[string]bool, len(keep))
	for _, k := range keep {
		if k != "" {
			keeping[k] = true
		}
	}
	for _, v := range in.Installed() {
		if !keeping[v] {
			os.RemoveAll(in.VersionDir(v))
		}
	}
}

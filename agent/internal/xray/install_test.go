package xray

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// releaseZip builds an archive shaped like a real one, whose "binary" is a
// script reporting the given version. Enough to exercise everything the
// installer does with it: verify, unpack, ask what it is, run it.
func releaseZip(t *testing.T, version string, staysUp bool) []byte {
	t.Helper()
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo \"Xray " + version + " (Xray, Penetrates Everything.)\"; exit 0; fi\n"
	if staysUp {
		body += "exec sleep 60\n"
	} else {
		body += "echo 'infra/conf: unknown transport protocol: xhttp' >&2\nexit 23\n"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range map[string]string{
		"xray":        body,
		"geoip.dat":   "ip data",
		"geosite.dat": "site data",
		"README.md":   "docs",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func installerFixture(t *testing.T, runningVersion string) (*Installer, *Manager, string) {
	t.Helper()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The currently running kernel: a script that stays up.
	cur := filepath.Join(t.TempDir(), "xray")
	if err := os.WriteFile(cur, []byte(
		"#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray "+runningVersion+" (Xray, Penetrates Everything.)'; exit 0; fi\nexec sleep 60\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	mgr := New(cur, state, logger, nil)
	return NewInstaller(state, mgr), mgr, cur
}

func noProgress(chiralv1.XrayInstallPhase, string) {}

func TestDirectInstallActivatesAndReportsRunning(t *testing.T) {
	archive := releaseZip(t, "26.9.1", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	in, mgr, _ := installerFixture(t, "26.3.27")
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version: "26.9.1", DownloadUrl: srv.URL, Sha256: digestOf(archive), Activate: true,
	}, nil, noProgress)

	// No API inbound in this config, so the probe has nothing to ask — which is
	// INCONCLUSIVE by design, not ACTIVE. "It did not fail" and "it works" are
	// different claims and only the second should promote a canary.
	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INCONCLUSIVE {
		t.Fatalf("phase = %v (%s)", phase, msg)
	}
	if got := mgr.RunningVersion(); got != "26.9.1" {
		t.Fatalf("running version = %q after activation, want 26.9.1", got)
	}
	// The geo databases ride along; without them every geosite:/geoip: routing
	// rule fails to load and the node rejects perfectly correct configs.
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(in.VersionDir("26.9.1"), name)); err != nil {
			t.Errorf("%s was not installed alongside the binary", name)
		}
	}
}

// The requirement in one test: a kernel that will not start must put the old
// one back, and the report must carry the kernel's own words.
func TestAKernelThatWillNotStartIsRolledBack(t *testing.T) {
	archive := releaseZip(t, "26.9.1", false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	in, mgr, cur := installerFixture(t, "26.3.27")
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version: "26.9.1", DownloadUrl: srv.URL, Sha256: digestOf(archive), Activate: true,
	}, nil, noProgress)

	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK {
		t.Fatalf("phase = %v (%s), want ROLLED_BACK", phase, msg)
	}
	if !strings.Contains(msg, "xhttp") {
		t.Fatalf("the report does not say why it failed:\n%s", msg)
	}
	if got := mgr.Binary(); got != cur {
		t.Fatalf("binary = %q after the rollback, want the previous %q", got, cur)
	}
	if got := mgr.RunningVersion(); got != "26.3.27" {
		t.Fatalf("running version = %q; the node is not serving on the old kernel", got)
	}
}

// The install carries a checksum for a reason. Bytes that do not match it are
// never unpacked, whichever route they arrived by.
func TestAMismatchedArchiveIsNeverInstalled(t *testing.T) {
	archive := releaseZip(t, "26.9.1", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	in, mgr, cur := installerFixture(t, "26.3.27")
	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version: "26.9.1", DownloadUrl: srv.URL, Sha256: strings.Repeat("a", 64), Activate: true,
	}, nil, noProgress)

	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED {
		t.Fatalf("phase = %v (%s), want FAILED", phase, msg)
	}
	if in.BinaryFor("26.9.1") != "" {
		t.Error("a mismatched archive was unpacked anyway")
	}
	if mgr.Binary() != cur {
		t.Error("the manager was pointed at a binary that failed verification")
	}
}

func TestAnInstallWithNoChecksumIsRefusedOutright(t *testing.T) {
	in, _, _ := installerFixture(t, "26.3.27")
	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version: "26.9.1", DownloadUrl: "https://example.invalid/a.zip", Activate: true,
	}, nil, noProgress)
	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED {
		t.Fatalf("phase = %v", phase)
	}
	if !strings.Contains(msg, "SHA-256") {
		t.Errorf("the refusal does not name the reason: %q", msg)
	}
}

// A URL pointing at the wrong build hashes correctly and unpacks cleanly. Only
// asking the binary settles it — and getting this wrong would have Core
// validating configs against a version no node is running.
func TestABinaryThatIsNotTheVersionItClaimsIsRejected(t *testing.T) {
	archive := releaseZip(t, "26.3.27", true) // labelled 26.9.1 by the caller
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	in, _, _ := installerFixture(t, "26.3.27")
	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version: "26.9.1", DownloadUrl: srv.URL, Sha256: digestOf(archive), Activate: false,
	}, nil, noProgress)

	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED {
		t.Fatalf("phase = %v (%s), want FAILED", phase, msg)
	}
	if in.BinaryFor("26.9.1") != "" {
		t.Error("a mislabelled version was left installed")
	}
}

// The relay: chunked, pull-driven, and it has to reassemble byte for byte.
type sliceRelay struct {
	body      []byte
	chunkSize int
	requests  int
}

func (r *sliceRelay) Chunk(ctx context.Context, version string, offset int64) ([]byte, bool, error) {
	r.requests++
	if offset > int64(len(r.body)) {
		return nil, false, fmt.Errorf("offset past the end")
	}
	end := offset + int64(r.chunkSize)
	if end > int64(len(r.body)) {
		end = int64(len(r.body))
	}
	return r.body[offset:end], end >= int64(len(r.body)), nil
}

func TestTheRelayIsUsedWhenTheDirectFetchFails(t *testing.T) {
	archive := releaseZip(t, "26.9.1", true)
	relay := &sliceRelay{body: archive, chunkSize: 512}

	in, _, _ := installerFixture(t, "26.3.27")
	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version:        "26.9.1",
		DownloadUrl:    "http://127.0.0.1:1/definitely-not-listening",
		Sha256:         digestOf(archive),
		RelayAvailable: true,
		Activate:       false,
	}, relay, noProgress)

	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED {
		t.Fatalf("phase = %v (%s), want INSTALLED", phase, msg)
	}
	if relay.requests < 2 {
		t.Errorf("the relay served the archive in %d request(s); it should have been chunked", relay.requests)
	}
	if in.BinaryFor("26.9.1") == "" {
		t.Fatal("the relayed archive did not install")
	}
}

// A node with no egress and no relay on offer must say so, rather than looking
// like a transient network blip forever.
func TestNoDirectRouteAndNoRelayIsAClearFailure(t *testing.T) {
	archive := releaseZip(t, "26.9.1", true)
	in, _, _ := installerFixture(t, "26.3.27")
	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version:        "26.9.1",
		DownloadUrl:    "http://127.0.0.1:1/definitely-not-listening",
		Sha256:         digestOf(archive),
		RelayAvailable: false,
	}, nil, noProgress)

	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED {
		t.Fatalf("phase = %v", phase)
	}
	if !strings.Contains(msg, "relay") {
		t.Errorf("the failure does not mention that no relay was offered: %q", msg)
	}
}

// Staging is how a fleet upgrade separates "everyone has the bytes" from
// "everyone is running it". It must not touch the running kernel.
func TestStagingDoesNotTouchTheRunningKernel(t *testing.T) {
	archive := releaseZip(t, "26.9.1", true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	in, mgr, cur := installerFixture(t, "26.3.27")
	if err := mgr.Start(); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	phase, msg := in.Install(context.Background(), &chiralv1.XrayInstall{
		Version: "26.9.1", DownloadUrl: srv.URL, Sha256: digestOf(archive), Activate: false,
	}, nil, noProgress)

	if phase != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED {
		t.Fatalf("phase = %v (%s)", phase, msg)
	}
	if mgr.Binary() != cur {
		t.Error("staging repointed the manager at the new binary")
	}
	if got := mgr.RunningVersion(); got != "26.3.27" {
		t.Fatalf("running version = %q; staging restarted the kernel", got)
	}
}

// Each version is ~66 MB unpacked. A node following prereleases for a year
// would otherwise carry gigabytes of kernels nothing will ever start again.
func TestPruneKeepsOnlyTheNamedVersions(t *testing.T) {
	in, _, _ := installerFixture(t, "26.3.27")
	for _, v := range []string{"26.1.1", "26.3.27", "26.9.1"} {
		dir := in.VersionDir(v)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "xray"), []byte("#!/bin/sh\n"), 0o755)
	}
	in.Prune("26.3.27", "26.9.1")

	if in.BinaryFor("26.1.1") != "" {
		t.Error("an unnamed version survived the prune")
	}
	for _, v := range []string{"26.3.27", "26.9.1"} {
		if in.BinaryFor(v) == "" {
			t.Errorf("%s was pruned but should have been kept", v)
		}
	}
}

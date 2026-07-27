package upgrade

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/release"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type fakeNodes struct {
	mu       sync.Mutex
	chunks   []*chiralv1.XrayChunk
	installs []*chiralv1.XrayInstall
	offline  bool
}

func (f *fakeNodes) SendXrayInstall(nodeID string, in *chiralv1.XrayInstall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installs = append(f.installs, in)
	return nil
}

func (f *fakeNodes) SendXrayChunk(nodeID string, c *chiralv1.XrayChunk) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, c)
	return nil
}

func (f *fakeNodes) IsOnline(string) bool { return !f.offline }

func (f *fakeNodes) lastChunk(t *testing.T) *chiralv1.XrayChunk {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.chunks) == 0 {
		t.Fatal("no chunk was sent")
	}
	return f.chunks[len(f.chunks)-1]
}

func fixture(t *testing.T) (*Service, *store.Store, *fakeNodes, store.Node) {
	t.Helper()
	box, err := secret.NewBox("upgrade-test-key-0123456789abcd")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	n, err := st.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetNodePlatform(n.ID, "linux/amd64"); err != nil {
		t.Fatal(err)
	}
	nodes := &fakeNodes{}
	reg := kernel.New(t.TempDir(), template.Xray{})
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewService(st, reg, release.Client{}, nodes, logger), st, nodes, n
}

// The guard that keeps the relay from being a general-purpose download proxy.
// A compromised agent must not be able to name an arbitrary version — and by
// extension an arbitrary URL, since the URL is looked up from what Core told it
// to install.
func TestARelayOnlyServesWhatTheNodeWasToldToInstall(t *testing.T) {
	svc, st, nodes, n := fixture(t)
	if err := st.StartXrayInstall(store.XrayInstall{
		NodeID: n.ID, Version: "26.9.1", DownloadURL: "https://example.invalid/a.zip",
		SHA256: strings.Repeat("a", 64), Phase: "DOWNLOADING",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	svc.HandleRelayRequest(context.Background(), n.ID, &chiralv1.XrayRelayRequest{
		Version: "26.10.1", Offset: 0,
	})
	c := nodes.lastChunk(t)
	if c.GetError() == "" {
		t.Fatal("the panel agreed to relay a version this node was never told to install")
	}
	if len(c.GetData()) != 0 {
		t.Error("a refusal carried data")
	}
}

// A node with no install at all gets a refusal, not a fetch.
func TestARelayWithNoInstallInProgressIsRefused(t *testing.T) {
	svc, _, nodes, n := fixture(t)
	svc.HandleRelayRequest(context.Background(), n.ID, &chiralv1.XrayRelayRequest{
		Version: "26.9.1", Offset: 0,
	})
	if c := nodes.lastChunk(t); c.GetError() == "" {
		t.Fatal("a node with no install in progress was served bytes")
	}
}

func TestHandleStatusRecordsThePhaseAndTheVersions(t *testing.T) {
	svc, st, _, n := fixture(t)
	if err := st.StartXrayInstall(store.XrayInstall{
		NodeID: n.ID, Version: "26.9.1", SHA256: strings.Repeat("a", 64), Phase: "DOWNLOADING",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	svc.HandleStatus(n.ID, &chiralv1.XrayStatus{
		Version:          "26.9.1",
		Phase:            chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE,
		Message:          "running and answering",
		RunningVersion:   "26.9.1",
		InstalledVersion: "26.9.1",
	})

	in, err := st.XrayInstallFor(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if in.Phase != "ACTIVE" {
		t.Fatalf("phase = %q, want ACTIVE", in.Phase)
	}
	// Versions travel with the verdict so the outcome need not wait for the
	// next heartbeat to be believed.
	node, _ := st.GetNode(n.ID)
	if node.XrayVersion != "26.9.1" || node.XrayInstalledVersion != "26.9.1" {
		t.Fatalf("versions = %q/%q", node.XrayVersion, node.XrayInstalledVersion)
	}
}

// A rollback must be recorded as a rollback. Folding it into FAILED would lose
// the one thing an operator needs at 3am: whether the node is serving.
func TestARollbackIsRecordedDistinctlyFromAFailure(t *testing.T) {
	svc, st, _, n := fixture(t)
	if err := st.StartXrayInstall(store.XrayInstall{
		NodeID: n.ID, Version: "26.9.1", SHA256: strings.Repeat("a", 64), Phase: "ACTIVATING",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	svc.HandleStatus(n.ID, &chiralv1.XrayStatus{
		Version:        "26.9.1",
		Phase:          chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK,
		Message:        "unknown transport protocol: xhttp",
		RunningVersion: "26.3.27",
	})
	in, _ := st.XrayInstallFor(n.ID)
	if in.Phase != "ROLLED_BACK" {
		t.Fatalf("phase = %q, want ROLLED_BACK", in.Phase)
	}
	// The kernel's own words are what an admin has to fix.
	if !strings.Contains(in.Message, "xhttp") {
		t.Fatalf("the diagnosis was lost: %q", in.Message)
	}
}

func TestInstallOnRefusesANodeWithNoKnownPlatform(t *testing.T) {
	svc, st, _, _ := fixture(t)
	bare, err := st.CreateNode("unknown-1", "join-hash-2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.InstallOn(context.Background(), bare.ID, "26.9.1", true)
	if err == nil {
		t.Fatal("a node whose architecture is unknown was told to install a binary")
	}
	if !strings.Contains(err.Error(), "platform") {
		t.Errorf("the error does not name the cause: %v", err)
	}
}

func TestPhaseNameStripsThePrefix(t *testing.T) {
	if got := phaseName(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK); got != "ROLLED_BACK" {
		t.Fatalf("phaseName = %q", got)
	}
	if got := phaseName(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_UNSPECIFIED); got != "UNSPECIFIED" {
		t.Fatalf("phaseName = %q", got)
	}
}

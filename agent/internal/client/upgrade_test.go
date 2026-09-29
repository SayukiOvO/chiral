package client

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/protobuf/proto"
)

type upgradeRuntime struct {
	fakeRuntime
	install func(context.Context, *chiralv1.XrayInstall, runtimeprovider.Relay, runtimeprovider.ProgressFunc) (chiralv1.XrayInstallPhase, string)
}

func (r *upgradeRuntime) Install(ctx context.Context, req *chiralv1.XrayInstall, relay runtimeprovider.Relay, progress runtimeprovider.ProgressFunc) (chiralv1.XrayInstallPhase, string) {
	return r.install(ctx, req, relay, progress)
}

func upgradeClient(t *testing.T, rt *upgradeRuntime) *Client {
	t.Helper()
	return NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func nextUpgradeFrame(t *testing.T, frames <-chan *chiralv1.AgentFrame) *chiralv1.AgentFrame {
	t.Helper()
	select {
	case f := <-frames:
		return f
	case <-time.After(3 * time.Second):
		t.Fatal("upgrade did not produce its expected frame")
		return nil
	}
}

func waitUpgradeIdle(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for c.installs.current() != "" {
		select {
		case <-deadline.C:
			t.Fatal("upgrade did not release the install slot")
		case <-tick.C:
		}
	}
}

func TestProviderUpgradeRejectsEmptyVersionWithoutTouchingInstallSlot(t *testing.T) {
	var calls atomic.Int32
	rt := &upgradeRuntime{install: func(context.Context, *chiralv1.XrayInstall, runtimeprovider.Relay, runtimeprovider.ProgressFunc) (chiralv1.XrayInstallPhase, string) {
		calls.Add(1)
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE, "must not be called"
	}}
	c := upgradeClient(t, rt)
	for _, occupied := range []bool{false, true} {
		if occupied && !c.installs.begin("26.9.28") {
			t.Fatal("cannot reserve the existing install slot")
		}
		for _, req := range []*chiralv1.XrayInstall{nil, {}, {Version: " \t\n"}} {
			frames := make(chan *chiralv1.AgentFrame, 1)
			c.startInstall(context.Background(), frames, req)
			if got := nextUpgradeFrame(t, frames).GetXrayStatus(); got.GetPhase() != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED {
				t.Fatalf("empty version was not rejected: %v", got)
			}
			want := ""
			if occupied {
				want = "26.9.28"
			}
			if c.installs.current() != want || calls.Load() != 0 {
				t.Fatal("empty request touched the provider or another installation's slot")
			}
		}
	}
}

func TestProviderUpgradePreservesInstructionRelayAndProgress(t *testing.T) {
	want := &chiralv1.XrayInstall{
		Version:        "26.9.28",
		DownloadUrl:    "https://release.example/Xray-linux-64.zip",
		Sha256:         strings.Repeat("a", 64),
		RelayAvailable: true,
		Activate:       false,
	}
	rt := &upgradeRuntime{fakeRuntime: fakeRuntime{running: "26.7.28", installed: "26.7.28"}}
	rt.install = func(ctx context.Context, req *chiralv1.XrayInstall, relay runtimeprovider.Relay, progress runtimeprovider.ProgressFunc) (chiralv1.XrayInstallPhase, string) {
		if !proto.Equal(req, want) {
			return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED, "install instruction changed"
		}
		progress(chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_DOWNLOADING, "using the panel relay")
		data, last, err := relay.Chunk(ctx, req.GetVersion(), 4096)
		if err != nil || !last || string(data) != "last archive chunk" {
			return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED, "relay reply did not reach the provider"
		}
		rt.installed = req.GetVersion()
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED, "staged without activation"
	}
	c := upgradeClient(t, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan *chiralv1.AgentFrame, 4)
	c.startInstall(ctx, frames, proto.Clone(want).(*chiralv1.XrayInstall))

	progress := nextUpgradeFrame(t, frames).GetXrayStatus()
	if progress.GetPhase() != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_DOWNLOADING || progress.GetVersion() != want.GetVersion() {
		t.Fatalf("provider progress was not forwarded: %v", progress)
	}
	request := nextUpgradeFrame(t, frames).GetXrayRelayRequest()
	if request.GetVersion() != want.GetVersion() || request.GetOffset() != 4096 {
		t.Fatalf("unexpected relay request: %v", request)
	}
	c.handleFrame(ctx, frames, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_XrayChunk{
		XrayChunk: &chiralv1.XrayChunk{
			Version: want.GetVersion(), Offset: 4096, Data: []byte("last archive chunk"), Last: true,
		},
	}})
	status := nextUpgradeFrame(t, frames).GetXrayStatus()
	if status.GetPhase() != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED || status.GetMessage() != "staged without activation" {
		t.Fatalf("unexpected provider result: %v", status)
	}
	if status.GetRunningVersion() != "26.7.28" || status.GetInstalledVersion() != want.GetVersion() {
		t.Fatalf("staging did not preserve distinct running and installed versions: %v", status)
	}
	waitUpgradeIdle(t, c)
}

func TestProviderUpgradeSerializesAndReleasesSlotAfterCancellation(t *testing.T) {
	var calls atomic.Int32
	rt := &upgradeRuntime{}
	rt.install = func(ctx context.Context, req *chiralv1.XrayInstall, relay runtimeprovider.Relay, _ runtimeprovider.ProgressFunc) (chiralv1.XrayInstallPhase, string) {
		calls.Add(1)
		if req.GetVersion() == "26.9.29" {
			return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED, "second install completed"
		}
		_, _, err := relay.Chunk(ctx, req.GetVersion(), 0)
		if err == nil {
			return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE, "unexpected relay success"
		}
		return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED, "relay cancelled"
	}
	c := upgradeClient(t, rt)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frames := make(chan *chiralv1.AgentFrame, 8)
	c.startInstall(ctx, frames, &chiralv1.XrayInstall{Version: "26.9.28"})
	if request := nextUpgradeFrame(t, frames).GetXrayRelayRequest(); request.GetVersion() != "26.9.28" {
		t.Fatalf("first install did not start: %v", request)
	}
	c.startInstall(ctx, frames, &chiralv1.XrayInstall{Version: "26.9.29"})
	status := nextUpgradeFrame(t, frames).GetXrayStatus()
	if status.GetPhase() != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED || !strings.Contains(status.GetMessage(), "another install (26.9.28)") {
		t.Fatalf("concurrent install was not rejected: %v", status)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider saw %d concurrent calls, want 1", calls.Load())
	}
	cancel()
	waitUpgradeIdle(t, c)

	// A completed cancellation must not retain the slot across a reconnect.
	c.startInstall(context.Background(), frames, &chiralv1.XrayInstall{Version: "26.9.29"})
	for {
		status = nextUpgradeFrame(t, frames).GetXrayStatus()
		if status.GetVersion() == "26.9.29" {
			break
		}
	}
	if status.GetPhase() != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INSTALLED || calls.Load() != 2 {
		t.Fatalf("next install could not proceed: calls=%d status=%v", calls.Load(), status)
	}
	waitUpgradeIdle(t, c)
}

func TestProviderUpgradePreservesTerminalVerdicts(t *testing.T) {
	for _, phase := range []chiralv1.XrayInstallPhase{
		chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE,
		chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INCONCLUSIVE,
		chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK,
		chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED,
	} {
		t.Run(phase.String(), func(t *testing.T) {
			rt := &upgradeRuntime{fakeRuntime: fakeRuntime{running: "running", installed: "installed"}}
			rt.install = func(context.Context, *chiralv1.XrayInstall, runtimeprovider.Relay, runtimeprovider.ProgressFunc) (chiralv1.XrayInstallPhase, string) {
				return phase, "provider verdict"
			}
			c := upgradeClient(t, rt)
			frames := make(chan *chiralv1.AgentFrame, 1)
			c.startInstall(context.Background(), frames, &chiralv1.XrayInstall{Version: "26.9.28", Activate: true})
			status := nextUpgradeFrame(t, frames).GetXrayStatus()
			if status.GetPhase() != phase || status.GetMessage() != "provider verdict" || status.GetRunningVersion() != "running" || status.GetInstalledVersion() != "installed" {
				t.Fatalf("provider verdict changed in transit: %v", status)
			}
			waitUpgradeIdle(t, c)
		})
	}
}

var _ runtimeprovider.Upgrader = (*upgradeRuntime)(nil)

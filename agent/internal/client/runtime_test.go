package client

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type fakeRuntime struct {
	appliedVersion int64
	appliedConfig  []byte
	probeOutbound  []byte
	probeURL       string
	restarts       int
	addedTag       string
	addedEmail     string
	addedAccount   []byte
	removedTag     string
	removedEmail   string
	stats          []runtimeprovider.Stat
	online         []runtimeprovider.OnlineUser
	complete       bool
	state          chiralv1.XrayState
	configVersion  int64
	running        string
	installed      string
}

func (f *fakeRuntime) Apply(version int64, configJSON []byte) error {
	f.appliedVersion = version
	f.appliedConfig = append([]byte(nil), configJSON...)
	return nil
}

func (f *fakeRuntime) Restart() error {
	f.restarts++
	return nil
}

func (f *fakeRuntime) SetProbe(outboundJSON []byte, url string) {
	f.probeOutbound = append([]byte(nil), outboundJSON...)
	f.probeURL = url
}

func (f *fakeRuntime) AddUser(_ context.Context, inboundTag, email string, accountJSON []byte) error {
	f.addedTag = inboundTag
	f.addedEmail = email
	f.addedAccount = append([]byte(nil), accountJSON...)
	return nil
}

func (f *fakeRuntime) RemoveUser(_ context.Context, inboundTag, email string) error {
	f.removedTag = inboundTag
	f.removedEmail = email
	return nil
}

func (f *fakeRuntime) Stats(context.Context) ([]runtimeprovider.Stat, error) {
	return f.stats, nil
}

func (f *fakeRuntime) OnlineUsers(context.Context) ([]runtimeprovider.OnlineUser, bool, error) {
	return f.online, f.complete, nil
}

func (f *fakeRuntime) State() chiralv1.XrayState { return f.state }
func (f *fakeRuntime) ConfigVersion() int64      { return f.configVersion }
func (f *fakeRuntime) RunningVersion() string    { return f.running }
func (f *fakeRuntime) InstalledVersion() string  { return f.installed }

func TestClientRoutesOrdinaryOperationsThroughRuntime(t *testing.T) {
	rt := &fakeRuntime{
		stats: []runtimeprovider.Stat{
			{Name: "user>>>alice@p.n>>>traffic>>>uplink", Value: 12},
			{Name: "user>>>alice@p.n>>>traffic>>>downlink", Value: 34},
		},
		online: []runtimeprovider.OnlineUser{{
			Email: "alice@p.n",
			IPs:   map[string]int64{"198.51.100.7": 1234},
		}},
		complete:      true,
		state:         chiralv1.XrayState_XRAY_STATE_RUNNING,
		configVersion: 41,
		running:       "26.7.28",
		installed:     "26.7.29",
	}
	c := NewWithRuntime(
		Config{
			StateDir: t.TempDir(),
			RuntimeStatusSnapshot: func() *chiralv1.RuntimeStatus {
				return directRuntimeStatus()
			},
		},
		rt,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	ctx := context.Background()
	send := make(chan *chiralv1.AgentFrame, 1)
	c.handleFrame(ctx, send, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_ConfigPush{
		ConfigPush: &chiralv1.ConfigPush{
			Version:           42,
			ConfigJson:        []byte(`{"inbounds":[]}`),
			ProbeOutboundJson: []byte(`{"tag":"probe-out"}`),
			ProbeUrl:          "https://probe.example/",
		},
	}})
	ack := (<-send).GetConfigAck()
	if ack == nil || !ack.GetApplied() || ack.GetVersion() != 42 {
		t.Fatalf("unexpected config ack: %+v", ack)
	}
	if rt.appliedVersion != 42 || string(rt.appliedConfig) != `{"inbounds":[]}` {
		t.Fatalf("config did not reach runtime: version=%d config=%s", rt.appliedVersion, rt.appliedConfig)
	}
	if string(rt.probeOutbound) != `{"tag":"probe-out"}` || rt.probeURL != "https://probe.example/" {
		t.Fatalf("probe did not reach runtime: outbound=%s url=%q", rt.probeOutbound, rt.probeURL)
	}

	c.handleFrame(ctx, send, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_Command{
		Command: &chiralv1.Command{Cmd: &chiralv1.Command_RestartXray{RestartXray: &chiralv1.RestartXray{}}},
	}})
	if rt.restarts != 1 {
		t.Fatalf("restart calls = %d, want 1", rt.restarts)
	}
	if event := <-c.events; event.GetKind() != chiralv1.EventKind_EVENT_KIND_XRAY_RESTARTED {
		t.Fatalf("unexpected restart event: %+v", event)
	}

	c.handleFrame(ctx, send, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{
		UserOp: &chiralv1.UserOp{
			Kind:        chiralv1.UserOpKind_USER_OP_KIND_ADD,
			InboundTag:  "profile-in",
			Email:       "alice@p.n",
			AccountJson: []byte(`{"id":"uuid"}`),
		},
	}})
	if rt.addedTag != "profile-in" || rt.addedEmail != "alice@p.n" || string(rt.addedAccount) != `{"id":"uuid"}` {
		t.Fatalf("add user did not reach runtime: %+v", rt)
	}

	c.handleFrame(ctx, send, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{
		UserOp: &chiralv1.UserOp{
			Kind:       chiralv1.UserOpKind_USER_OP_KIND_REMOVE,
			InboundTag: "profile-in",
			Email:      "alice@p.n",
		},
	}})
	if rt.removedTag != "profile-in" || rt.removedEmail != "alice@p.n" {
		t.Fatalf("remove user did not reach runtime: tag=%q email=%q", rt.removedTag, rt.removedEmail)
	}

	stats := c.statsFrame(ctx).GetStats()
	if len(stats.GetEntries()) != 1 || stats.GetEntries()[0].GetUplinkBytes() != 12 || stats.GetEntries()[0].GetDownlinkBytes() != 34 {
		t.Fatalf("unexpected traffic frame: %+v", stats)
	}
	online := c.onlineFrame(ctx).GetOnline()
	if !online.GetComplete() || len(online.GetUsers()) != 1 || online.GetUsers()[0].GetEmail() != "alice@p.n" {
		t.Fatalf("unexpected online frame: %+v", online)
	}

	hb := &chiralv1.Heartbeat{}
	c.populateRuntimeHeartbeat(hb)
	if hb.GetXrayState() != rt.state || hb.GetConfigVersion() != 41 || hb.GetXrayVersion() != rt.running || hb.GetInstalledXrayVersion() != rt.installed {
		t.Fatalf("runtime status did not reach heartbeat: %+v", hb)
	}
	if hb.GetRuntime().GetProvider() != "direct-xray" || hb.GetRuntime().GetMode() != chiralv1.RuntimeMode_RUNTIME_MODE_ACTIVE {
		t.Fatalf("direct runtime identity did not reach heartbeat: %+v", hb.GetRuntime())
	}
}

func TestClientReportsShadowRuntimeObservation(t *testing.T) {
	want := &chiralv1.RuntimeStatus{
		Provider:           "3x-ui",
		Mode:               chiralv1.RuntimeMode_RUNTIME_MODE_SHADOW,
		Health:             chiralv1.RuntimeHealth_RUNTIME_HEALTH_READY,
		ObservedAtUnix:     1234,
		ObservedAgeSeconds: 42,
		Version:            "v3.7.0",
		Capabilities:       []string{"status", "inbounds_list"},
		ContractDigest:     "0123456789abcdef",
		Error:              "",
		XrayState:          chiralv1.XrayState_XRAY_STATE_STOPPED,
		XrayVersion:        "25.8.3",
	}
	c := NewWithRuntime(
		Config{
			StateDir: t.TempDir(),
			RuntimeStatusSnapshot: func() *chiralv1.RuntimeStatus {
				return want
			},
		},
		&fakeRuntime{},
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	hb := &chiralv1.Heartbeat{}
	c.populateRuntimeHeartbeat(hb)
	if hb.GetRuntime() == want {
		t.Fatal("heartbeat retained the observer's mutable protobuf pointer")
	}
	if hb.GetRuntime().GetProvider() != want.GetProvider() || hb.GetRuntime().GetHealth() != want.GetHealth() || hb.GetRuntime().GetObservedAtUnix() != want.GetObservedAtUnix() || hb.GetRuntime().GetObservedAgeSeconds() != want.GetObservedAgeSeconds() || hb.GetRuntime().GetXrayState() != want.GetXrayState() || hb.GetRuntime().GetXrayVersion() != want.GetXrayVersion() {
		t.Fatalf("shadow runtime observation did not reach heartbeat: %+v", hb.GetRuntime())
	}
}

func TestGenericRuntimeWithoutStatusIsExplicitlyUnknown(t *testing.T) {
	c := NewWithRuntime(
		Config{StateDir: t.TempDir()},
		&fakeRuntime{},
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	hb := &chiralv1.Heartbeat{}
	c.populateRuntimeHeartbeat(hb)
	if hb.GetRuntime().GetProvider() != "unknown" || hb.GetRuntime().GetMode() != chiralv1.RuntimeMode_RUNTIME_MODE_UNSPECIFIED {
		t.Fatalf("generic runtime was misidentified: %+v", hb.GetRuntime())
	}
}

func TestNilRuntimeSnapshotIsExplicitlyUnknown(t *testing.T) {
	c := NewWithRuntime(
		Config{
			StateDir:              t.TempDir(),
			RuntimeStatusSnapshot: func() *chiralv1.RuntimeStatus { return nil },
		},
		&fakeRuntime{},
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	hb := &chiralv1.Heartbeat{}
	c.populateRuntimeHeartbeat(hb)
	if hb.GetRuntime().GetProvider() != "unknown" {
		t.Fatalf("nil snapshot was mistaken for a legacy direct agent: %+v", hb.GetRuntime())
	}
}

func TestRuntimeWithoutDirectXrayOwnerRejectsLegacyInstall(t *testing.T) {
	rt := &fakeRuntime{running: "running", installed: "installed"}
	c := NewWithRuntime(
		Config{StateDir: t.TempDir()},
		rt,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	send := make(chan *chiralv1.AgentFrame, 1)
	c.startInstall(context.Background(), send, &chiralv1.XrayInstall{Version: "next"})
	status := (<-send).GetXrayStatus()
	if status == nil || status.GetPhase() != chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED {
		t.Fatalf("unexpected install status: %+v", status)
	}
	if status.GetRunningVersion() != "running" || status.GetInstalledVersion() != "installed" {
		t.Fatalf("runtime versions missing from install status: %+v", status)
	}
}

var _ runtimeprovider.Runtime = (*fakeRuntime)(nil)

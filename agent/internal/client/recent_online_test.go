package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type fakeRecentRuntime struct {
	fakeRuntime
	sample runtimeprovider.RecentOnlineSample
	err    error
	calls  int
	onRead func()
}

func (f *fakeRecentRuntime) RecentOnline(context.Context) (runtimeprovider.RecentOnlineSample, error) {
	f.calls++
	if f.onRead != nil {
		f.onRead()
	}
	return f.sample, f.err
}

func (*fakeRecentRuntime) OnlineUsers(context.Context) ([]runtimeprovider.OnlineUser, bool, error) {
	panic("approximate runtime must not fall back to an exact snapshot")
}

func TestRecentOnlineRequiresPerStreamNegotiation(t *testing.T) {
	rt := &fakeRecentRuntime{sample: runtimeprovider.RecentOnlineSample{RetrievedAt: time.Now()}}
	c := NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, policy := range []*chiralv1.OnlinePolicy{
		nil,
		{Enabled: true}, // Old Core has not negotiated the new frame.
		{RecentObservations: true},
	} {
		c.onlinePolicy = policy
		if c.onlineFrame(context.Background()) != nil || rt.calls != 0 {
			t.Fatal("unnegotiated collection or report")
		}
	}
	c.onlinePolicy = &chiralv1.OnlinePolicy{Enabled: true, RecentObservations: true}
	frame := c.onlineFrame(context.Background())
	if frame.GetOnline() != nil || !frame.GetRecentOnline().GetReadOk() || rt.calls != 1 {
		t.Fatal("recent report did not use its distinct frame")
	}
	c.onlinePolicy = nil // The stream's existing reset path clears negotiation.
	if c.onlineFrame(context.Background()) != nil || rt.calls != 1 {
		t.Fatal("new stream retained permission")
	}
}

func TestRecentOnlineFailureIsUnknownAndDoesNotLeakProviderError(t *testing.T) {
	var logs bytes.Buffer
	rt := &fakeRecentRuntime{
		sample: runtimeprovider.RecentOnlineSample{RetrievedAt: time.Now(), Users: []runtimeprovider.RecentOnlineUser{{Email: "private-email"}}},
		err:    errors.New("private-error-including-address"),
		onRead: func() { time.Sleep(time.Millisecond) },
	}
	c := NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	c.onlinePolicy = &chiralv1.OnlinePolicy{Enabled: true, RecentObservations: true}
	report := c.onlineFrame(context.Background()).GetRecentOnline()
	if report == nil || report.ReadOk || len(report.Users) != 0 || strings.Contains(logs.String(), "private-") {
		t.Fatal("failure was disguised as empty success or leaked a provider error")
	}
	if report.RetrievedAgeSeconds == 0 {
		t.Fatal("failed HTTP read lost its attempt age")
	}
}

func TestRecentOnlinePolicyRevocationDuringReadDropsReport(t *testing.T) {
	rt := &fakeRecentRuntime{sample: runtimeprovider.RecentOnlineSample{RetrievedAt: time.Now()}}
	c := NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.onlinePolicy = &chiralv1.OnlinePolicy{Enabled: true, RecentObservations: true}
	rt.onRead = func() {
		c.onlineMu.Lock()
		c.onlinePolicy = nil
		c.onlineMu.Unlock()
	}
	if c.onlineFrame(context.Background()) != nil {
		t.Fatal("report sent after policy was revoked")
	}
}

func TestRecentOnlineCanceledStreamDoesNotCollect(t *testing.T) {
	rt := &fakeRecentRuntime{}
	c := NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c.onlinePolicy = &chiralv1.OnlinePolicy{Enabled: true, RecentObservations: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.onlineFrame(ctx) != nil || rt.calls != 0 {
		t.Fatal("canceled stream collected observations")
	}
}

func TestRecentOnlinePolicyReenableDoesNotAuthorizeOldEvidence(t *testing.T) {
	rt := &fakeRecentRuntime{sample: runtimeprovider.RecentOnlineSample{RetrievedAt: time.Now()}}
	c := NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	setEnabled := func(enabled bool) {
		c.handleFrame(context.Background(), nil, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_OnlinePolicy{
			OnlinePolicy: &chiralv1.OnlinePolicy{Enabled: enabled, RecentObservations: true},
		}})
	}
	setEnabled(true)
	rt.onRead = func() {
		setEnabled(false)
		setEnabled(true)
	}
	if c.onlineFrame(context.Background()) != nil {
		t.Fatal("reenabling policy authorized a read collected before revocation")
	}
	rt.onRead = nil
	_, epoch := c.recentOnlinePolicy()
	queued := queuedRecentOnline{frame: c.onlineFrame(context.Background()), queuedAt: time.Now(), policyEpoch: epoch}
	setEnabled(false)
	setEnabled(true)
	if c.prepareRecentOnlineForSend(context.Background(), queued, time.Now()) != nil {
		t.Fatal("reenabling policy authorized queued evidence collected before revocation")
	}
}

func TestQueuedRecentOnlineIncludesBackpressureAndRechecksPermission(t *testing.T) {
	now := time.Now()
	c := &Client{onlinePolicy: &chiralv1.OnlinePolicy{Enabled: true, RecentObservations: true}}
	queued := queuedRecentOnline{queuedAt: now.Add(-1500 * time.Millisecond), frame: &chiralv1.AgentFrame{
		Frame: &chiralv1.AgentFrame_RecentOnline{RecentOnline: &chiralv1.RecentOnlineReport{
			ReadOk: true, RetrievedAgeSeconds: 3,
		}},
	}}
	if f := c.prepareRecentOnlineForSend(context.Background(), queued, now); f.GetRecentOnline().GetRetrievedAgeSeconds() != 5 {
		t.Fatal("writer backpressure refreshed an old sample")
	}
	if queued.frame.GetRecentOnline().RetrievedAgeSeconds != 3 {
		t.Fatal("dispatch mutated queued evidence")
	}
	queued.frame.GetRecentOnline().ReadOk = false
	if f := c.prepareRecentOnlineForSend(context.Background(), queued, now); f.GetRecentOnline().GetRetrievedAgeSeconds() != 5 || f.GetRecentOnline().ReadOk {
		t.Fatal("writer backpressure refreshed a failed read or turned it into success")
	}
	c.onlinePolicy = nil
	if c.prepareRecentOnlineForSend(context.Background(), queued, now) != nil {
		t.Fatal("revoked report was dispatched from the writer queue")
	}
	c.onlinePolicy = &chiralv1.OnlinePolicy{Enabled: true, RecentObservations: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if c.prepareRecentOnlineForSend(ctx, queued, now) != nil {
		t.Fatal("canceled report was dispatched")
	}
	queued.frame.GetRecentOnline().RetrievedAgeSeconds = math.MaxUint32
	if c.prepareRecentOnlineForSend(context.Background(), queued, now) != nil {
		t.Fatal("overflowed age refreshed stale evidence")
	}
}

func TestRecentOnlineReportUsesReadAgeNotInventedLastSeen(t *testing.T) {
	now := time.Now()
	report := recentOnlineReport(runtimeprovider.RecentOnlineSample{
		RetrievedAt: now.Add(-1500 * time.Millisecond),
		Users: []runtimeprovider.RecentOnlineUser{{Email: "private-email", ObservedOnline: true,
			IPs: []string{"::ffff:203.0.113.7", "203.0.113.7", "2001:db8::1"}}},
	}, now)
	if !report.ReadOk || report.RetrievedAgeSeconds != 2 || len(report.Users) != 1 || len(report.Users[0].Ips) != 2 || !report.Users[0].ObservedOnline {
		t.Fatalf("incorrect recent sample encoding: %+v", report)
	}
}

func TestRecentOnlineRejectsInvalidOrOversizedSample(t *testing.T) {
	now := time.Now()
	for _, sample := range []runtimeprovider.RecentOnlineSample{
		{},
		{RetrievedAt: now.Add(time.Second)},
		{RetrievedAt: now, Users: make([]runtimeprovider.RecentOnlineUser, chiralv1.MaxRecentOnlineUsers+1)},
		{RetrievedAt: now, Users: []runtimeprovider.RecentOnlineUser{{Email: ""}}},
		{RetrievedAt: now, Users: []runtimeprovider.RecentOnlineUser{{Email: "same"}, {Email: "same"}}},
		{RetrievedAt: now, Users: []runtimeprovider.RecentOnlineUser{{Email: strings.Repeat("e", chiralv1.MaxRecentOnlineEmailBytes+1)}}},
		{RetrievedAt: now, Users: []runtimeprovider.RecentOnlineUser{{Email: "unsafe\nemail"}}},
		{RetrievedAt: now, Users: []runtimeprovider.RecentOnlineUser{{Email: "e", IPs: []string{"invalid"}}}},
		{RetrievedAt: now, Users: []runtimeprovider.RecentOnlineUser{{Email: "e", IPs: []string{"fe80::1%en0"}}}},
		{RetrievedAt: now, Users: []runtimeprovider.RecentOnlineUser{{Email: "e", IPs: make([]string, chiralv1.MaxRecentOnlineIPs+1)}}},
	} {
		if report := recentOnlineReport(sample, now); report.ReadOk || len(report.Users) != 0 {
			t.Fatal("invalid sample was emitted as successful evidence")
		}
	}
}

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

const statsReporterID = "0123456789abcdef0123456789abcdef"

type acknowledgedTestRuntime struct {
	fakeRuntime
	batch       *runtimeprovider.AccountingBatch
	batchErr    error
	batchCalls  int
	legacyCalls int
	journal     *runtimeprovider.Accounting
	ackErr      error
	ackID       string
	ackSequence uint64
}

func (r *acknowledgedTestRuntime) Stats(context.Context) ([]runtimeprovider.Stat, error) {
	r.legacyCalls++
	return nil, errors.New("acknowledged runtime must not use destructive legacy reads")
}

func (r *acknowledgedTestRuntime) StatsBatch(context.Context) (*runtimeprovider.AccountingBatch, error) {
	r.batchCalls++
	return r.batch, r.batchErr
}

func (r *acknowledgedTestRuntime) AcknowledgeStats(id string, sequence uint64) error {
	r.ackID, r.ackSequence = id, sequence
	if r.ackErr != nil {
		return r.ackErr
	}
	if r.journal != nil {
		return r.journal.Ack(fmt.Sprintf("%s:%d", id, sequence))
	}
	return nil
}

func acknowledgedClient(t *testing.T, rt *acknowledgedTestRuntime) *Client {
	t.Helper()
	return NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func setStatsPolicy(c *Client, enabled bool) {
	c.handleFrame(context.Background(), nil, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_StatsPolicy{
		StatsPolicy: &chiralv1.StatsPolicy{AcknowledgedDeltas: enabled},
	}})
}

func TestAcknowledgedStatsWaitForExplicitPolicyAndNeverFallBack(t *testing.T) {
	rt := &acknowledgedTestRuntime{batch: &runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: 1}}
	c := acknowledgedClient(t, rt)
	for _, enabled := range []bool{false, false} {
		if frame := c.statsFrame(context.Background()); frame != nil {
			t.Fatalf("unnegotiated report: %v", frame)
		}
		setStatsPolicy(c, enabled)
	}
	if rt.batchCalls != 0 || rt.legacyCalls != 0 {
		t.Fatalf("old Core consumed counters: batch=%d legacy=%d", rt.batchCalls, rt.legacyCalls)
	}
	setStatsPolicy(c, true)
	if c.statsFrame(context.Background()) == nil || rt.batchCalls != 1 || rt.legacyCalls != 0 {
		t.Fatal("explicit policy did not select acknowledged reporting")
	}
	setStatsPolicy(c, false)
	if c.statsFrame(context.Background()) != nil || rt.batchCalls != 1 || rt.legacyCalls != 0 {
		t.Fatal("withdrawn policy still consumed counters")
	}
}

type unavailableStatsService struct{ chiralv1.AgentServiceClient }

func (unavailableStatsService) Channel(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[chiralv1.AgentFrame, chiralv1.CoreFrame], error) {
	return nil, errors.New("simulated reconnect failure")
}

func TestAcknowledgedStatsNegotiationDoesNotSurviveReconnect(t *testing.T) {
	rt := &acknowledgedTestRuntime{}
	c := acknowledgedClient(t, rt)
	setStatsPolicy(c, true)
	if err := c.runStream(context.Background(), unavailableStatsService{}); err == nil {
		t.Fatal("simulated Channel failure was lost")
	}
	if c.statsFrame(context.Background()) != nil || rt.batchCalls != 0 || rt.legacyCalls != 0 {
		t.Fatal("a previous connection's policy survived reconnect")
	}
}

func TestAcknowledgedStatsPreserveIdentityAndAllScopesOnTheWire(t *testing.T) {
	rt := &acknowledgedTestRuntime{batch: &runtimeprovider.AccountingBatch{
		ReporterID: statsReporterID, Sequence: 7,
		Entries: []runtimeprovider.CounterDelta{
			{Key: "user>>>alice@profile.node", Up: 1<<53 + 1, Down: 13},
			{Key: "inbound>>>entry", Up: 21, Down: 34},
			{Key: "outbound>>>exit", Up: 55, Down: 89},
		},
	}}
	c := acknowledgedClient(t, rt)
	setStatsPolicy(c, true)
	want := &chiralv1.StatsReport{ReporterId: statsReporterID, Sequence: 7, Entries: []*chiralv1.StatEntry{
		{Scope: chiralv1.StatScope_STAT_SCOPE_USER, Name: "alice@profile.node", UplinkBytes: 1<<53 + 1, DownlinkBytes: 13},
		{Scope: chiralv1.StatScope_STAT_SCOPE_INBOUND, Name: "entry", UplinkBytes: 21, DownlinkBytes: 34},
		{Scope: chiralv1.StatScope_STAT_SCOPE_OUTBOUND, Name: "exit", UplinkBytes: 55, DownlinkBytes: 89},
	}}
	for i := 0; i < 2; i++ {
		frame := c.statsFrame(context.Background())
		raw, err := proto.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		var decoded chiralv1.AgentFrame
		if err := proto.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if !proto.Equal(decoded.GetStats(), want) {
			t.Fatalf("report/retry changed its identity or counters: %v", decoded.GetStats())
		}
	}
	if rt.legacyCalls != 0 {
		t.Fatal("legacy statistics path was called")
	}
}

func TestAcknowledgedStatsRejectMalformedRuntimeBatches(t *testing.T) {
	oversized := make([]runtimeprovider.CounterDelta, 4097)
	for i := range oversized {
		oversized[i] = runtimeprovider.CounterDelta{Key: fmt.Sprintf("inbound>>>entry-%d", i), Up: 1}
	}
	tests := []struct {
		name  string
		batch runtimeprovider.AccountingBatch
	}{
		{"empty_identity", runtimeprovider.AccountingBatch{}},
		{"invalid_reporter", runtimeprovider.AccountingBatch{ReporterID: "not-hex", Sequence: 1}},
		{"uppercase_reporter", runtimeprovider.AccountingBatch{ReporterID: strings.ToUpper(statsReporterID), Sequence: 1}},
		{"zero_sequence", runtimeprovider.AccountingBatch{ReporterID: statsReporterID}},
		{"overflow_sequence", runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: math.MaxInt64 + 1}},
		{"batch_4097", runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: 1, Entries: oversized}},
		{"name_513_bytes", runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: 1, Entries: []runtimeprovider.CounterDelta{{Key: "user>>>" + strings.Repeat("x", 513), Up: 1}}}},
		{"invalid_utf8", runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: 1, Entries: []runtimeprovider.CounterDelta{{Key: "user>>>invalid\xff", Up: 1}}}},
		{"duplicate_entries", runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: 1, Entries: []runtimeprovider.CounterDelta{{Key: "user>>>alice", Up: 1}, {Key: "user>>>alice", Down: 1}}}},
		{"gap_with_entries", runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: 1, Indeterminate: true, Entries: []runtimeprovider.CounterDelta{{Key: "user>>>alice", Up: 1}}}},
	}
	for _, delta := range []runtimeprovider.CounterDelta{
		{Key: "broken", Up: 1}, {Key: "user>>>", Up: 1}, {Key: "unknown>>>x", Up: 1},
		{Key: "user>>>x", Up: math.MaxInt64 + 1}, {Key: "user>>>x", Up: math.MaxInt64, Down: 1},
	} {
		tests = append(tests, struct {
			name  string
			batch runtimeprovider.AccountingBatch
		}{delta.Key + fmt.Sprint(delta.Up, delta.Down), runtimeprovider.AccountingBatch{
			ReporterID: statsReporterID, Sequence: 1, Entries: []runtimeprovider.CounterDelta{delta},
		}})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &acknowledgedTestRuntime{batch: &tt.batch}
			c := acknowledgedClient(t, rt)
			setStatsPolicy(c, true)
			if got := c.statsFrame(context.Background()); got != nil {
				t.Fatalf("malformed acknowledged batch became a report: %v", got)
			}
			if rt.legacyCalls != 0 {
				t.Fatal("invalid acknowledged report fell back to legacy reads")
			}
		})
	}
}

func TestAcknowledgedContinuityGapSurvivesFullEventQueueAndWireReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gap.json")
	journal, err := runtimeprovider.OpenAccounting(path, "provider", runtimeprovider.BaselineOnly)
	if err != nil {
		t.Fatal(err)
	}
	if batch, err := journal.Sample("old-database", []runtimeprovider.CumulativeCounter{{Key: "user>>>alice", Up: 100}}); err != nil || batch != nil {
		t.Fatalf("initial baseline: batch=%v err=%v", batch, err)
	}
	batch, err := journal.Sample("restored-database", []runtimeprovider.CumulativeCounter{{Key: "user>>>alice", Up: 50}})
	if err != nil || batch == nil || !batch.Indeterminate {
		t.Fatalf("restore did not produce a durable gap: batch=%v err=%v", batch, err)
	}
	rt := &acknowledgedTestRuntime{batch: batch, journal: journal}
	c := acknowledgedClient(t, rt)
	for len(c.events) < cap(c.events) {
		c.QueueEvent(chiralv1.EventKind_EVENT_KIND_ERROR, "unrelated event")
	}
	setStatsPolicy(c, true)
	var original *chiralv1.StatsReport
	for i := 0; i < 2; i++ {
		frame := c.statsFrame(context.Background())
		if frame == nil {
			t.Fatal("a full best-effort event queue suppressed the durable gap")
		}
		raw, err := proto.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		var decoded chiralv1.AgentFrame
		if err := proto.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		report := decoded.GetStats()
		if !report.GetContinuityIndeterminate() || len(report.GetEntries()) != 0 || report.GetReporterId() != batch.ReporterID || report.GetSequence() != batch.Sequence {
			t.Fatalf("continuity marker or identity was lost on the wire: %v", report)
		}
		if original != nil && !proto.Equal(original, report) {
			t.Fatal("reopening/replaying changed the durable gap")
		}
		original = report
		// Simulate disconnect before the receipt: no event was delivered, and
		// reopening the journal must still return the same gap report.
		journal, err = runtimeprovider.OpenAccounting(path, "provider", runtimeprovider.BaselineOnly)
		if err != nil {
			t.Fatal(err)
		}
		rt.batch, err = journal.Sample("restored-database", nil)
		if err != nil {
			t.Fatal(err)
		}
		rt.journal = journal
	}
	for len(c.events) > 0 {
		if event := <-c.events; event.GetMessage() != "unrelated event" {
			t.Fatal("test did not actually exercise a dropped continuity event")
		}
	}
	c.handleFrame(context.Background(), nil, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_StatsAck{
		StatsAck: &chiralv1.StatsAck{ReporterId: batch.ReporterID, Sequence: batch.Sequence},
	}})
	journal, err = runtimeprovider.OpenAccounting(path, "provider", runtimeprovider.BaselineOnly)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := journal.Sample("restored-database", []runtimeprovider.CumulativeCounter{{Key: "user>>>alice", Up: 50}}); err != nil || pending != nil {
		t.Fatalf("acknowledged gap was not durably released: pending=%v err=%v", pending, err)
	}
}

func TestAcknowledgedStatsLargestValidBatchFitsDefaultGRPCFrame(t *testing.T) {
	entries := make([]runtimeprovider.CounterDelta, 4096)
	for i := range entries {
		// Exercise the longest permitted names and varints together. The wire
		// bounds must fit gRPC's default 4 MiB receive limit without splitting
		// an already durable pending batch after its sequence was allocated.
		name := fmt.Sprintf("%04x", i) + strings.Repeat("x", 508)
		entries[i] = runtimeprovider.CounterDelta{Key: "inbound>>>" + name, Up: math.MaxInt64 / 2, Down: math.MaxInt64 / 2}
	}
	rt := &acknowledgedTestRuntime{batch: &runtimeprovider.AccountingBatch{
		ReporterID: statsReporterID, Sequence: math.MaxInt64, Entries: entries,
	}}
	c := acknowledgedClient(t, rt)
	setStatsPolicy(c, true)
	frame := c.statsFrame(context.Background())
	if frame == nil || len(frame.GetStats().GetEntries()) != 4096 {
		t.Fatal("largest valid batch was rejected or truncated")
	}
	if size := proto.Size(frame); size >= 4<<20 {
		t.Fatalf("largest valid batch is %d bytes; cannot traverse default gRPC transport", size)
	}
}

func TestAcknowledgedStatsReadFailureDoesNotFallBack(t *testing.T) {
	rt := &acknowledgedTestRuntime{batchErr: errors.New("provider unavailable")}
	c := acknowledgedClient(t, rt)
	setStatsPolicy(c, true)
	if got := c.statsFrame(context.Background()); got != nil || rt.legacyCalls != 0 {
		t.Fatalf("failed read produced a frame or a legacy read: %v", got)
	}
}

func TestStatsAcknowledgementOnlyClearsExactDurablePendingBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic.json")
	open := func() *runtimeprovider.Accounting {
		t.Helper()
		journal, err := runtimeprovider.OpenAccounting(path, "provider", runtimeprovider.CountFromZero)
		if err != nil {
			t.Fatal(err)
		}
		return journal
	}
	journal := open()
	batch, err := journal.Sample("database", []runtimeprovider.CumulativeCounter{{Key: "user>>>alice", Up: 10}})
	if err != nil || batch == nil {
		t.Fatalf("create pending: %v, %v", batch, err)
	}
	rt := &acknowledgedTestRuntime{journal: journal}
	c := acknowledgedClient(t, rt)
	ack := func(id string, sequence uint64) {
		c.handleFrame(context.Background(), nil, &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_StatsAck{
			StatsAck: &chiralv1.StatsAck{ReporterId: id, Sequence: sequence},
		}})
	}
	for _, bad := range []struct {
		id  string
		seq uint64
	}{{"", 0}, {statsReporterID, batch.Sequence}, {batch.ReporterID, 0}, {batch.ReporterID, 2}, {batch.ReporterID, math.MaxInt64 + 1}} {
		ack(bad.id, bad.seq)
		rt.journal = open()
		pending, err := rt.journal.Sample("database", nil)
		if err != nil || pending == nil || pending.ID != batch.ID {
			t.Fatalf("invalid receipt cleared pending report: %v, %v", pending, err)
		}
	}
	rt.ackErr = errors.New("simulated journal write failure")
	ack(batch.ReporterID, batch.Sequence)
	if pending, err := open().Sample("database", nil); err != nil || pending == nil {
		t.Fatalf("failed receipt persistence cleared report: %v, %v", pending, err)
	}
	rt.ackErr = nil
	ack(batch.ReporterID, batch.Sequence)
	if rt.ackID != batch.ReporterID || rt.ackSequence != batch.Sequence {
		t.Fatal("receipt identity changed during dispatch")
	}
	rt.journal = open()
	ack(batch.ReporterID, batch.Sequence) // Duplicate receipt is harmless.
	if pending, err := open().Sample("database", []runtimeprovider.CumulativeCounter{{Key: "user>>>alice", Up: 10}}); err != nil || pending != nil {
		t.Fatalf("exact receipt was not durable: %v, %v", pending, err)
	}
}

// This provider deliberately finishes a started sample after cancellation, as
// a real implementation can be between its completed HTTP read and atomic
// journal write. The Client must serialize whole calls across stream contexts.
type blockedStatsRuntime struct {
	fakeRuntime
	calls   atomic.Int32
	entered chan int32
	release chan struct{}
}

func (r *blockedStatsRuntime) StatsBatch(context.Context) (*runtimeprovider.AccountingBatch, error) {
	sequence := r.calls.Add(1)
	r.entered <- sequence
	<-r.release
	return &runtimeprovider.AccountingBatch{ReporterID: statsReporterID, Sequence: uint64(sequence)}, nil
}

func (r *blockedStatsRuntime) AcknowledgeStats(string, uint64) error { return nil }

func blockedStatsClient(t *testing.T) (*Client, *blockedStatsRuntime) {
	t.Helper()
	rt := &blockedStatsRuntime{entered: make(chan int32, 2), release: make(chan struct{})}
	t.Cleanup(func() { close(rt.release) })
	c := NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	setStatsPolicy(c, true)
	return c, rt
}

func awaitStatsPoll(t *testing.T, entered <-chan int32, want int32) {
	t.Helper()
	select {
	case sequence := <-entered:
		if sequence != want {
			t.Fatalf("provider call=%d, want=%d", sequence, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("provider call did not start")
	}
}

func awaitStatsFrame(t *testing.T, frames <-chan *chiralv1.AgentFrame) *chiralv1.AgentFrame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("statistics poll did not finish")
		return nil
	}
}

func TestAcknowledgedStatsSerializeSnapshotsAcrossStreamContexts(t *testing.T) {
	c, rt := blockedStatsClient(t)
	oldContext, cancelOld := context.WithCancel(context.Background())
	defer cancelOld()
	oldFrames, newFrames := make(chan *chiralv1.AgentFrame, 1), make(chan *chiralv1.AgentFrame, 1)
	go func() { oldFrames <- c.statsFrame(oldContext) }()
	awaitStatsPoll(t, rt.entered, 1)
	cancelOld()
	// A new stream can negotiate while the cancelled old provider read is
	// finishing. It must not start reading a later snapshot in parallel.
	setStatsPolicy(c, false)
	setStatsPolicy(c, true)
	started := make(chan struct{})
	go func() {
		close(started)
		newFrames <- c.statsFrame(context.Background())
	}()
	<-started
	select {
	case <-rt.entered:
		t.Fatal("new stream overlapped an unfinished old snapshot")
	case <-time.After(50 * time.Millisecond):
	}
	rt.release <- struct{}{}
	if frame := awaitStatsFrame(t, oldFrames); frame != nil {
		t.Fatal("cancelled stream emitted its now-durable batch")
	}
	awaitStatsPoll(t, rt.entered, 2)
	rt.release <- struct{}{}
	if frame := awaitStatsFrame(t, newFrames); frame.GetStats().GetSequence() != 2 {
		t.Fatalf("new stream did not resume after the old poll: %v", frame)
	}
}

func TestAcknowledgedStatsCancelledContextNeverCallsProvider(t *testing.T) {
	c, rt := blockedStatsClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.statsFrame(ctx); got != nil || rt.calls.Load() != 0 {
		t.Fatalf("already-cancelled stream consumed counters: frame=%v calls=%d", got, rt.calls.Load())
	}
}

func TestAcknowledgedStatsRecheckCancellationAfterWaitingForPoll(t *testing.T) {
	c, rt := blockedStatsClient(t)
	firstFrames, waitingFrames := make(chan *chiralv1.AgentFrame, 1), make(chan *chiralv1.AgentFrame, 1)
	go func() { firstFrames <- c.statsFrame(context.Background()) }()
	awaitStatsPoll(t, rt.entered, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	go func() {
		close(started)
		waitingFrames <- c.statsFrame(ctx)
	}()
	<-started
	select {
	case <-rt.entered:
		t.Fatal("waiting poll overlapped the current snapshot")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	rt.release <- struct{}{}
	if awaitStatsFrame(t, firstFrames) == nil {
		t.Fatal("uncancelled first poll was dropped")
	}
	if frame := awaitStatsFrame(t, waitingFrames); frame != nil || rt.calls.Load() != 1 {
		t.Fatalf("poll cancelled while waiting still called provider: frame=%v calls=%d", frame, rt.calls.Load())
	}
}

func TestLegacyStatsStillWorkWithoutAcknowledgementNegotiation(t *testing.T) {
	rt := &fakeRuntime{stats: []runtimeprovider.Stat{{Name: "user>>>alice>>>traffic>>>uplink", Value: 7}}}
	c := NewWithRuntime(Config{StateDir: t.TempDir()}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	frame := c.statsFrame(context.Background())
	stats := frame.GetStats()
	if len(stats.GetEntries()) != 1 || stats.GetEntries()[0].GetUplinkBytes() != 7 || stats.GetReporterId() != "" || stats.GetSequence() != 0 {
		t.Fatalf("legacy destructive statistics path changed: %v", frame)
	}
}

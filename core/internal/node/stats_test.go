package node

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

const acknowledgedReporterID = "0123456789abcdef0123456789abcdef"

type statsServiceFixture struct {
	svc                   *Service
	st                    *store.Store
	db                    *sql.DB
	session               *Session
	userID, nodeID, email string
}

func newStatsServiceFixture(t *testing.T) *statsServiceFixture {
	t.Helper()
	box, err := secret.NewBox("node-stats-test-key-0123456789abc")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "stats.db")
	st, err := store.Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(st, NewManager(time.Minute, logger), st, logger)
	userID, nodeID, email := seedCredential(t, st, "subscriber", "node")
	return &statsServiceFixture{svc: svc, st: st, db: db, userID: userID, nodeID: nodeID, email: email,
		session: &Session{nodeID: nodeID, send: make(chan *chiralv1.CoreFrame, sendQueueSize), done: make(chan struct{})}}
}

func (f *statsServiceFixture) report(sequence uint64) *chiralv1.StatsReport {
	return &chiralv1.StatsReport{ReporterId: acknowledgedReporterID, Sequence: sequence, Entries: []*chiralv1.StatEntry{
		{Scope: chiralv1.StatScope_STAT_SCOPE_USER, Name: f.email, UplinkBytes: 11, DownlinkBytes: 23},
		{Scope: chiralv1.StatScope_STAT_SCOPE_INBOUND, Name: "entry", UplinkBytes: 101, DownlinkBytes: 203},
		{Scope: chiralv1.StatScope_STAT_SCOPE_OUTBOUND, Name: "exit", UplinkBytes: 307, DownlinkBytes: 409},
	}}
}

func (f *statsServiceFixture) deliver(report *chiralv1.StatsReport) {
	f.svc.handleFrame(f.nodeID, f.session, &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Stats{Stats: report}})
}

func requireStatsAck(t *testing.T, session *Session, report *chiralv1.StatsReport) {
	t.Helper()
	select {
	case frame := <-session.send:
		ack := frame.GetStatsAck()
		if ack == nil || ack.GetReporterId() != report.GetReporterId() || ack.GetSequence() != report.GetSequence() {
			t.Fatalf("incorrect traffic receipt: %v", frame)
		}
	default:
		t.Fatal("committed statistics did not receive a receipt")
	}
}

func requireNoStatsAck(t *testing.T, session *Session) {
	t.Helper()
	select {
	case frame := <-session.send:
		t.Fatalf("failed statistics received a frame: %v", frame)
	default:
	}
}

func (f *statsServiceFixture) requireTotals(t *testing.T, batches int64) {
	t.Helper()
	u, err := f.st.GetUser(f.userID)
	if err != nil || u.UsedBytes != 34*batches {
		t.Fatalf("quota usage=%d, want=%d, error=%v", u.UsedBytes, 34*batches, err)
	}
	points, err := f.st.TrafficSeries(f.userID, f.nodeID, time.Now().Add(-24*time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var up, down int64
	for _, point := range points {
		up, down = up+point.UpBytes, down+point.DownBytes
	}
	if up != 11*batches || down != 23*batches {
		t.Fatalf("history up/down=%d/%d, want=%d/%d", up, down, 11*batches, 23*batches)
	}
	var runtimeUp, runtimeDown int64
	if err := f.db.QueryRow(`SELECT COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
		FROM runtime_traffic_totals WHERE node_id = ?`, f.nodeID).Scan(&runtimeUp, &runtimeDown); err != nil {
		t.Fatal(err)
	}
	if runtimeUp != 408*batches || runtimeDown != 612*batches {
		t.Fatalf("runtime telemetry up/down=%d/%d, want=%d/%d", runtimeUp, runtimeDown, 408*batches, 612*batches)
	}
}

func TestAcknowledgedStatsCommitBeforeAckAndDeduplicateRetry(t *testing.T) {
	f := newStatsServiceFixture(t)
	report := f.report(1)
	f.deliver(report)
	f.requireTotals(t, 1)
	requireStatsAck(t, f.session, report)
	f.deliver(proto.Clone(report).(*chiralv1.StatsReport))
	f.requireTotals(t, 1)
	requireStatsAck(t, f.session, report)
	second := f.report(2)
	f.deliver(second)
	f.requireTotals(t, 2)
	requireStatsAck(t, f.session, second)
	f.deliver(report) // Old, already committed sequence must not bill again.
	f.requireTotals(t, 2)
	requireStatsAck(t, f.session, report)
}

func TestAcknowledgedStatsLostQueueReceiptIsSafeToRetry(t *testing.T) {
	f := newStatsServiceFixture(t)
	for i := 0; i < cap(f.session.send); i++ {
		f.session.send <- &chiralv1.CoreFrame{}
	}
	report := f.report(1)
	f.deliver(report)
	f.requireTotals(t, 1)
	for i := 0; i < cap(f.session.send); i++ {
		if got := <-f.session.send; got.GetStatsAck() != nil {
			t.Fatal("receipt unexpectedly replaced a queued command")
		}
	}
	f.deliver(report)
	f.requireTotals(t, 1)
	requireStatsAck(t, f.session, report)
}

func TestAcknowledgedStatsPersistenceFailureRollsBackAndSendsNoAck(t *testing.T) {
	f := newStatsServiceFixture(t)
	if _, err := f.db.Exec(`CREATE TRIGGER reject_stats_receipt BEFORE INSERT ON stats_receipts
		BEGIN SELECT RAISE(ABORT, 'simulated receipt persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	report := f.report(1)
	f.deliver(report)
	requireNoStatsAck(t, f.session)
	f.requireTotals(t, 0)
	if _, err := f.db.Exec(`DROP TRIGGER reject_stats_receipt`); err != nil {
		t.Fatal(err)
	}
	f.deliver(report)
	f.requireTotals(t, 1)
	requireStatsAck(t, f.session, report)
}

func TestAcknowledgedStatsRejectInvalidReportWithoutAckOrPartialCommit(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*chiralv1.StatsReport)
	}{
		{"missing_reporter", func(r *chiralv1.StatsReport) { r.ReporterId = "" }},
		{"malformed_reporter", func(r *chiralv1.StatsReport) { r.ReporterId = "invalid" }},
		{"missing_sequence", func(r *chiralv1.StatsReport) { r.Sequence = 0 }},
		{"sequence_overflow", func(r *chiralv1.StatsReport) { r.Sequence = math.MaxInt64 + 1 }},
		{"sequence_gap", func(r *chiralv1.StatsReport) { r.Sequence = 2 }},
		{"unknown_scope", func(r *chiralv1.StatsReport) { r.Entries[1].Scope = chiralv1.StatScope(99) }},
		{"nil_entry", func(r *chiralv1.StatsReport) { r.Entries[1] = nil }},
		{"empty_name", func(r *chiralv1.StatsReport) { r.Entries[1].Name = "" }},
		{"name_513_bytes", func(r *chiralv1.StatsReport) { r.Entries[1].Name = strings.Repeat("x", 513) }},
		{"invalid_utf8", func(r *chiralv1.StatsReport) { r.Entries[1].Name = "invalid\xff" }},
		{"counter_overflow", func(r *chiralv1.StatsReport) { r.Entries[1].UplinkBytes = math.MaxInt64 + 1 }},
		{"counter_sum_overflow", func(r *chiralv1.StatsReport) { r.Entries[1].UplinkBytes, r.Entries[1].DownlinkBytes = math.MaxInt64, 1 }},
		{"gap_with_entries", func(r *chiralv1.StatsReport) { r.ContinuityIndeterminate = true }},
		{"gap_missing_identity", func(r *chiralv1.StatsReport) {
			r.ReporterId, r.Sequence, r.ContinuityIndeterminate = "", 0, true
		}},
		{"duplicate_entry", func(r *chiralv1.StatsReport) {
			r.Entries = append(r.Entries, proto.Clone(r.Entries[0]).(*chiralv1.StatEntry))
		}},
		{"batch_4097", func(r *chiralv1.StatsReport) {
			r.Entries = make([]*chiralv1.StatEntry, 4097)
			for i := range r.Entries {
				r.Entries[i] = &chiralv1.StatEntry{Scope: chiralv1.StatScope_STAT_SCOPE_INBOUND, Name: fmt.Sprintf("entry-%d", i)}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newStatsServiceFixture(t)
			report := f.report(1)
			tt.mutate(report)
			f.deliver(report)
			requireNoStatsAck(t, f.session)
			f.requireTotals(t, 0)
			valid := f.report(1)
			f.deliver(valid)
			requireStatsAck(t, f.session, valid)
			f.requireTotals(t, 1)
		})
	}
}

func TestAcknowledgedContinuityGapIsCommittedBeforeAckAndRetained(t *testing.T) {
	f := newStatsServiceFixture(t)
	gap := &chiralv1.StatsReport{ReporterId: acknowledgedReporterID, Sequence: 1, ContinuityIndeterminate: true}
	// There is deliberately no separate Event frame. The gap must be durable
	// even if the Agent's best-effort event queue was full or the event was lost.
	f.deliver(gap)
	var sequence uint64
	var marked bool
	if err := f.db.QueryRow(`SELECT sequence, continuity_indeterminate FROM stats_receipts WHERE node_id = ? AND reporter_id = ?`, f.nodeID, acknowledgedReporterID).Scan(&sequence, &marked); err != nil || sequence != 1 || !marked {
		t.Fatalf("gap receipt was not committed before ACK: sequence=%d marked=%v err=%v", sequence, marked, err)
	}
	var gaps int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM stats_continuity_gaps WHERE node_id = ? AND reporter_id = ? AND sequence = 1`, f.nodeID, acknowledgedReporterID).Scan(&gaps); err != nil || gaps != 1 {
		t.Fatalf("gap evidence was not committed before ACK: gaps=%d err=%v", gaps, err)
	}
	requireStatsAck(t, f.session, gap)
	f.requireTotals(t, 0)
	// A replacement stream has no buffered event or ACK from the old stream.
	f.session = &Session{nodeID: f.nodeID, send: make(chan *chiralv1.CoreFrame, sendQueueSize), done: make(chan struct{})}
	f.deliver(gap)
	requireStatsAck(t, f.session, gap)
	f.deliver(f.report(2))
	requireStatsAck(t, f.session, f.report(2))
	f.requireTotals(t, 1)
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM stats_continuity_gaps WHERE node_id = ?`, f.nodeID).Scan(&gaps); err != nil || gaps != 1 {
		t.Fatalf("retry or later usage lost/duplicated the gap: gaps=%d err=%v", gaps, err)
	}
	changed := proto.Clone(gap).(*chiralv1.StatsReport)
	changed.ContinuityIndeterminate = false
	f.deliver(changed)
	requireNoStatsAck(t, f.session)
}

func TestAcknowledgedContinuityGapPersistenceFailureDoesNotAck(t *testing.T) {
	f := newStatsServiceFixture(t)
	if _, err := f.db.Exec(`CREATE TRIGGER reject_stats_gap BEFORE INSERT ON stats_continuity_gaps
		BEGIN SELECT RAISE(ABORT, 'simulated gap persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	gap := &chiralv1.StatsReport{ReporterId: acknowledgedReporterID, Sequence: 1, ContinuityIndeterminate: true}
	f.deliver(gap)
	requireNoStatsAck(t, f.session)
	var receipts int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM stats_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("failed gap consumed sequence: receipts=%d err=%v", receipts, err)
	}
	if _, err := f.db.Exec(`DROP TRIGGER reject_stats_gap`); err != nil {
		t.Fatal(err)
	}
	f.deliver(gap)
	requireStatsAck(t, f.session, gap)
	f.requireTotals(t, 0)
}

func TestAcknowledgedStatsRejectChangedReplayWithoutAck(t *testing.T) {
	f := newStatsServiceFixture(t)
	report := f.report(1)
	f.deliver(report)
	requireStatsAck(t, f.session, report)
	report.Entries[0].UplinkBytes++
	f.deliver(report)
	requireNoStatsAck(t, f.session)
	f.requireTotals(t, 1)
}

func TestAcknowledgedStatsRetryDoesNotReapplyChangedBillingRate(t *testing.T) {
	f := newStatsServiceFixture(t)
	if err := f.st.SetNodeTrafficRate(f.nodeID, 2.5); err != nil {
		t.Fatal(err)
	}
	report := f.report(1)
	f.deliver(report)
	requireStatsAck(t, f.session, report)
	if err := f.st.SetNodeTrafficRate(f.nodeID, 0); err != nil {
		t.Fatal(err)
	}
	// The receipt was lost at the Agent, while the administrator changed the
	// multiplier. A replay must not bill the old traffic at either rate again.
	f.deliver(report)
	requireStatsAck(t, f.session, report)
	second := f.report(2)
	f.deliver(second)
	requireStatsAck(t, f.session, second)
	user, err := f.st.GetUser(f.userID)
	if err != nil || user.UsedBytes != 85 {
		t.Fatalf("retry or rate change altered prior billing: usage=%d error=%v", user.UsedBytes, err)
	}
	creds, err := f.st.UserCredentials(f.userID)
	if err != nil || len(creds) != 1 || creds[0].UpBytes != 22 || creds[0].DownBytes != 46 {
		t.Fatalf("multiplier or retry changed raw usage: %v, %v", creds, err)
	}
}

func TestAcknowledgedStatsRejectCrossNodeCredentialAtomically(t *testing.T) {
	f := newStatsServiceFixture(t)
	victimID, _, email := seedCredential(t, f.st, "victim", "other-node")
	report := f.report(1)
	report.Entries = append(report.Entries, &chiralv1.StatEntry{
		Scope: chiralv1.StatScope_STAT_SCOPE_USER, Name: email, UplinkBytes: 999,
	})
	f.deliver(report)
	requireNoStatsAck(t, f.session)
	f.requireTotals(t, 0)
	victim, err := f.st.GetUser(victimID)
	if err != nil || victim.UsedBytes != 0 {
		t.Fatalf("another node was allowed to bill victim: %+v, %v", victim, err)
	}
	valid := f.report(1)
	f.deliver(valid)
	requireStatsAck(t, f.session, valid)
	f.requireTotals(t, 1)
}

func TestLegacyStatsRemainReceiptFree(t *testing.T) {
	f := newStatsServiceFixture(t)
	report := f.report(1)
	report.ReporterId, report.Sequence = "", 0
	f.deliver(report)
	requireNoStatsAck(t, f.session)
	user, err := f.st.GetUser(f.userID)
	if err != nil || user.UsedBytes != 34 {
		t.Fatalf("legacy direct-Xray compatibility was lost: usage=%d error=%v", user.UsedBytes, err)
	}
}

func TestLegacyStatsCannotBypassCrossNodeCredentialAuthorization(t *testing.T) {
	f := newStatsServiceFixture(t)
	victimID, _, email := seedCredential(t, f.st, "victim", "other-node")
	f.deliver(&chiralv1.StatsReport{Entries: []*chiralv1.StatEntry{{
		Scope: chiralv1.StatScope_STAT_SCOPE_USER, Name: email, UplinkBytes: 999,
	}}})
	requireNoStatsAck(t, f.session)
	victim, err := f.st.GetUser(victimID)
	if err != nil || victim.UsedBytes != 0 {
		t.Fatalf("omitting report identity bypassed node authorization: usage=%d error=%v", victim.UsedBytes, err)
	}
}

func TestAcknowledgedStatsDisabledRecorderDoesNotAck(t *testing.T) {
	f := newStatsServiceFixture(t)
	f.svc.traffic = nil
	f.deliver(f.report(1))
	requireNoStatsAck(t, f.session)
	f.requireTotals(t, 0)
}

func TestChannelNegotiatesAcknowledgedStatsAndReturnsCommittedReceipts(t *testing.T) {
	f := newStatsServiceFixture(t)
	credential := "isolated-test-node-credential"
	if _, err := f.st.RedeemJoinToken("join-hash-node", auth.HashSecret(credential), "test", "test", ""); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	chiralv1.RegisterAgentServiceServer(server, f.svc)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	t.Cleanup(func() {
		f.svc.mgr.CloseAll()
		server.Stop()
		listener.Close()
		if err := <-served; err != nil {
			t.Errorf("in-memory server exited with error: %v", err)
		}
	})
	conn, err := grpc.NewClient("passthrough:///stats-buffer", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, CredentialMetadataKey, credential)
	stream, err := chiralv1.NewAgentServiceClient(conn).Channel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Hello{Hello: &chiralv1.Hello{
		NodeId: f.nodeID, AgentVersion: "test", Platform: "linux/amd64",
	}}}); err != nil {
		t.Fatal(err)
	}
	policy, err := stream.Recv()
	if err != nil || !policy.GetStatsPolicy().GetAcknowledgedDeltas() {
		t.Fatalf("missing explicit statistics policy: %v, %v", policy, err)
	}
	report := f.report(1)
	for i := 0; i < 2; i++ {
		if err := stream.Send(&chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Stats{Stats: report}}); err != nil {
			t.Fatal(err)
		}
		frame, err := stream.Recv()
		if err != nil || frame.GetStatsAck().GetReporterId() != report.ReporterId || frame.GetStatsAck().GetSequence() != 1 {
			t.Fatalf("missing exact receipt: %v, %v", frame, err)
		}
		f.requireTotals(t, 1)
	}
}

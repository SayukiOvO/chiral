package node

import (
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/protobuf/proto"
)

func retireStatsCredential(t *testing.T, f *statsServiceFixture) {
	t.Helper()
	credentials, err := f.st.UserCredentials(f.userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 1 || credentials[0].Email != f.email {
		t.Fatalf("unexpected fixture credentials before retirement: %+v", credentials)
	}
	if err := f.st.DeleteCredentialsForBinding(f.userID, credentials[0].ProfileID); err != nil {
		t.Fatal(err)
	}
	requireRetiredStatsCredentialCannotServe(t, f)
}

func requireRetiredStatsCredentialCannotServe(t *testing.T, f *statsServiceFixture) {
	t.Helper()
	if _, _, err := f.st.CredentialOwner(f.email); !store.IsNotFound(err) {
		t.Fatalf("retired accounting identity still resolves as an authorized credential: %v", err)
	}
	credentials, err := f.st.NodeCredentials(f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 0 {
		t.Fatalf("retired accounting identity returned to node config: %+v", credentials)
	}
}

func requireRetiredStatsCounters(t *testing.T, f *statsServiceFixture, wantUp, wantDown int64) {
	t.Helper()
	var up, down int64
	var retired bool
	if err := f.db.QueryRow(`SELECT up_bytes, down_bytes, retired_at IS NOT NULL
		FROM credential_traffic_owners WHERE email = ?`, f.email).Scan(&up, &down, &retired); err != nil {
		t.Fatal(err)
	}
	if !retired || up != wantUp || down != wantDown {
		t.Fatalf("retired accounting counters = %d/%d retired=%v, want %d/%d retired=true",
			up, down, retired, wantUp, wantDown)
	}
}

func TestAcknowledgedStatsBillsRetiredCredentialBeforeAckWithoutRestoringAccess(t *testing.T) {
	f := newStatsServiceFixture(t)
	retireStatsCredential(t, f)

	report := f.report(1)
	f.deliver(report)
	// Persistence must precede the receipt: observe billing while the ACK is still
	// queued, then verify that a replay only returns that receipt.
	f.requireTotals(t, 1)
	requireRetiredStatsCounters(t, f, 11, 23)
	requireRetiredStatsCredentialCannotServe(t, f)
	requireStatsAck(t, f.session, report)

	f.deliver(proto.Clone(report).(*chiralv1.StatsReport))
	requireStatsAck(t, f.session, report)
	f.requireTotals(t, 1)
	requireRetiredStatsCounters(t, f, 11, 23)
	requireRetiredStatsCredentialCannotServe(t, f)
}

func TestAcknowledgedStatsRejectsCrossNodeRetiredCredentialAtomically(t *testing.T) {
	f := newStatsServiceFixture(t)
	retireStatsCredential(t, f)
	other, err := f.st.CreateNode("retired-stats-attacker", "retired-stats-attacker-token")
	if err != nil {
		t.Fatal(err)
	}
	otherSession := &Session{nodeID: other.ID, send: make(chan *chiralv1.CoreFrame, sendQueueSize), done: make(chan struct{})}
	report := &chiralv1.StatsReport{
		ReporterId: acknowledgedReporterID,
		Sequence:   1,
		Entries: []*chiralv1.StatEntry{
			{Scope: chiralv1.StatScope_STAT_SCOPE_INBOUND, Name: "must-roll-back", UplinkBytes: 5, DownlinkBytes: 7},
			{Scope: chiralv1.StatScope_STAT_SCOPE_USER, Name: f.email, UplinkBytes: 101, DownlinkBytes: 103},
		},
	}
	f.svc.handleFrame(other.ID, otherSession, &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_Stats{Stats: report}})
	requireNoStatsAck(t, otherSession)

	f.requireTotals(t, 0)
	requireRetiredStatsCounters(t, f, 0, 0)
	requireRetiredStatsCredentialCannotServe(t, f)
	var runtimeRows, receiptRows, historyRows int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM runtime_traffic_totals WHERE node_id = ?`, other.ID).Scan(&runtimeRows); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM stats_receipts WHERE node_id = ?`, other.ID).Scan(&receiptRows); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM traffic_buckets WHERE node_id = ?`, other.ID).Scan(&historyRows); err != nil {
		t.Fatal(err)
	}
	if runtimeRows != 0 || receiptRows != 0 || historyRows != 0 {
		t.Fatalf("rejected cross-node batch left partial state: runtime=%d receipts=%d history=%d",
			runtimeRows, receiptRows, historyRows)
	}
}

func TestAcknowledgedStatsReceiptFailureRollsBackRetiredCredential(t *testing.T) {
	f := newStatsServiceFixture(t)
	retireStatsCredential(t, f)
	if _, err := f.db.Exec(`CREATE TRIGGER reject_retired_stats_receipt BEFORE INSERT ON stats_receipts
		BEGIN SELECT RAISE(ABORT, 'simulated retired receipt persistence failure'); END`); err != nil {
		t.Fatal(err)
	}

	report := f.report(1)
	f.deliver(report)
	requireNoStatsAck(t, f.session)
	f.requireTotals(t, 0)
	requireRetiredStatsCounters(t, f, 0, 0)
	requireRetiredStatsCredentialCannotServe(t, f)

	if _, err := f.db.Exec(`DROP TRIGGER reject_retired_stats_receipt`); err != nil {
		t.Fatal(err)
	}
	f.deliver(report)
	f.requireTotals(t, 1)
	requireRetiredStatsCounters(t, f, 11, 23)
	requireRetiredStatsCredentialCannotServe(t, f)
	requireStatsAck(t, f.session, report)
}

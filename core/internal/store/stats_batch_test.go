package store

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const statsTestReporter = "0123456789abcdef0123456789abcdef"

var statsTestTime = time.Date(2026, 9, 29, 12, 15, 0, 0, time.UTC)

type statsBatchFixture struct {
	node Node
	user User
	cred Credential
}

func seedStatsBatch(t *testing.T, st *Store) statsBatchFixture {
	t.Helper()
	n, err := st.CreateNode("stats-entry", "stats-node-token-hash")
	if err != nil {
		t.Fatal(err)
	}
	u := seedUser(t, st, "stats-alice")
	p, err := st.CreateProfile("stats-profile")
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.PutCredential(Credential{
		UserID: u.ID, NodeID: n.ID, ProfileID: p.ID, Email: "alice@stats-entry", Secret: "test-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	return statsBatchFixture{node: n, user: u, cred: c}
}

// Include the replay receipt as well as every counter: a rejected transaction
// must not consume its sequence, even if billing happened before the failure.
func statsBatchSnapshot(t *testing.T, st *Store) string {
	t.Helper()
	queries := []string{
		`SELECT id, used_bytes FROM users ORDER BY id`,
		`SELECT email, up_bytes, down_bytes FROM credentials ORDER BY email`,
		`SELECT bucket_start, node_id, user_id, up_bytes, down_bytes FROM traffic_buckets ORDER BY bucket_start, node_id, user_id`,
		`SELECT node_id, scope, name, up_bytes, down_bytes FROM runtime_traffic_totals ORDER BY node_id, scope, name`,
		`SELECT node_id, reporter_id, sequence, digest, continuity_indeterminate, received_at FROM stats_receipts ORDER BY node_id, reporter_id`,
		`SELECT node_id, reporter_id, sequence, received_at, reason FROM stats_continuity_gaps ORDER BY node_id, reporter_id, sequence`,
	}
	var snapshot [][][]any
	for _, query := range queries {
		rows, err := st.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var table [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			table = append(table, values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		snapshot = append(snapshot, table)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func requireStatsBatch(t *testing.T, st *Store, nodeID, reporter string, sequence uint64, entries []TrafficDelta, at time.Time, wantApplied bool) {
	t.Helper()
	applied, err := st.RecordStatsBatch(nodeID, reporter, sequence, entries, false, at)
	if err != nil || applied != wantApplied {
		t.Fatalf("batch %s/%d: applied=%v err=%v, want applied=%v", reporter, sequence, applied, err, wantApplied)
	}
}

func requireStatsUsage(t *testing.T, st *Store, userID string, want int64) {
	t.Helper()
	u, err := st.GetUser(userID)
	if err != nil {
		t.Fatal(err)
	}
	if u.UsedBytes != want {
		t.Fatalf("used_bytes=%d, want %d", u.UsedBytes, want)
	}
}

func requireStatsSeries(t *testing.T, st *Store, userID, nodeID string, up, down int64) {
	t.Helper()
	points, err := st.TrafficSeries(userID, nodeID, statsTestTime.Truncate(TrafficBucket), statsTestTime.Add(2*TrafficBucket))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].At != statsTestTime.Truncate(TrafficBucket).Unix() || points[0].UpBytes != up || points[0].DownBytes != down {
		t.Fatalf("traffic series=%+v, want one original bucket with up=%d down=%d", points, up, down)
	}
}

func TestStatsBatchReplaySurvivesReopenWithoutDoubleBilling(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replay.db")
	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := seedStatsBatch(t, st)
	entries := []TrafficDelta{
		{Scope: "user", Name: f.cred.Email, Up: 100, Down: 250},
		{Scope: "inbound", Name: "profile-in", Up: 100, Down: 250},
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, true)
	before := statsBatchSnapshot(t, st)
	// Entry ordering and reconnect time cannot turn the same batch into new usage.
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, []TrafficDelta{entries[1], entries[0]}, statsTestTime.Add(TrafficBucket), false)
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatalf("replay changed persisted state: before=%s after=%s", before, after)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime.Add(TrafficBucket), false)
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatal("durable replay receipt was lost on reopen")
	}
	requireStatsUsage(t, st, f.user.ID, 350)
	requireStatsSeries(t, st, f.user.ID, f.node.ID, 100, 250)
}

func TestStatsBatchRejectsDigestReuseAndSequenceGaps(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	one := []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 3, Down: 5}}
	two := []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 7, Down: 11}}
	for _, sequence := range []uint64{2, 17} {
		if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, sequence, one, false, statsTestTime); err == nil || applied {
			t.Fatalf("initial sequence gap accepted: applied=%v err=%v", applied, err)
		}
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, one, statsTestTime, true)
	before := statsBatchSnapshot(t, st)
	for _, tc := range []struct {
		sequence uint64
		entries  []TrafficDelta
	}{
		{1, two},
		{3, one},
	} {
		if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, tc.sequence, tc.entries, false, statsTestTime); err == nil || applied {
			t.Fatalf("invalid replay accepted: sequence=%d applied=%v err=%v", tc.sequence, applied, err)
		}
		if after := statsBatchSnapshot(t, st); after != before {
			t.Fatal("rejected sequence changed counters or receipt")
		}
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, two, statsTestTime, true)
	before = statsBatchSnapshot(t, st)
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, one, statsTestTime, false)
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatal("old replay below the durable high-water mark was applied")
	}
	requireStatsUsage(t, st, f.user.ID, 26)
}

func TestStatsBatchReceiptFailureRollsBackEveryCounter(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	entries := []TrafficDelta{
		{Scope: "user", Name: f.cred.Email, Up: 3, Down: 5},
		{Scope: "inbound", Name: "profile-in", Up: 3, Down: 5},
		{Scope: "outbound", Name: "direct", Up: 3, Down: 5},
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, true)
	before := statsBatchSnapshot(t, st)
	if _, err := st.db.Exec(`CREATE TRIGGER stats_test_receipt_failure BEFORE INSERT ON stats_receipts
		BEGIN SELECT RAISE(ABORT, 'forced receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 2, entries, false, statsTestTime); err == nil || applied || !strings.Contains(err.Error(), "forced receipt failure") {
		t.Fatalf("expected final receipt failure: applied=%v err=%v", applied, err)
	}
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatalf("failed receipt left partial writes: before=%s after=%s", before, after)
	}
	if _, err := st.db.Exec(`DROP TRIGGER stats_test_receipt_failure`); err != nil {
		t.Fatal(err)
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, entries, statsTestTime, true)
	requireStatsUsage(t, st, f.user.ID, 16)
	requireStatsSeries(t, st, f.user.ID, f.node.ID, 6, 10)
}

func TestStatsBatchCrossNodeCredentialRejectsWholeBatch(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	other, err := st.CreateNode("stats-other", "stats-other-hash")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := st.PutCredential(Credential{UserID: f.user.ID, ProfileID: f.cred.ProfileID, NodeID: other.ID, Email: "zulu@other-node", Secret: "other-secret"})
	if err != nil {
		t.Fatal(err)
	}
	before := statsBatchSnapshot(t, st)
	for _, foreignUp := range []int64{0, 1} {
		entries := []TrafficDelta{
			{Scope: "inbound", Name: "profile-in", Up: 9, Down: 10},
			{Scope: "user", Name: f.cred.Email, Up: 9, Down: 10},
			{Scope: "user", Name: foreign.Email, Up: foreignUp},
		}
		if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, entries, false, statsTestTime); err == nil || applied {
			t.Fatalf("cross-node attribution accepted: applied=%v err=%v", applied, err)
		}
		if after := statsBatchSnapshot(t, st); after != before {
			t.Fatal("valid entries preceding a foreign credential were partially charged")
		}
	}
}

func TestStatsBatchUnknownMachineAndRawScopesDoNotChargeHumans(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	entries := []TrafficDelta{
		{Scope: "user", Name: f.cred.Email, Up: 5, Down: 6},
		{Scope: "user", Name: "relay-machine@exit", Up: 30, Down: 40},
		{Scope: "user", Name: "revoked@entry", Up: 2, Down: 3},
		{Scope: "inbound", Name: "profile-in", Up: 37, Down: 49},
		{Scope: "outbound", Name: "direct", Up: 37, Down: 49},
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, true)
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, false)
	requireStatsUsage(t, st, f.user.ID, 11)
	requireStatsSeries(t, st, f.user.ID, f.node.ID, 5, 6)
	requireStatsSeries(t, st, "", f.node.ID, 37, 49)
	for _, entry := range entries[3:] {
		var up, down int64
		if err := st.db.QueryRow(`SELECT up_bytes, down_bytes FROM runtime_traffic_totals WHERE node_id = ? AND scope = ? AND name = ?`, f.node.ID, entry.Scope, entry.Name).Scan(&up, &down); err != nil {
			t.Fatal(err)
		}
		if up != entry.Up || down != entry.Down {
			t.Fatalf("raw %s totals=%d/%d, want %d/%d", entry.Scope, up, down, entry.Up, entry.Down)
		}
	}
}

func TestStatsBatchRatesBillEachEntryWithoutChangingRawCounters(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	if err := st.SetNodeTrafficRate(f.node.ID, 2.5); err != nil {
		t.Fatal(err)
	}
	exit, err := st.CreateNode("stats-exit", "stats-exit-hash")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := st.CreateNodeRelay(NodeRelay{EntryNodeID: f.node.ID, ExitNodeID: exit.ID, ProfileID: f.cred.ProfileID, Label: "test-relay", Secret: "machine-secret", TrafficRate: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	relayCred, err := st.PutCredential(Credential{UserID: f.user.ID, ProfileID: f.cred.ProfileID, NodeID: f.node.ID, ExitRelayID: relay.ID, Email: "alice@relay", Secret: "relay-user-secret"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := st.CreateExternalSub("stats-external", "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceExternalProxies(sub.ID, []ExternalProxy{{Name: "exit", Type: "socks", Server: "example.invalid", Port: 1080, Config: "{}"}}); err != nil {
		t.Fatal(err)
	}
	proxies, err := st.ExternalProxies(sub.ID)
	if err != nil || len(proxies) != 1 {
		t.Fatalf("external fixture: proxies=%v err=%v", proxies, err)
	}
	if err := st.SetExternalProxyTrafficRate(proxies[0].ID, 0); err != nil {
		t.Fatal(err)
	}
	externalCred, err := st.PutCredential(Credential{UserID: f.user.ID, ProfileID: f.cred.ProfileID, NodeID: f.node.ID, ExitProxyID: proxies[0].ID, Email: "alice@external", Secret: "external-user-secret"})
	if err != nil {
		t.Fatal(err)
	}
	entries := []TrafficDelta{
		{Scope: "user", Name: f.cred.Email, Up: 1, Down: 2},
		{Scope: "user", Name: relayCred.Email, Up: 1, Down: 2},
		{Scope: "user", Name: externalCred.Email, Up: 1, Down: 2},
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, true)
	// ceil(3*2.5) + ceil(3*1.5) + ceil(3*0): the selected exit owns its rate.
	requireStatsUsage(t, st, f.user.ID, 13)
	requireStatsSeries(t, st, f.user.ID, f.node.ID, 3, 6)
	creds, err := st.UserCredentials(f.user.ID)
	if err != nil || len(creds) != 3 {
		t.Fatalf("credentials=%v err=%v", creds, err)
	}
	for _, c := range creds {
		if c.UpBytes != 1 || c.DownBytes != 2 {
			t.Fatalf("billing rate changed raw credential counters: %+v", c)
		}
	}
}

func TestStatsBatchOverflowRejectsWithoutAnyPartialWrites(t *testing.T) {
	for _, kind := range []string{"entry-sum", "credential-up", "credential-down", "usage", "chart", "runtime", "rate-product", "rate-infinity", "negative-rate"} {
		t.Run(kind, func(t *testing.T) {
			st := testStore(t, storeTestKey)
			f := seedStatsBatch(t, st)
			entry := TrafficDelta{Scope: "user", Name: f.cred.Email, Up: 2, Down: 1}
			var err error
			switch kind {
			case "entry-sum":
				entry.Up, entry.Down = math.MaxInt64, 1
			case "credential-up":
				_, err = st.db.Exec(`UPDATE credentials SET up_bytes = ? WHERE id = ?`, math.MaxInt64, f.cred.ID)
			case "credential-down":
				_, err = st.db.Exec(`UPDATE credentials SET down_bytes = ? WHERE id = ?`, math.MaxInt64, f.cred.ID)
			case "usage":
				_, err = st.db.Exec(`UPDATE users SET used_bytes = ? WHERE id = ?`, math.MaxInt64-2, f.user.ID)
			case "chart":
				_, err = st.db.Exec(`INSERT INTO traffic_buckets (bucket_start, node_id, user_id, up_bytes, down_bytes) VALUES (?, ?, ?, ?, 0)`, statsTestTime.Truncate(TrafficBucket).Unix(), f.node.ID, f.user.ID, math.MaxInt64)
			case "runtime":
				entry.Scope, entry.Name = "outbound", "direct"
				_, err = st.db.Exec(`INSERT INTO runtime_traffic_totals (node_id, scope, name, up_bytes, down_bytes) VALUES (?, 'outbound', 'direct', ?, 0)`, f.node.ID, math.MaxInt64)
			case "rate-product":
				err = st.SetNodeTrafficRate(f.node.ID, float64(math.MaxInt64)/2)
			case "rate-infinity":
				err = st.SetNodeTrafficRate(f.node.ID, math.MaxFloat64)
			case "negative-rate":
				err = st.SetNodeTrafficRate(f.node.ID, -1)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := statsBatchSnapshot(t, st)
			entries := []TrafficDelta{{Scope: "inbound", Name: "first-write", Up: 1}, entry}
			if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, entries, false, statsTestTime); err == nil || applied {
				t.Fatalf("overflow/invalid rate accepted: applied=%v err=%v", applied, err)
			}
			if after := statsBatchSnapshot(t, st); after != before {
				t.Fatal("overflow rejection left a receipt or partial counters")
			}
		})
	}
}

func TestStatsBatchEmptySequenceIsDurableAndReplayable(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, nil, statsTestTime, true)
	before := statsBatchSnapshot(t, st)
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, []TrafficDelta{}, statsTestTime.Add(TrafficBucket), false)
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatal("replaying an empty batch changed state")
	}
	var sequence int64
	if err := st.db.QueryRow(`SELECT sequence FROM stats_receipts WHERE node_id = ? AND reporter_id = ?`, f.node.ID, statsTestReporter).Scan(&sequence); err != nil || sequence != 1 {
		t.Fatalf("empty receipt: sequence=%d err=%v", sequence, err)
	}
	for _, table := range []string{"traffic_buckets", "runtime_traffic_totals"} {
		var count int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("empty batch wrote %s: count=%d err=%v", table, count, err)
		}
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 1}}, statsTestTime, true)
	requireStatsUsage(t, st, f.user.ID, 1)
}

func TestStatsBatchContinuityGapSurvivesReopenAndReceiptAdvancement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "continuity.db")
	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := seedStatsBatch(t, st)
	applyGap := func(sequence uint64, at time.Time, wantApplied bool) {
		t.Helper()
		applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, sequence, nil, true, at)
		if err != nil || applied != wantApplied {
			t.Fatalf("gap %d: applied=%v err=%v want applied=%v", sequence, applied, err, wantApplied)
		}
	}
	applyGap(1, statsTestTime, true)
	var marked bool
	if err := st.db.QueryRow(`SELECT continuity_indeterminate FROM stats_receipts WHERE node_id = ? AND reporter_id = ?`, f.node.ID, statsTestReporter).Scan(&marked); err != nil || !marked {
		t.Fatalf("receipt lost continuity marker: marked=%v err=%v", marked, err)
	}
	before := statsBatchSnapshot(t, st)
	applyGap(1, statsTestTime.Add(time.Hour), false)
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatal("gap replay rewrote its evidence or timestamp")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyGap(1, statsTestTime.Add(2*time.Hour), false)
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatal("gap evidence or its replay receipt was lost on reopen")
	}
	entries := []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 2, Down: 3}}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, entries, statsTestTime, true)
	if err := st.db.QueryRow(`SELECT continuity_indeterminate FROM stats_receipts WHERE node_id = ? AND reporter_id = ?`, f.node.ID, statsTestReporter).Scan(&marked); err != nil || marked {
		t.Fatalf("ordinary receipt did not clear only its current marker: marked=%v err=%v", marked, err)
	}
	applyGap(3, statsTestTime.Add(time.Minute), true)
	before = statsBatchSnapshot(t, st)
	applyGap(1, statsTestTime.Add(3*time.Hour), false)
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, entries, statsTestTime, false)
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatal("old replay changed retained gaps or accounting")
	}
	rows, err := st.db.Query(`SELECT sequence, received_at, reason FROM stats_continuity_gaps WHERE node_id = ? AND reporter_id = ? ORDER BY sequence`, f.node.ID, statsTestReporter)
	if err != nil {
		t.Fatal(err)
	}
	for i, sequence := range []uint64{1, 3} {
		var got uint64
		var receivedAt int64
		var reason string
		if !rows.Next() {
			rows.Close()
			t.Fatal("an older gap disappeared when the receipt advanced")
		}
		if err := rows.Scan(&got, &receivedAt, &reason); err != nil || got != sequence || receivedAt != statsTestTime.Add(time.Duration(i)*time.Minute).Unix() || reason != statsContinuityGapReason {
			rows.Close()
			t.Fatalf("gap evidence: sequence=%d at=%d reason=%q err=%v", got, receivedAt, reason, err)
		}
	}
	if rows.Next() || rows.Err() != nil {
		rows.Close()
		t.Fatal("gap replay duplicated evidence or listing failed")
	}
	rows.Close()
	requireStatsUsage(t, st, f.user.ID, 5)
}

func TestStatsBatchContinuityMarkerIsPartOfReplayIdentity(t *testing.T) {
	for _, initialGap := range []bool{false, true} {
		t.Run(fmt.Sprint(initialGap), func(t *testing.T) {
			st := testStore(t, storeTestKey)
			f := seedStatsBatch(t, st)
			if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, nil, initialGap, statsTestTime); err != nil || !applied {
				t.Fatalf("first empty batch: applied=%v err=%v", applied, err)
			}
			for _, advanced := range []bool{false, true} {
				if advanced {
					requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, nil, statsTestTime, true)
				}
				before := statsBatchSnapshot(t, st)
				if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, nil, !initialGap, statsTestTime); err == nil || applied {
					t.Fatalf("changed continuity marker was acknowledged: advanced=%v applied=%v err=%v", advanced, applied, err)
				}
				if after := statsBatchSnapshot(t, st); after != before {
					t.Fatal("changed replay created or removed a continuity marker")
				}
			}
		})
	}
}

func TestStatsBatchContinuityGapAndReceiptCommitAtomically(t *testing.T) {
	for _, rejectedTable := range []string{"stats_continuity_gaps", "stats_receipts"} {
		t.Run(rejectedTable, func(t *testing.T) {
			st := testStore(t, storeTestKey)
			f := seedStatsBatch(t, st)
			before := statsBatchSnapshot(t, st)
			if _, err := st.db.Exec(`CREATE TRIGGER reject_continuity_write BEFORE INSERT ON ` + rejectedTable + ` BEGIN SELECT RAISE(ABORT, 'forced continuity failure'); END`); err != nil {
				t.Fatal(err)
			}
			if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, nil, true, statsTestTime); err == nil || applied || !strings.Contains(err.Error(), "forced continuity failure") {
				t.Fatalf("failure was acknowledged: applied=%v err=%v", applied, err)
			}
			if after := statsBatchSnapshot(t, st); after != before {
				t.Fatal("failed transaction left a gap or consumed the sequence")
			}
			if _, err := st.db.Exec(`DROP TRIGGER reject_continuity_write`); err != nil {
				t.Fatal(err)
			}
			if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, nil, true, statsTestTime); err != nil || !applied {
				t.Fatalf("failed sequence could not retry: applied=%v err=%v", applied, err)
			}
		})
	}
}

func TestStatsBatchContinuityGapRejectsTrafficEntries(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	before := statsBatchSnapshot(t, st)
	entries := []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 5}}
	if applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, entries, true, statsTestTime); err == nil || applied {
		t.Fatalf("gap with usage accepted: applied=%v err=%v", applied, err)
	}
	if after := statsBatchSnapshot(t, st); after != before {
		t.Fatal("invalid gap billed traffic or consumed its sequence")
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, entries, statsTestTime, true)
	requireStatsUsage(t, st, f.user.ID, 5)
}

func TestStatsBatchRejectsInvalidEnvelopeAndEntries(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	valid := TrafficDelta{Scope: "user", Name: f.cred.Email, Up: 1}
	before := statsBatchSnapshot(t, st)
	for _, tc := range []struct {
		name     string
		reporter string
		sequence uint64
		entries  []TrafficDelta
	}{
		{"missing-reporter", "", 1, []TrafficDelta{valid}},
		{"upper-case-reporter", strings.ToUpper(statsTestReporter), 1, []TrafficDelta{valid}},
		{"short-reporter", "abcdef", 1, []TrafficDelta{valid}},
		{"nonhex-reporter", strings.Repeat("z", 32), 1, []TrafficDelta{valid}},
		{"zero-sequence", statsTestReporter, 0, []TrafficDelta{valid}},
		{"sequence-overflow", statsTestReporter, uint64(math.MaxInt64) + 1, []TrafficDelta{valid}},
		{"unknown-scope", statsTestReporter, 1, []TrafficDelta{{Scope: "fleet", Name: "x", Up: 1}}},
		{"empty-name", statsTestReporter, 1, []TrafficDelta{{Scope: "user", Up: 1}}},
		{"long-name", statsTestReporter, 1, []TrafficDelta{{Scope: "user", Name: strings.Repeat("x", 513), Up: 1}}},
		{"negative-up", statsTestReporter, 1, []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: -1}}},
		{"negative-down", statsTestReporter, 1, []TrafficDelta{{Scope: "user", Name: f.cred.Email, Down: -1}}},
		{"duplicate", statsTestReporter, 1, []TrafficDelta{valid, valid}},
		{"too-many-entries", statsTestReporter, 1, make([]TrafficDelta, 10001)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if applied, err := st.RecordStatsBatch(f.node.ID, tc.reporter, tc.sequence, tc.entries, false, statsTestTime); err == nil || applied {
				t.Fatalf("invalid batch accepted: applied=%v err=%v", applied, err)
			}
			if after := statsBatchSnapshot(t, st); after != before {
				t.Fatal("invalid batch changed billing, chart data or receipts")
			}
		})
	}
}

func TestStatsBatchConcurrentDuplicatesAndReportersSerialize(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	entries := []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 1, Down: 2}}
	const workers = 8
	type result struct {
		applied bool
		err     error
	}
	for _, distinct := range []bool{false, true} {
		start := make(chan struct{})
		results := make(chan result, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				reporter := statsTestReporter
				if distinct {
					reporter = fmt.Sprintf("%032x", i+1)
				}
				applied, err := st.RecordStatsBatch(f.node.ID, reporter, 1, entries, false, statsTestTime)
				results <- result{applied, err}
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		appliedCount := 0
		for result := range results {
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.applied {
				appliedCount++
			}
		}
		want := 1
		if distinct {
			want = workers
		}
		if appliedCount != want {
			t.Fatalf("distinct=%v: applied=%d, want %d", distinct, appliedCount, want)
		}
	}
	requireStatsUsage(t, st, f.user.ID, 3*(workers+1))
	requireStatsSeries(t, st, f.user.ID, f.node.ID, workers+1, 2*(workers+1))
}

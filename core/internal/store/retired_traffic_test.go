package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func requireRetiredCounters(t *testing.T, st *Store, email string, wantUp, wantDown int64) {
	t.Helper()
	var up, down int64
	if err := st.db.QueryRow(`SELECT up_bytes, down_bytes FROM credential_traffic_owners
		WHERE email = ? AND retired_at IS NOT NULL`, email).Scan(&up, &down); err != nil {
		t.Fatal(err)
	}
	if up != wantUp || down != wantDown {
		t.Fatalf("retired raw counters = %d/%d, want %d/%d", up, down, wantUp, wantDown)
	}
}

func requireNoServingCredential(t *testing.T, st *Store, f statsBatchFixture) {
	t.Helper()
	if _, _, err := st.CredentialOwner(f.cred.Email); !IsNotFound(err) {
		t.Fatalf("retired accounting identity is still an online owner: %v", err)
	}
	if rows, err := st.NodeCredentials(f.node.ID); err != nil || len(rows) != 0 {
		t.Fatalf("retired accounting identity became a serving credential: count=%d err=%v", len(rows), err)
	}
}

func TestRetiredTrafficBillsLateBatchesOnceAcrossReopenWithoutRestoringAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retired.db")
	st, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := seedStatsBatch(t, st)
	if err := st.SetNodeTrafficRate(f.node.ID, 2.5); err != nil {
		t.Fatal(err)
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1,
		[]TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 10, Down: 20}}, statsTestTime, true)
	if err := st.DeleteCredentialsForBinding(f.user.ID, f.cred.ProfileID); err != nil {
		t.Fatal(err)
	}
	requireNoServingCredential(t, st, f)
	if err := st.SetNodeTrafficRate(f.node.ID, 100); err != nil {
		t.Fatal(err)
	}
	late := []TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 4, Down: 2}}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, late, statsTestTime, true)
	requireStatsUsage(t, st, f.user.ID, 90) // 30*2.5 + 6*2.5, not the later rate.
	requireRetiredCounters(t, st, f.cred.Email, 14, 22)
	requireStatsSeries(t, st, f.user.ID, f.node.ID, 14, 22)
	before := statsBatchSnapshot(t, st)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 2, late, statsTestTime.Add(time.Hour), false)
	if statsBatchSnapshot(t, st) != before {
		t.Fatal("late-batch replay changed billing, ownership or its receipt")
	}
	// The legacy direct-Xray path has not opted into replayable late billing.
	if err := st.AddCredentialTraffic(f.cred.Email, 100, 200); err != nil {
		t.Fatal(err)
	}
	if statsBatchSnapshot(t, st) != before {
		t.Fatal("legacy traffic unexpectedly used a retired accounting identity")
	}
	requireNoServingCredential(t, st, f)
}

func TestRetiredTrafficCannotBeClaimedByAnotherNode(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	if err := st.DeleteCredentialsForBinding(f.user.ID, f.cred.ProfileID); err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateNode("other-stats-node", "other-stats-token")
	if err != nil {
		t.Fatal(err)
	}
	before := statsBatchSnapshot(t, st)
	applied, err := st.RecordStatsBatch(other.ID, statsTestReporter, 1, []TrafficDelta{
		{Scope: "inbound", Name: "first-write", Up: 5},
		{Scope: "user", Name: f.cred.Email, Down: 10},
	}, false, statsTestTime)
	if err == nil || applied || statsBatchSnapshot(t, st) != before {
		t.Fatal("cross-node retired traffic was accepted or left a partial write")
	}
}

func TestRetiredTrafficDoesNotRecreateOrChargeADeletedUser(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	if err := st.DeleteUser(f.user.ID); err != nil {
		t.Fatal(err)
	}
	replacement := seedUser(t, st, f.user.Name)
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1,
		[]TrafficDelta{{Scope: "user", Name: f.cred.Email, Up: 7, Down: 11}}, statsTestTime, true)
	requireStatsUsage(t, st, replacement.ID, 0)
	if _, err := st.GetUser(f.user.ID); !IsNotFound(err) {
		t.Fatalf("late billing restored the deleted user: %v", err)
	}
	requireRetiredCounters(t, st, f.cred.Email, 7, 11)
	requireStatsSeries(t, st, "", f.node.ID, 7, 11)
	if points, err := st.TrafficSeries(replacement.ID, f.node.ID, statsTestTime.Add(-time.Hour), statsTestTime.Add(time.Hour)); err != nil || len(points) != 0 {
		t.Fatalf("late traffic was attributed to a same-name replacement: %v", err)
	}
	requireNoServingCredential(t, st, f)
}

func TestRetiredTrafficRejectsOverflowAtomically(t *testing.T) {
	for _, kind := range []string{"up", "down", "usage", "rate"} {
		t.Run(kind, func(t *testing.T) {
			st := testStore(t, storeTestKey)
			f := seedStatsBatch(t, st)
			if kind == "rate" {
				if err := st.SetNodeTrafficRate(f.node.ID, -1); err != nil {
					t.Fatal(err)
				}
			}
			if err := st.DeleteCredentialsForBinding(f.user.ID, f.cred.ProfileID); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "up":
				_, err = st.db.Exec(`UPDATE credential_traffic_owners SET up_bytes = ? WHERE email = ?`, math.MaxInt64, f.cred.Email)
			case "down":
				_, err = st.db.Exec(`UPDATE credential_traffic_owners SET down_bytes = ? WHERE email = ?`, math.MaxInt64, f.cred.Email)
			case "usage":
				_, err = st.db.Exec(`UPDATE users SET used_bytes = ? WHERE id = ?`, math.MaxInt64, f.user.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := statsBatchSnapshot(t, st)
			applied, err := st.RecordStatsBatch(f.node.ID, statsTestReporter, 1, []TrafficDelta{
				{Scope: "inbound", Name: "first-write", Up: 1},
				{Scope: "user", Name: f.cred.Email, Up: 1, Down: 1},
			}, false, statsTestTime)
			if err == nil || applied || statsBatchSnapshot(t, st) != before {
				t.Fatal("rejected retired traffic changed billing, counters or receipt")
			}
		})
	}
}

func TestRebindingUsesANewAccountingKeyAndKeepsLateTrafficSeparate(t *testing.T) {
	st := testStore(t, storeTestKey)
	f := seedStatsBatch(t, st)
	if err := st.SetNodeTrafficRate(f.node.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteCredentialsForBinding(f.user.ID, f.cred.ProfileID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNodeTrafficRate(f.node.ID, 5); err != nil {
		t.Fatal(err)
	}
	newCredential, err := st.PutCredential(Credential{UserID: f.user.ID, ProfileID: f.cred.ProfileID,
		NodeID: f.node.ID, Email: f.cred.Email, Secret: "replacement-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if newCredential.Email == f.cred.Email {
		t.Fatal("recreated credential reused a retired statistics key")
	}
	requireStatsBatch(t, st, f.node.ID, statsTestReporter, 1, []TrafficDelta{
		{Scope: "user", Name: f.cred.Email, Up: 3},
		{Scope: "user", Name: newCredential.Email, Down: 4},
	}, statsTestTime, true)
	requireStatsUsage(t, st, f.user.ID, 26)
	requireRetiredCounters(t, st, f.cred.Email, 3, 0)
	current, err := st.FindCredential(f.user.ID, f.cred.ProfileID, f.node.ID, "")
	if err != nil || current.UpBytes != 0 || current.DownBytes != 4 {
		t.Fatalf("old generation traffic polluted the new credential: %v", err)
	}
}

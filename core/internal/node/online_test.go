package node

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/online"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// A node may only speak for the credentials issued to it.
//
// Every agent runs on a rented box that Chiral does not control, so a report is
// an assertion by an untrusted party. Without the check, one compromised VPS
// could attribute any address to any subscriber — and those addresses land in
// user_devices, which is precisely what an operator later reads as evidence of
// account sharing. Fabricated evidence against an innocent user is worse than
// not having the feature.

func onlineFixture(t *testing.T) (*Service, *online.Registry, *store.Store) {
	t.Helper()
	box, _ := secret.NewBox("node-online-test-key-0123456789ab")
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := NewManager(30*time.Second, logger)
	svc := NewService(st, mgr, st, logger)
	reg := online.New(time.Minute)
	svc.EnableOnlineTracking(reg, st, 30*time.Second)
	return svc, reg, st
}

// seedCredential creates a user, profile and node, and issues the credential
// binding them, returning the stats email the agent would report.
func seedCredential(t *testing.T, st *store.Store, userName, nodeName string) (userID, nodeID, email string) {
	t.Helper()
	u, err := st.CreateUser(store.User{Name: userName, Enabled: true}, "hash-"+userName)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreateProfile("p-" + nodeName)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateNode(nodeName, "join-hash-"+nodeName)
	if err != nil {
		t.Fatal(err)
	}
	email = "e-" + userName + "@" + p.ID + "." + n.ID
	if _, err := st.PutCredential(store.Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: email, Secret: "s",
	}); err != nil {
		t.Fatal(err)
	}
	return u.ID, n.ID, email
}

func TestOnlineReportIsRecorded(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	userID, nodeID, email := seedCredential(t, st, "mai", "tokyo")

	svc.recordOnline(nodeID, &chiralv1.OnlineReport{
		AtUnix:   time.Now().Unix(),
		Complete: true,
		Users: []*chiralv1.OnlineUser{{
			Email: email,
			Ips:   []*chiralv1.OnlineIP{{Ip: "203.0.113.7", LastSeenUnix: time.Now().Unix()}},
		}},
	})

	if got := reg.Status(userID, time.Now()).Count; got != 1 {
		t.Fatalf("count = %d, want 1", got)
	}
	devices, err := st.UserDevices(userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].IP != "203.0.113.7" {
		t.Fatalf("devices = %v, want one row for 203.0.113.7", devices)
	}
}

// The core of the trust model: tokyo reports an address for a credential
// belonging to frankfurt. It must be dropped from both the live view and the
// durable record.
func TestANodeCannotReportForAnotherNodesCredential(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	victimID, _, victimEmail := seedCredential(t, st, "victim", "frankfurt")
	_, attackerNodeID, _ := seedCredential(t, st, "attacker", "tokyo")

	svc.recordOnline(attackerNodeID, &chiralv1.OnlineReport{
		AtUnix:   time.Now().Unix(),
		Complete: true,
		Users: []*chiralv1.OnlineUser{{
			Email: victimEmail,
			Ips: []*chiralv1.OnlineIP{
				{Ip: "203.0.113.1", LastSeenUnix: time.Now().Unix()},
				{Ip: "203.0.113.2", LastSeenUnix: time.Now().Unix()},
				{Ip: "203.0.113.3", LastSeenUnix: time.Now().Unix()},
			},
		}},
	})

	if got := reg.Status(victimID, time.Now()).Count; got != 0 {
		t.Errorf("count = %d; a node forged addresses onto another node's subscriber", got)
	}
	devices, err := st.UserDevices(victimID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Errorf("forged addresses were persisted as evidence: %v", devices)
	}
}

// An unknown credential is ordinary right after a revocation, so it is dropped
// quietly rather than treated as an attack.
func TestUnknownCredentialsAreIgnored(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	userID, nodeID, _ := seedCredential(t, st, "mai", "tokyo")

	svc.recordOnline(nodeID, &chiralv1.OnlineReport{
		AtUnix:   time.Now().Unix(),
		Complete: true,
		Users: []*chiralv1.OnlineUser{{
			Email: "ghost@nowhere.invalid",
			Ips:   []*chiralv1.OnlineIP{{Ip: "203.0.113.9", LastSeenUnix: time.Now().Unix()}},
		}},
	})

	if got := reg.Status(userID, time.Now()).Count; got != 0 {
		t.Errorf("count = %d, want 0", got)
	}
}

// An agent's clock is not ours. A timestamp from the future would push the
// sighting past its TTL check and keep a user "online" indefinitely.
func TestImplausibleTimestampsFallBackToOurClock(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	userID, nodeID, email := seedCredential(t, st, "mai", "tokyo")

	svc.recordOnline(nodeID, &chiralv1.OnlineReport{
		AtUnix:   time.Now().Unix(),
		Complete: true,
		Users: []*chiralv1.OnlineUser{{
			Email: email,
			Ips: []*chiralv1.OnlineIP{
				{Ip: "203.0.113.1", LastSeenUnix: time.Now().Add(48 * time.Hour).Unix()},
				{Ip: "203.0.113.2", LastSeenUnix: 0},
			},
		}},
	})

	// Both are still real observations, so both count — just on our clock.
	if got := reg.Status(userID, time.Now()).Count; got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	devices, _ := st.UserDevices(userID)
	now := time.Now().Unix()
	for _, d := range devices {
		if d.LastSeen > now+5 {
			t.Errorf("%s recorded at %d, in the future relative to %d", d.IP, d.LastSeen, now)
		}
	}
}

// An empty report is a statement ("nobody is connected here"), and the
// registry must act on it — otherwise a disconnected device never clears.
func TestEmptyReportClearsTheNodesAddresses(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	userID, nodeID, email := seedCredential(t, st, "mai", "tokyo")

	svc.recordOnline(nodeID, &chiralv1.OnlineReport{
		AtUnix: time.Now().Unix(), Complete: true,
		Users: []*chiralv1.OnlineUser{{
			Email: email,
			Ips:   []*chiralv1.OnlineIP{{Ip: "203.0.113.7", LastSeenUnix: time.Now().Unix()}},
		}},
	})
	svc.recordOnline(nodeID, &chiralv1.OnlineReport{AtUnix: time.Now().Unix(), Complete: true})

	if got := reg.Status(userID, time.Now()).Count; got != 0 {
		t.Fatalf("count = %d, want 0 after an empty report", got)
	}
	// The durable record keeps it: they were there, and that is the point.
	devices, _ := st.UserDevices(userID)
	if len(devices) != 1 {
		t.Fatalf("the history lost the address: %v", devices)
	}
}

// With recording off, nothing is stored even if an agent sends a report
// anyway — a stale policy, or an agent that ignores it.
func TestReportsAreIgnoredWhenRecordingIsOff(t *testing.T) {
	box, _ := secret.NewBox("node-online-test-key-0123456789ab")
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	svc := NewService(st, NewManager(30*time.Second, logger), st, logger)

	userID, nodeID, email := seedCredential(t, st, "mai", "tokyo")
	svc.recordOnline(nodeID, &chiralv1.OnlineReport{
		AtUnix: time.Now().Unix(), Complete: true,
		Users: []*chiralv1.OnlineUser{{
			Email: email,
			Ips:   []*chiralv1.OnlineIP{{Ip: "203.0.113.7", LastSeenUnix: time.Now().Unix()}},
		}},
	})

	devices, _ := st.UserDevices(userID)
	if len(devices) != 0 {
		t.Fatalf("addresses were recorded with the feature switched off: %v", devices)
	}
}

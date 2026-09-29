package user

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

type userSnapshotFixture struct {
	st      *store.Store
	box     *secret.Box
	path    string
	profile store.Profile
	node    store.Node
	logger  *slog.Logger
}

func newUserSnapshotFixture(t *testing.T) *userSnapshotFixture {
	t.Helper()
	box, err := secret.NewBox("snapshot-user-test-key-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture.db")
	st, err := store.Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p, err := st.CreateProfile("snapshot-profile")
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.CreateNode("snapshot-node", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	return &userSnapshotFixture{st: st, box: box, path: path, profile: p, node: n,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func (f *userSnapshotFixture) addUser(t *testing.T, name string, expires int64, enabled, entitled, credential bool) store.User {
	t.Helper()
	u, err := f.st.CreateUser(store.User{Name: name, Enabled: enabled, ExpiresAt: expires}, "hash-"+name)
	if err != nil {
		t.Fatal(err)
	}
	if entitled {
		if err := f.st.BindUserProfile(u.ID, f.profile.ID); err != nil {
			t.Fatal(err)
		}
	}
	if credential {
		f.putCredential(t, u, "", "", "original-"+name)
	}
	return u
}

func (f *userSnapshotFixture) putCredential(t *testing.T, u store.User, exitID, relayID, value string) store.Credential {
	t.Helper()
	c, err := f.st.PutCredential(store.Credential{
		UserID: u.ID, ProfileID: f.profile.ID, NodeID: f.node.ID,
		ExitProxyID: exitID, ExitRelayID: relayID, Secret: value,
		Email: StatsEmailForExit(u.Name, u.ID, f.profile.ID, f.node.ID, exitKey(exitID, relayID)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *userSnapshotFixture) openSnapshot(t *testing.T) *store.Store {
	t.Helper()
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.OpenSnapshot(f.path, f.box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestSnapshotServiceRequiresAReadOnlyStoreAndFixedTime(t *testing.T) {
	f := newUserSnapshotFixture(t)
	if _, err := NewSnapshotService(f.st, f.logger, time.Unix(1000, 0)); err == nil {
		t.Fatal("snapshot service accepted a mutable store")
	}
	if _, err := NewSnapshotService(nil, f.logger, time.Unix(1000, 0)); err == nil {
		t.Fatal("snapshot service accepted a nil store")
	}
	st := f.openSnapshot(t)
	for _, at := range []time.Time{{}, time.Unix(-1, 0)} {
		if _, err := NewSnapshotService(st, f.logger, at); err == nil {
			t.Fatal("snapshot service accepted an invalid fixed time")
		}
	}
}

func TestSnapshotCredentialsUseFixedExpiryAndKeepSourceUnchanged(t *testing.T) {
	f := newUserSnapshotFixture(t)
	f.addUser(t, "expires-at-boundary", 2000, true, true, true)
	f.addUser(t, "disabled", 0, false, true, true)
	f.addUser(t, "not-entitled", 0, true, false, true)
	st := f.openSnapshot(t)
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at   int64
		want int
	}{{1999, 1}, {2000, 0}, {2001, 0}, {1999, 1}} {
		svc, err := NewSnapshotService(st, f.logger, time.Unix(tc.at, 0))
		if err != nil {
			t.Fatal(err)
		}
		creds, err := svc.EnsureCredentials(f.profile.ID, f.node.ID)
		if err != nil || len(creds) != tc.want {
			t.Fatalf("at %d: got %d credentials, err=%v", tc.at, len(creds), err)
		}
		if tc.want > 0 && creds[0].Secret != "original-expires-at-boundary" {
			t.Fatal("snapshot rendering did not retain the existing credential")
		}
	}
	after, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot credential rendering changed the database")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(f.path + suffix); !os.IsNotExist(err) {
			t.Fatal("snapshot rendering created a journal file")
		}
	}
}

func TestSnapshotMissingCredentialIsAnErrorAndNeverMinted(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			f := newUserSnapshotFixture(t)
			u := f.addUser(t, "missing-private-name", 0, enabled, true, false)
			st := f.openSnapshot(t)
			svc, err := NewSnapshotService(st, f.logger, time.Unix(1000, 0))
			if err != nil {
				t.Fatal(err)
			}
			if creds, err := svc.EnsureCredentials(f.profile.ID, f.node.ID); err == nil || creds != nil {
				t.Fatal("missing snapshot credential was silently omitted or minted")
			} else if err.Error() != "snapshot credential is missing or cannot be read" {
				t.Fatalf("unsafe or unexpected credential error: %q", err)
			}
			if creds, err := st.UserCredentials(u.ID); err != nil || len(creds) != 0 {
				t.Fatalf("credential appeared during snapshot rendering: %d, %v", len(creds), err)
			}
		})
	}
}

func TestSnapshotCredentialsPreserveProfileAndAllowedSetBoundaries(t *testing.T) {
	f := newUserSnapshotFixture(t)
	allowed := f.addUser(t, "allowed", 0, true, true, true)
	f.addUser(t, "not-in-allowed-set", 0, true, true, false)
	f.addUser(t, "not-entitled", 0, true, false, true)
	st := f.openSnapshot(t)
	svc, err := NewSnapshotService(st, f.logger, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	creds, err := svc.EnsureCredentialsForExit(f.profile.ID, f.node.ID, "", map[string]bool{allowed.ID: true})
	if err != nil || len(creds) != 1 || creds[0].UserID != allowed.ID {
		t.Fatalf("allowed set or profile entitlement changed: %d, %v", len(creds), err)
	}
	if creds, err := svc.EnsureCredentialsForExit(f.profile.ID, f.node.ID, "", map[string]bool{}); err != nil || len(creds) != 0 {
		t.Fatalf("an empty allowed set granted credentials: %d, %v", len(creds), err)
	}
}

func TestSnapshotReadsDistinctDirectExternalAndRelayCredentials(t *testing.T) {
	f := newUserSnapshotFixture(t)
	u := f.addUser(t, "three-dimensions", 0, true, true, true)
	sub, err := f.st.CreateExternalSub("test-external", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReplaceExternalProxies(sub.ID, []store.ExternalProxy{{Name: "external", Type: "vless", Server: "192.0.2.1", Port: 443, Config: "name: external"}}); err != nil {
		t.Fatal(err)
	}
	proxies, err := f.st.ExternalProxies(sub.ID)
	if err != nil || len(proxies) != 1 {
		t.Fatalf("external fixture failed: %d, %v", len(proxies), err)
	}
	exitNode, err := f.st.CreateNode("exit-node", "exit-hash")
	if err != nil {
		t.Fatal(err)
	}
	relay, err := f.st.CreateNodeRelay(store.NodeRelay{EntryNodeID: f.node.ID, ExitNodeID: exitNode.ID,
		ProfileID: f.profile.ID, Label: "test-relay", Secret: "machine-only-secret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f.putCredential(t, u, proxies[0].ID, "", "original-external-secret")
	f.putCredential(t, u, "", relay.ID, "original-relay-secret")
	st := f.openSnapshot(t)
	svc, err := NewSnapshotService(st, f.logger, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		call func() ([]store.Credential, error)
		want string
	}{
		{"direct", func() ([]store.Credential, error) { return svc.EnsureCredentials(f.profile.ID, f.node.ID) }, "original-three-dimensions"},
		{"external", func() ([]store.Credential, error) {
			return svc.EnsureCredentialsForExit(f.profile.ID, f.node.ID, proxies[0].ID, map[string]bool{u.ID: true})
		}, "original-external-secret"},
		{"relay", func() ([]store.Credential, error) {
			return svc.EnsureCredentialsForRelay(f.profile.ID, f.node.ID, relay.ID, map[string]bool{u.ID: true})
		}, "original-relay-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds, err := tc.call()
			if err != nil || len(creds) != 1 || creds[0].Secret != tc.want {
				t.Fatalf("credential dimension was not preserved: %d, %v", len(creds), err)
			}
		})
	}
}

func TestNormalServiceStillMintsMissingCredentials(t *testing.T) {
	f := newUserSnapshotFixture(t)
	u := f.addUser(t, "normal-user", 0, true, true, false)
	svc := NewService(f.st, f.logger)
	creds, err := svc.EnsureCredentials(f.profile.ID, f.node.ID)
	if err != nil || len(creds) != 1 || creds[0].UserID != u.ID || creds[0].Secret == "" {
		t.Fatalf("normal credential creation changed: %d, %v", len(creds), err)
	}
	again, err := svc.EnsureCredentials(f.profile.ID, f.node.ID)
	if err != nil || len(again) != 1 || again[0].Secret != creds[0].Secret {
		t.Fatal("normal service stopped preserving existing credentials")
	}
}

func TestSnapshotCredentialReadErrorsDoNotExposeDriverDetails(t *testing.T) {
	f := newUserSnapshotFixture(t)
	st := f.openSnapshot(t)
	svc, err := NewSnapshotService(st, f.logger, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if creds, err := svc.EnsureCredentials(f.profile.ID, f.node.ID); err == nil || creds != nil || err.Error() != "snapshot entitlements could not be read" {
		t.Fatalf("unsafe or unexpected snapshot query diagnostic: %v", err)
	}
}

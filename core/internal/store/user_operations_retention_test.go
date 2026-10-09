package store

import (
	"fmt"
	"strings"
	"testing"
)

type userOperationRetentionFixture struct {
	s     *Store
	u     User
	p     Profile
	n     Node
	c     Credential
	sub   ExternalSub
	proxy ExternalProxy
	relay NodeRelay
}

func newUserOperationRetentionFixture(t *testing.T, exit string) userOperationRetentionFixture {
	t.Helper()
	f := userOperationRetentionFixture{s: testStore(t, storeTestKey)}
	f.u = seedUser(t, f.s, "receipt-retention")
	var err error
	if f.p, err = f.s.CreateProfile("receipt-profile"); err != nil {
		t.Fatal(err)
	}
	if f.n, err = f.s.CreateNode("receipt-node", "receipt-token"); err != nil {
		t.Fatal(err)
	}
	f.c = Credential{UserID: f.u.ID, ProfileID: f.p.ID, NodeID: f.n.ID,
		Email: "receipt-retention@example.invalid", Secret: "unchanged-private-credential"}
	switch exit {
	case "proxy":
		if f.sub, err = f.s.CreateExternalSub("receipt-provider", "", "private-source"); err != nil {
			t.Fatal(err)
		}
		if err = f.s.ReplaceExternalProxies(f.sub.ID, []ExternalProxy{{Name: "receipt-exit", Type: "vless", Server: "exit.invalid", Port: 443, Config: "original-config"}}); err != nil {
			t.Fatal(err)
		}
		proxies, err := f.s.ExternalProxies(f.sub.ID)
		if err != nil || len(proxies) != 1 {
			t.Fatalf("proxies=%v err=%v", proxies, err)
		}
		f.proxy = proxies[0]
		f.c.ExitProxyID = f.proxy.ID
	case "relay":
		exitNode, err := f.s.CreateNode("receipt-exit-node", "receipt-exit-token")
		if err != nil {
			t.Fatal(err)
		}
		f.relay, err = f.s.CreateNodeRelay(NodeRelay{EntryNodeID: f.n.ID, ExitNodeID: exitNode.ID,
			ProfileID: f.p.ID, Label: "receipt-relay", Secret: "link-private", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		f.c.ExitRelayID = f.relay.ID
	}
	if f.c, err = f.s.PutCredential(f.c); err != nil {
		t.Fatal(err)
	}
	if err = f.s.BindUserProfile(f.u.ID, f.p.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.s.BeginUserOperation(f.n.ID, "rendered-inbound", f.c.Email, "pending-retirement", 2, "request-digest"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f userOperationRetentionFixture) assertPendingContext(t *testing.T) {
	t.Helper()
	c, err := f.s.FindCredentialForExit(f.u.ID, f.p.ID, f.n.ID, f.c.ExitProxyID, f.c.ExitRelayID)
	if err != nil || c != f.c {
		t.Fatalf("credential context changed: got=%+v want=%+v err=%v", c, f.c, err)
	}
	pending, err := f.s.UserOperationPending(f.n.ID, "rendered-inbound", f.c.Email)
	if err != nil || !pending {
		t.Fatalf("pending barrier lost: pending=%v err=%v", pending, err)
	}
	if _, err := f.s.GetUser(f.u.ID); err != nil {
		t.Fatalf("user cascade partially committed: %v", err)
	}
	if _, err := f.s.GetProfile(f.p.ID); err != nil {
		t.Fatalf("profile cascade partially committed: %v", err)
	}
	if _, err := f.s.GetNode(f.n.ID); err != nil {
		t.Fatalf("node cascade partially committed: %v", err)
	}
	if owner := readTrafficOwner(t, f.s, f.c.Email); owner.RetiredAt.Valid || !owner.UserID.Valid || owner.UserID.String != f.u.ID {
		t.Fatalf("failed deletion changed accounting ownership: %+v", owner)
	}
}

func TestPendingUserOperationProtectsExplicitAndCascadeDeletion(t *testing.T) {
	cases := []struct {
		name   string
		exit   string
		remove func(userOperationRetentionFixture) error
	}{
		{"direct", "", func(f userOperationRetentionFixture) error { return f.s.DeleteCredentialsForBinding(f.u.ID, f.p.ID) }},
		{"user", "", func(f userOperationRetentionFixture) error { return f.s.DeleteUser(f.u.ID) }},
		{"profile", "", func(f userOperationRetentionFixture) error { return f.s.DeleteProfile(f.p.ID) }},
		{"node", "", func(f userOperationRetentionFixture) error { return f.s.DeleteNode(f.n.ID) }},
		{"external-refresh", "proxy", func(f userOperationRetentionFixture) error { return f.s.ReplaceExternalProxies(f.sub.ID, nil) }},
		{"external-subscription", "proxy", func(f userOperationRetentionFixture) error { return f.s.DeleteExternalSub(f.sub.ID) }},
		{"relay", "relay", func(f userOperationRetentionFixture) error { return f.s.DeleteNodeRelay(f.relay.ID) }},
	}
	for _, tc := range cases {
		for _, result := range []int32{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/result-%d", tc.name, result), func(t *testing.T) {
				f := newUserOperationRetentionFixture(t, tc.exit)
				if result != 0 {
					if err := f.s.FinishUserOperation(f.n.ID, "pending-retirement", result); err != nil {
						t.Fatal(err)
					}
				}
				err := tc.remove(f)
				if result == 0 {
					if err == nil || !strings.Contains(err.Error(), "unresolved user operation") {
						t.Fatalf("uncertain deletion was not refused: %v", err)
					}
					f.assertPendingContext(t)
					return
				}
				if err != nil {
					t.Fatalf("terminal receipt blocked normal deletion: %v", err)
				}
				if _, err := f.s.FindCredentialForExit(f.u.ID, f.p.ID, f.n.ID, f.c.ExitProxyID, f.c.ExitRelayID); !IsNotFound(err) {
					t.Fatalf("terminal deletion did not remove credential: %v", err)
				}
			})
		}
	}
}

func TestPendingUserOperationRollsBackEntireExternalRefresh(t *testing.T) {
	f := newUserOperationRetentionFixture(t, "proxy")
	control := ExternalProxy{Name: "control", Type: "vless", Server: "control.invalid", Port: 443, Config: "before-refresh"}
	if err := f.s.ReplaceExternalProxies(f.sub.ID, []ExternalProxy{f.proxy, control}); err != nil {
		t.Fatal(err)
	}
	control.Config = "after-refresh"
	newProxy := ExternalProxy{Name: "new", Type: "vless", Server: "new.invalid", Port: 443, Config: "new-config"}
	if err := f.s.ReplaceExternalProxies(f.sub.ID, []ExternalProxy{control, newProxy}); err == nil {
		t.Fatal("refresh removed an unresolved credential")
	}
	f.assertPendingContext(t)
	proxies, err := f.s.ExternalProxies(f.sub.ID)
	if err != nil || len(proxies) != 2 {
		t.Fatalf("refresh committed a partial inventory: %v err=%v", proxies, err)
	}
	for _, p := range proxies {
		if p.Name == "control" && p.Config != "before-refresh" {
			t.Fatal("failed refresh kept an earlier update from the transaction")
		}
		if p.Name == "new" {
			t.Fatal("failed refresh kept a newly inserted proxy")
		}
	}
}

func TestPendingUserOperationPreservesCredentialIdentityButAllowsAccounting(t *testing.T) {
	for _, column := range []string{"id", "user_id", "profile_id", "node_id", "email", "secret", "exit_proxy_id", "exit_relay_id"} {
		t.Run(column, func(t *testing.T) {
			f := newUserOperationRetentionFixture(t, "")
			// The trigger must reject the identity change before any foreign-key
			// validation or later assembly can hide the original binding.
			_, err := f.s.db.Exec(`UPDATE credentials SET `+column+` = ? WHERE id = ?`, "changed-identity", f.c.ID)
			if err == nil || !strings.Contains(err.Error(), "unresolved user operation") {
				t.Fatalf("identity change was not refused: %v", err)
			}
			f.assertPendingContext(t)
		})
	}
	f := newUserOperationRetentionFixture(t, "")
	if _, err := f.s.db.Exec(`UPDATE credentials SET email = email, secret = secret, up_bytes = 17, down_bytes = 23 WHERE id = ?`, f.c.ID); err != nil {
		t.Fatalf("pending operation blocked accounting or a no-op identity update: %v", err)
	}
	if err := f.s.SetUserActive(f.u.ID, false); err != nil {
		t.Fatalf("pending operation blocked a nonidentity user flag: %v", err)
	}
	f.c.UpBytes, f.c.DownBytes = 17, 23
	f.assertPendingContext(t)
}

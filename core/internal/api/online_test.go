package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/online"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

type recentOnlineStub struct {
	status online.Status
	recent []online.RecentAddress
}

func (s *recentOnlineStub) Status(string, time.Time) online.Status { return s.status }
func (s *recentOnlineStub) Counts(time.Time) map[string]int        { return map[string]int{} }
func (s *recentOnlineStub) RecentAddresses(string, time.Time) []online.RecentAddress {
	return append([]online.RecentAddress(nil), s.recent...)
}

type exactOnlyOnlineStub struct{ status online.Status }

func (s *exactOnlyOnlineStub) Status(string, time.Time) online.Status { return s.status }
func (s *exactOnlyOnlineStub) Counts(time.Time) map[string]int        { return map[string]int{} }

func seedAdminRole(t *testing.T, st *store.Store, username string, role auth.Role) string {
	t.Helper()
	a, err := st.CreateAdmin(username, "unused-test-password-hash", role)
	if err != nil {
		t.Fatal(err)
	}
	token, tokenHash := auth.NewSecret()
	if err := st.CreateSession(tokenHash, a.ID, store.SessionTTL); err != nil {
		t.Fatal(err)
	}
	return token
}

func seedOnlineUser(t *testing.T, st *store.Store) store.User {
	t.Helper()
	u, err := st.CreateUser(store.User{Name: "online-test-user", Enabled: true, DeviceLimit: 2}, "online-test-hash")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestApproximateOnlineAggregateDoesNotLeakAddresses(t *testing.T) {
	srv, st := portalFixture(t)
	u := seedOnlineUser(t, st)
	recentIP := "203.0.113.19"
	srv.EnableOnlineTracking(&recentOnlineStub{
		status: online.Status{
			Count:          0,
			Partial:        true,
			NodesReporting: 0,
			Estimate: &online.Estimate{
				ObservedOnline:     false,
				RecentAddressCount: 1,
				RetrievedAt:        1_700_000_000,
				NodesReporting:     1,
				ReadOK:             true,
			},
		},
		recent: []online.RecentAddress{{
			IP: recentIP, NodeID: "approx-node", RetrievedAt: 1_700_000_000,
		}},
	})
	h := srv.Handler()

	for _, tc := range []struct {
		name string
		role auth.Role
	}{
		{name: "viewer", role: auth.RoleViewer},
		{name: "operator", role: auth.RoleOperator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := seedAdminRole(t, st, tc.name, tc.role)

			status := do(t, h, http.MethodGet, "/api/users/"+u.ID+"/online", token, nil)
			if status.Code != http.StatusOK {
				t.Fatalf("online status %d, want 200: %s", status.Code, status.Body)
			}
			if strings.Contains(status.Body.String(), recentIP) {
				t.Fatal("aggregate online response leaked a recent source address")
			}
			var got onlineView
			if err := json.Unmarshal(status.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Count != 0 {
				t.Fatalf("exact count = %d, want 0; approximate addresses must stay separate", got.Count)
			}
			if got.Estimate == nil || got.Estimate.RecentAddressCount != 1 || !got.Estimate.ReadOK {
				t.Fatalf("estimate = %#v, want one readable recent address", got.Estimate)
			}

			list := do(t, h, http.MethodGet, "/api/users", token, nil)
			if list.Code != http.StatusOK {
				t.Fatalf("user list status %d, want 200: %s", list.Code, list.Body)
			}
			if strings.Contains(list.Body.String(), recentIP) {
				t.Fatal("user list leaked a recent source address")
			}
			var listed struct {
				Users []userView `json:"users"`
			}
			if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Users) != 1 || listed.Users[0].OnlineEstimate == nil {
				t.Fatalf("user list estimate missing: %#v", listed.Users)
			}
			if listed.Users[0].OnlineDevices == nil || *listed.Users[0].OnlineDevices != 0 {
				t.Fatalf("exact list count = %#v, want an explicit zero", listed.Users[0].OnlineDevices)
			}

			addresses := do(t, h, http.MethodGet, "/api/users/"+u.ID+"/devices", token, nil)
			if addresses.Code != http.StatusForbidden {
				t.Fatalf("address status %d, want 403", addresses.Code)
			}
			if strings.Contains(addresses.Body.String(), recentIP) {
				t.Fatal("forbidden address response leaked a recent source address")
			}
		})
	}
}

func TestSuperadminRecentAddressesAreAuditedAndNotPersisted(t *testing.T) {
	srv, st := portalFixture(t)
	u := seedOnlineUser(t, st)
	exactIP := "198.51.100.8"
	recentIP := "203.0.113.27"
	observedAt := time.Unix(1_700_000_000, 0)
	if err := st.RecordDevice(u.ID, exactIP, "exact-node", observedAt); err != nil {
		t.Fatal(err)
	}
	srv.EnableOnlineTracking(&recentOnlineStub{
		status: online.Status{Estimate: &online.Estimate{
			ObservedOnline: true, RecentAddressCount: 1,
			RetrievedAt: observedAt.Unix(), NodesReporting: 1, ReadOK: true,
		}},
		recent: []online.RecentAddress{{
			IP: recentIP, NodeID: "approx-node", RetrievedAt: observedAt.Unix(),
		}},
	})
	token := seedAdmin(t, st)

	w := do(t, srv.Handler(), http.MethodGet, "/api/users/"+u.ID+"/devices", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body)
	}
	var got struct {
		Devices         []deviceView        `json:"devices"`
		RecentAddresses []recentAddressView `json:"recent_addresses"`
		Recording       bool                `json:"recording"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Recording {
		t.Fatal("recording=false with an online source installed")
	}
	if len(got.Devices) != 1 || got.Devices[0].IP != exactIP {
		t.Fatalf("exact devices = %#v", got.Devices)
	}
	if len(got.RecentAddresses) != 1 || got.RecentAddresses[0].IP != recentIP {
		t.Fatalf("recent addresses = %#v", got.RecentAddresses)
	}
	if got.RecentAddresses[0].RetrievedAt != observedAt.Unix() {
		t.Fatalf("retrieved_at = %d, want %d", got.RecentAddresses[0].RetrievedAt, observedAt.Unix())
	}

	stored, err := st.UserDevices(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].IP != exactIP {
		t.Fatalf("reading recent addresses changed durable device history: %#v", stored)
	}

	entries, err := st.AuditPage(0, 10, "", "user.devices_viewed")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].TargetID != u.ID {
		t.Fatalf("audit entries = %#v", entries)
	}
}

func TestDevicesResponseSupportsExactOnlyOnlineSources(t *testing.T) {
	srv, st := portalFixture(t)
	u := seedOnlineUser(t, st)
	srv.EnableOnlineTracking(&exactOnlyOnlineStub{status: online.Status{
		Count: 1, NodesReporting: 1,
	}})
	token := seedAdmin(t, st)

	w := do(t, srv.Handler(), http.MethodGet, "/api/users/"+u.ID+"/devices", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body)
	}
	var got struct {
		RecentAddresses []recentAddressView `json:"recent_addresses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.RecentAddresses == nil || len(got.RecentAddresses) != 0 {
		t.Fatalf("recent_addresses = %#v, want an empty array", got.RecentAddresses)
	}
}

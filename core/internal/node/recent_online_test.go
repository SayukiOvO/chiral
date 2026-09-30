package node

import (
	"strings"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/online"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func TestRecentOnlineReportIsNormalizedWithoutDurableDeviceWrites(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	userID, nodeID, email := seedCredential(t, st, "mai", "tokyo")
	oldestExpected := time.Now().Add(-11 * time.Second).Unix()

	svc.recordRecentOnline(nodeID, &chiralv1.RecentOnlineReport{
		RetrievedAgeSeconds: 10,
		ReadOk:              true,
		Users: []*chiralv1.RecentOnlineUser{{
			Email:          email,
			ObservedOnline: true,
			Ips: []string{
				"2001:0db8::1",
				"2001:db8:0:0::1",
				"::ffff:192.0.2.1",
			},
		}},
	})

	status := reg.Status(userID, time.Now())
	if status.Count != 0 || !status.Partial || status.Estimate == nil {
		t.Fatalf("status = %+v, want a separate approximate estimate", status)
	}
	estimate := status.Estimate
	if !estimate.ReadOK || !estimate.ObservedOnline || estimate.RecentAddressCount != 2 || estimate.NodesReporting != 1 {
		t.Fatalf("estimate = %+v, want one successful normalized sample", estimate)
	}
	newestExpected := time.Now().Add(-9 * time.Second).Unix()
	if estimate.RetrievedAt < oldestExpected || estimate.RetrievedAt > newestExpected {
		t.Fatalf("retrieved_at = %d, want Core receive time minus reported age", estimate.RetrievedAt)
	}

	addresses := reg.RecentAddresses(userID, time.Now())
	if len(addresses) != 2 {
		t.Fatalf("addresses = %+v, want two normalized distinct addresses", addresses)
	}
	want := map[string]bool{"192.0.2.1": false, "2001:db8::1": false}
	for _, address := range addresses {
		if _, ok := want[address.IP]; !ok {
			t.Fatalf("unexpected normalized address: %+v", address)
		}
		want[address.IP] = true
		if address.NodeID != nodeID || address.RetrievedAt != estimate.RetrievedAt {
			t.Fatalf("address provenance = %+v, estimate = %+v", address, estimate)
		}
	}
	for ip, seen := range want {
		if !seen {
			t.Fatalf("normalized address %s was not retained", ip)
		}
	}
	devices, err := st.UserDevices(userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Fatalf("approximate addresses were persisted as real last-seen evidence: %+v", devices)
	}
}

func TestRecentOnlineCannotUseAnotherNodesCredential(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	victimID, _, victimEmail := seedCredential(t, st, "victim", "frankfurt")
	_, attackerNodeID, _ := seedCredential(t, st, "attacker", "tokyo")

	svc.recordRecentOnline(attackerNodeID, &chiralv1.RecentOnlineReport{
		ReadOk: true,
		Users: []*chiralv1.RecentOnlineUser{{
			Email: victimEmail, ObservedOnline: true, Ips: []string{"192.0.2.9"},
		}},
	})

	status := reg.Status(victimID, time.Now())
	if status.Estimate == nil || status.Estimate.ObservedOnline || status.Estimate.RecentAddressCount != 0 {
		t.Fatalf("cross-node credential affected the victim estimate: %+v", status.Estimate)
	}
	if got := reg.RecentAddresses(victimID, time.Now()); len(got) != 0 {
		t.Fatalf("cross-node credential recorded addresses: %+v", got)
	}
}

func TestRecentOnlineReportsAreIgnoredWhenTrackingIsDisabled(t *testing.T) {
	svc, reg, st := onlineFixture(t)
	userID, nodeID, email := seedCredential(t, st, "mai", "tokyo")
	svc.online = nil

	svc.recordRecentOnline(nodeID, &chiralv1.RecentOnlineReport{
		ReadOk: true,
		Users: []*chiralv1.RecentOnlineUser{{
			Email: email, ObservedOnline: true, Ips: []string{"192.0.2.1"},
		}},
	})

	status := reg.Status(userID, time.Now())
	if status.Estimate != nil || status.Count != 0 {
		t.Fatalf("disabled tracking accepted a recent report: %+v", status)
	}
}

func TestRecentOnlineEmptyFailedAndStaleReportsRemainUnknown(t *testing.T) {
	tests := []struct {
		name       string
		report     *chiralv1.RecentOnlineReport
		wantReadOK bool
		wantNodes  int
	}{
		{name: "empty successful read", report: &chiralv1.RecentOnlineReport{ReadOk: true}, wantReadOK: true, wantNodes: 1},
		{name: "failed read", report: &chiralv1.RecentOnlineReport{}, wantReadOK: false, wantNodes: 1},
		{name: "stale read", report: &chiralv1.RecentOnlineReport{ReadOk: true, RetrievedAgeSeconds: 120}, wantReadOK: false, wantNodes: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, reg, st := onlineFixture(t)
			userID, nodeID, _ := seedCredential(t, st, "mai", "tokyo")
			svc.recordRecentOnline(nodeID, tt.report)

			status := reg.Status(userID, time.Now())
			if status.Estimate == nil || status.Estimate.ReadOK != tt.wantReadOK || status.Estimate.NodesReporting != tt.wantNodes {
				t.Fatalf("estimate = %+v, want read_ok=%t nodes=%d", status.Estimate, tt.wantReadOK, tt.wantNodes)
			}
			if status.Estimate.ObservedOnline || status.Estimate.RecentAddressCount != 0 || !status.Partial || status.Count != 0 {
				t.Fatalf("non-positive approximate report became exact offline or positive: %+v", status)
			}
		})
	}
}

func TestInvalidRecentOnlineReportIsRejectedAtomically(t *testing.T) {
	tests := []struct {
		name   string
		report *chiralv1.RecentOnlineReport
	}{
		{name: "nil report", report: nil},
		{name: "failed read with users", report: &chiralv1.RecentOnlineReport{Users: []*chiralv1.RecentOnlineUser{{Email: "x"}}}},
		{name: "too many users", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: make([]*chiralv1.RecentOnlineUser, chiralv1.MaxRecentOnlineUsers+1)}},
		{name: "nil user", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{nil}}},
		{name: "empty email", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{}}}},
		{name: "long email", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: strings.Repeat("e", chiralv1.MaxRecentOnlineEmailBytes+1)}}}},
		{name: "invalid email encoding", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: string([]byte{0xff})}}}},
		{name: "email surrounding space", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: " email"}}}},
		{name: "email control character", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: "a\nb"}}}},
		{name: "duplicate email", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: "same"}, {Email: "same"}}}},
		{name: "too many addresses", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: "x", Ips: make([]string, chiralv1.MaxRecentOnlineIPs+1)}}}},
		{name: "invalid address", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: "x", Ips: []string{"not-an-address"}}}}},
		{name: "scoped address", report: &chiralv1.RecentOnlineReport{ReadOk: true, Users: []*chiralv1.RecentOnlineUser{{Email: "x", Ips: []string{"fe80::1%eth0"}}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, reg, st := onlineFixture(t)
			userID, nodeID, email := seedCredential(t, st, "mai", "tokyo")
			svc.recordRecentOnline(nodeID, &chiralv1.RecentOnlineReport{
				ReadOk: true,
				Users: []*chiralv1.RecentOnlineUser{{
					Email: email, ObservedOnline: true, Ips: []string{"192.0.2.1"},
				}},
			})
			before := reg.Status(userID, time.Now())
			if before.Estimate == nil || !before.Estimate.ObservedOnline {
				t.Fatalf("test setup did not install a positive estimate: %+v", before)
			}

			svc.recordRecentOnline(nodeID, tt.report)
			after := reg.Status(userID, time.Now())
			if after.Estimate == nil || !after.Estimate.ReadOK || !after.Estimate.ObservedOnline || after.Estimate.RecentAddressCount != 1 {
				t.Fatalf("invalid report partially replaced prior state: %+v", after.Estimate)
			}
		})
	}
}

func TestRecentRegistryCapabilityIsOptional(t *testing.T) {
	var exactOnly OnlineRegistry = exactOnlyOnlineRegistry{}
	if _, ok := exactOnly.(RecentOnlineRegistry); ok {
		t.Fatal("legacy exact registry unexpectedly negotiated recent observations")
	}
	var withRecent OnlineRegistry = online.New(time.Minute)
	if _, ok := withRecent.(RecentOnlineRegistry); !ok {
		t.Fatal("online.Registry did not negotiate recent observations")
	}
}

type exactOnlyOnlineRegistry struct{}

func (exactOnlyOnlineRegistry) Replace(string, []online.Observation, bool, time.Time) {}
func (exactOnlyOnlineRegistry) Forget(string)                                         {}

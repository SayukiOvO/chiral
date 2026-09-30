package online

import (
	"testing"
	"time"
)

func TestRecentModeRemovesOnlyThatNodesExactContribution(t *testing.T) {
	r := New(time.Minute)
	r.Replace("tokyo", []Observation{{UserID: "u1", IP: "192.0.2.1", At: t0}}, true, t0)
	r.Replace("frankfurt", []Observation{{UserID: "u1", IP: "192.0.2.1", At: t0}}, true, t0)

	r.ReplaceRecent("tokyo", []RecentObservation{{
		UserID: "u1", ObservedOnline: true, IPs: []string{"192.0.2.1"},
	}}, true, t0)

	st := r.Status("u1", t0)
	if st.Count != 1 || st.NodesReporting != 1 {
		t.Fatalf("exact status = %+v, want the Frankfurt contribution preserved", st)
	}
	if !st.Partial || st.Estimate == nil || !st.Estimate.ReadOK || !st.Estimate.ObservedOnline {
		t.Fatalf("recent estimate = %+v, want a successful approximate observation", st)
	}
	if st.Estimate.RecentAddressCount != 1 || st.Estimate.NodesReporting != 1 {
		t.Fatalf("recent estimate = %+v, want one address from one node", st.Estimate)
	}
}

func TestEmptyAndFailedRecentReportsRemainUnknown(t *testing.T) {
	r := New(time.Minute)
	r.ReplaceRecent("tokyo", []RecentObservation{{
		UserID: "u1", ObservedOnline: true, IPs: []string{"192.0.2.1"},
	}}, true, t0)

	r.ReplaceRecent("tokyo", nil, true, t0.Add(time.Second))
	st := r.Status("u1", t0.Add(time.Second))
	if st.Estimate == nil || !st.Estimate.ReadOK || st.Estimate.ObservedOnline || st.Estimate.RecentAddressCount != 0 {
		t.Fatalf("empty provider read = %+v, want a successful but non-positive estimate", st.Estimate)
	}
	if !st.Partial || st.Count != 0 {
		t.Fatalf("empty approximate read became an exact offline result: %+v", st)
	}

	r.ReplaceRecent("tokyo", nil, false, t0.Add(2*time.Second))
	st = r.Status("u1", t0.Add(2*time.Second))
	if st.Estimate == nil || st.Estimate.ReadOK || st.Estimate.ObservedOnline || st.Estimate.RecentAddressCount != 0 {
		t.Fatalf("failed provider read = %+v, want an explicit unknown estimate", st.Estimate)
	}
	if !st.Partial {
		t.Fatalf("failed provider read became a complete fleet result: %+v", st)
	}
}

func TestStaleRecentSampleStaysUnknownAfterPrune(t *testing.T) {
	r := New(time.Minute)
	r.ReplaceRecent("tokyo", []RecentObservation{{
		UserID: "u1", ObservedOnline: true, IPs: []string{"192.0.2.1"},
	}}, true, t0)
	later := t0.Add(2 * time.Minute)

	assertUnknown := func(stage string) {
		t.Helper()
		st := r.Status("u1", later)
		if st.Estimate == nil || st.Estimate.ReadOK || st.Estimate.NodesReporting != 0 {
			t.Fatalf("%s stale estimate = %+v, want non-nil unknown with no reporting node", stage, st.Estimate)
		}
		if !st.Partial || st.Count != 0 {
			t.Fatalf("%s stale sample became exact offline: %+v", stage, st)
		}
		if got := r.RecentAddresses("u1", later); len(got) != 0 {
			t.Fatalf("%s stale addresses = %v, want none", stage, got)
		}
	}

	assertUnknown("before prune")
	r.Prune(later)
	assertUnknown("after prune")
}

func TestRecentSameAddressAcrossNodesKeepsNodeProvenance(t *testing.T) {
	r := New(time.Minute)
	observation := []RecentObservation{{
		UserID: "u1", ObservedOnline: true, IPs: []string{"192.0.2.1"},
	}}
	r.ReplaceRecent("tokyo", observation, true, t0)
	r.ReplaceRecent("frankfurt", observation, true, t0)

	st := r.Status("u1", t0)
	if st.Estimate == nil || st.Estimate.RecentAddressCount != 1 || st.Estimate.NodesReporting != 2 {
		t.Fatalf("estimate = %+v, want one distinct address reported by two nodes", st.Estimate)
	}
	addresses := r.RecentAddresses("u1", t0)
	if len(addresses) != 2 || addresses[0].NodeID != "frankfurt" || addresses[1].NodeID != "tokyo" {
		t.Fatalf("addresses = %+v, want per-node provenance", addresses)
	}

	r.Forget("tokyo")
	st = r.Status("u1", t0)
	if st.Estimate == nil || st.Estimate.RecentAddressCount != 1 || st.Estimate.NodesReporting != 1 {
		t.Fatalf("estimate after Forget = %+v, want Frankfurt preserved", st.Estimate)
	}
	addresses = r.RecentAddresses("u1", t0)
	if len(addresses) != 1 || addresses[0].NodeID != "frankfurt" {
		t.Fatalf("addresses after Forget = %+v, want only Frankfurt", addresses)
	}
}

func TestExactReportClearsThatNodesRecentMode(t *testing.T) {
	r := New(time.Minute)
	r.ReplaceRecent("tokyo", []RecentObservation{{UserID: "u1", ObservedOnline: true}}, true, t0)
	r.Replace("tokyo", []Observation{{UserID: "u1", IP: "192.0.2.1", At: t0}}, true, t0)

	st := r.Status("u1", t0)
	if st.Count != 1 || st.Partial || st.Estimate != nil {
		t.Fatalf("status = %+v, want only the new exact snapshot", st)
	}
}

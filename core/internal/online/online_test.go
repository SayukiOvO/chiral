package online

import (
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func TestReplaceInstallsOneNodesView(t *testing.T) {
	r := New(time.Minute)
	r.Replace("n1", []Observation{
		{UserID: "u1", IP: "1.1.1.1", At: t0},
		{UserID: "u1", IP: "2.2.2.2", At: t0},
	}, true, t0)

	if got := r.Status("u1", t0).Count; got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

// The report is absolute, so an address a node stops mentioning has gone away.
func TestReplaceDropsAddressesTheNodeNoLongerReports(t *testing.T) {
	r := New(time.Minute)
	r.Replace("n1", []Observation{
		{UserID: "u1", IP: "1.1.1.1", At: t0},
		{UserID: "u1", IP: "2.2.2.2", At: t0},
	}, true, t0)
	r.Replace("n1", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, true, t0)

	if got := r.Status("u1", t0).Count; got != 1 {
		t.Fatalf("count = %d, want 1 after the second address went away", got)
	}
}

// The failure this guards against would make every user's count collapse to
// whatever the most recent node happened to report.
func TestReplaceLeavesOtherNodesAlone(t *testing.T) {
	r := New(time.Minute)
	r.Replace("tokyo", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, true, t0)
	r.Replace("frankfurt", []Observation{{UserID: "u1", IP: "2.2.2.2", At: t0}}, true, t0)

	if got := r.Status("u1", t0).Count; got != 2 {
		t.Fatalf("count = %d, want 2; one node's report cleared another's", got)
	}
}

// A union, not a sum: one device reaching two nodes is one address. Summing
// would double every roaming user and make any threshold meaningless.
func TestSameAddressAcrossNodesCountsOnce(t *testing.T) {
	r := New(time.Minute)
	r.Replace("tokyo", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, true, t0)
	r.Replace("frankfurt", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, true, t0)

	if got := r.Status("u1", t0).Count; got != 1 {
		t.Fatalf("count = %d, want 1; the same address was counted per node", got)
	}
}

func TestIncompleteRoundMarksTheStatusPartial(t *testing.T) {
	r := New(time.Minute)
	r.Replace("n1", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, false, t0)

	st := r.Status("u1", t0)
	if !st.Partial {
		t.Fatal("a truncated round was reported as a complete view")
	}
}

func TestForgetDropsANodesContributionImmediately(t *testing.T) {
	r := New(time.Hour)
	r.Replace("tokyo", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, true, t0)
	r.Replace("frankfurt", []Observation{{UserID: "u1", IP: "2.2.2.2", At: t0}}, true, t0)
	r.Forget("tokyo")

	st := r.Status("u1", t0)
	if st.Count != 1 {
		t.Fatalf("count = %d, want 1 after tokyo disconnected", st.Count)
	}
	if st.NodesReporting != 1 {
		t.Fatalf("nodes reporting = %d, want 1", st.NodesReporting)
	}
}

func TestSightingsExpire(t *testing.T) {
	r := New(time.Minute)
	r.Replace("n1", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, true, t0)

	later := t0.Add(2 * time.Minute)
	if got := r.Status("u1", later).Count; got != 0 {
		t.Fatalf("count = %d, want 0 once the sighting aged out", got)
	}
	r.Prune(later)
	if len(r.users) != 0 {
		t.Fatalf("prune left %d users behind", len(r.users))
	}
}

func TestCountsCoversEveryUser(t *testing.T) {
	r := New(time.Minute)
	r.Replace("n1", []Observation{
		{UserID: "u1", IP: "1.1.1.1", At: t0},
		{UserID: "u2", IP: "2.2.2.2", At: t0},
		{UserID: "u2", IP: "3.3.3.3", At: t0},
	}, true, t0)

	counts := r.Counts(t0)
	if counts["u1"] != 1 || counts["u2"] != 2 {
		t.Fatalf("counts = %v, want u1:1 u2:2", counts)
	}
}

// An empty report is a statement — "nobody is connected here" — and must clear
// that node's addresses, unlike a node that has gone silent.
func TestEmptyReportClearsThatNode(t *testing.T) {
	r := New(time.Minute)
	r.Replace("n1", []Observation{{UserID: "u1", IP: "1.1.1.1", At: t0}}, true, t0)
	r.Replace("n1", nil, true, t0)

	if got := r.Status("u1", t0).Count; got != 0 {
		t.Fatalf("count = %d, want 0 after an empty report", got)
	}
	if got := r.Status("u1", t0).NodesReporting; got != 1 {
		t.Fatalf("nodes reporting = %d, want 1; an empty report is still a report", got)
	}
}

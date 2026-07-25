package store

import (
	"testing"
	"time"
)

func TestNodeSamplesRoundTrip(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	base := time.Now().Truncate(NodeSampleInterval)

	for i := 0; i < 3; i++ {
		at := base.Add(time.Duration(i) * NodeSampleInterval)
		if err := s.PutNodeSample(n.ID, at, NodeSample{
			CPUPercent: float64(10 * (i + 1)), MemUsedBytes: int64(1000 * (i + 1)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.NodeSamples(n.ID, base.Add(-time.Minute), base.Add(10*NodeSampleInterval))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(got))
	}
	// Oldest first, so a chart can draw straight through.
	if got[0].At >= got[1].At || got[1].At >= got[2].At {
		t.Errorf("samples are not in time order: %+v", got)
	}
	if got[0].CPUPercent != 10 || got[2].MemUsedBytes != 3000 {
		t.Errorf("values did not round-trip: %+v", got)
	}
}

// Heartbeats arrive far more often than the sample interval; the extras must
// be absorbed rather than piling up or failing.
func TestHeartbeatsWithinOneIntervalCollapse(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	base := time.Now().Truncate(NodeSampleInterval)

	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(i) * 10 * time.Second) // 6 heartbeats, one interval
		if err := s.PutNodeSample(n.ID, at, NodeSample{CPUPercent: float64(i)}); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
	}
	got, _ := s.NodeSamples(n.ID, base.Add(-time.Minute), base.Add(NodeSampleInterval))
	if len(got) != 1 {
		t.Fatalf("expected one sample for the interval, got %d", len(got))
	}
	// First of the interval wins; a later one must not overwrite it.
	if got[0].CPUPercent != 0 {
		t.Errorf("a later heartbeat overwrote the interval's sample: %+v", got[0])
	}
}

func TestNodeSamplesAreWindowed(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	base := time.Now().Truncate(NodeSampleInterval)
	for i := 0; i < 5; i++ {
		s.PutNodeSample(n.ID, base.Add(time.Duration(i)*NodeSampleInterval), NodeSample{CPUPercent: float64(i)})
	}
	// [from, to) — the upper bound is exclusive.
	got, _ := s.NodeSamples(n.ID, base.Add(NodeSampleInterval), base.Add(3*NodeSampleInterval))
	if len(got) != 2 {
		t.Fatalf("expected 2 samples in the half-open window, got %d", len(got))
	}
	if got[0].CPUPercent != 1 || got[1].CPUPercent != 2 {
		t.Errorf("wrong slice of the series: %+v", got)
	}
}

func TestDeletingNodeCascadesToSamples(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	s.PutNodeSample(n.ID, time.Now(), NodeSample{CPUPercent: 1})
	if err := s.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM node_samples WHERE node_id = ?`, n.ID).Scan(&count)
	if count != 0 {
		t.Errorf("%d samples outlived their node", count)
	}
}

// --- traffic ---

func TestTrafficAccumulatesIntoBuckets(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	u := seedUser(t, s, "alice")
	base := time.Now().Truncate(TrafficBucket)

	// Three reports inside one bucket must sum, not replace.
	for i := 0; i < 3; i++ {
		if err := s.AddTraffic(n.ID, u.ID, base.Add(time.Duration(i)*time.Minute), 100, 200); err != nil {
			t.Fatal(err)
		}
	}
	points, err := s.TrafficSeries(u.ID, "", base.Add(-time.Hour), base.Add(TrafficBucket))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("expected one bucket, got %d", len(points))
	}
	if points[0].UpBytes != 300 || points[0].DownBytes != 600 {
		t.Errorf("deltas did not accumulate: %+v", points[0])
	}
}

func TestTrafficSeriesNarrowsByUserAndNode(t *testing.T) {
	s := testStore(t, storeTestKey)
	n1, _ := s.CreateNode("tokyo-1", "h1")
	n2, _ := s.CreateNode("frankfurt-1", "h2")
	alice := seedUser(t, s, "alice")
	bob := seedUser(t, s, "bob")
	at := time.Now()

	s.AddTraffic(n1.ID, alice.ID, at, 10, 0)
	s.AddTraffic(n2.ID, alice.ID, at, 20, 0)
	s.AddTraffic(n1.ID, bob.ID, at, 40, 0)

	from, to := at.Add(-time.Hour), at.Add(time.Hour)
	sum := func(userID, nodeID string) int64 {
		points, err := s.TrafficSeries(userID, nodeID, from, to)
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, p := range points {
			total += p.UpBytes
		}
		return total
	}
	if got := sum("", ""); got != 70 {
		t.Errorf("fleet total = %d, want 70", got)
	}
	if got := sum(alice.ID, ""); got != 30 {
		t.Errorf("alice = %d, want 30", got)
	}
	if got := sum("", n1.ID); got != 50 {
		t.Errorf("node tokyo-1 = %d, want 50", got)
	}
	if got := sum(alice.ID, n1.ID); got != 10 {
		t.Errorf("alice on tokyo-1 = %d, want 10", got)
	}
}

// Traffic for a credential whose user is already gone still counts towards the
// node and the fleet — losing the total would be worse than an ownerless row.
func TestTrafficWithNoOwnerStillCounts(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	at := time.Now()
	if err := s.AddTraffic(n.ID, "", at, 500, 500); err != nil {
		t.Fatal(err)
	}
	points, _ := s.TrafficSeries("", "", at.Add(-time.Hour), at.Add(time.Hour))
	if len(points) != 1 || points[0].UpBytes != 500 {
		t.Errorf("ownerless traffic was dropped: %+v", points)
	}
	// It must not be attributed to some user.
	if got, _ := s.TrafficSeries("someone", "", at.Add(-time.Hour), at.Add(time.Hour)); len(got) != 0 {
		t.Errorf("ownerless traffic was attributed to a user: %+v", got)
	}
}

// A user's history must survive their deletion, or the fleet totals move
// retroactively.
func TestUserHistorySurvivesDeletion(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	u := seedUser(t, s, "alice")
	at := time.Now()
	s.AddTraffic(n.ID, u.ID, at, 100, 100)

	if err := s.DeleteUser(u.ID); err != nil {
		t.Fatal(err)
	}
	points, _ := s.TrafficSeries("", "", at.Add(-time.Hour), at.Add(time.Hour))
	var total int64
	for _, p := range points {
		total += p.UpBytes
	}
	if total != 100 {
		t.Errorf("fleet history changed when a user was deleted: %d", total)
	}
}

func TestZeroTrafficWritesNothing(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	if err := s.AddTraffic(n.ID, "", time.Now(), 0, 0); err != nil {
		t.Fatal(err)
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM traffic_buckets`).Scan(&count)
	if count != 0 {
		t.Errorf("an empty report created %d rows", count)
	}
}

// --- retention ---

func TestPruneDropsOnlyExpiredRows(t *testing.T) {
	s := testStore(t, storeTestKey)
	n, _ := s.CreateNode("tokyo-1", "h")
	now := time.Now()

	s.PutNodeSample(n.ID, now.Add(-NodeSampleRetention-time.Hour), NodeSample{CPUPercent: 1}) // expired
	s.PutNodeSample(n.ID, now.Add(-time.Hour), NodeSample{CPUPercent: 2})                     // fresh
	s.AddTraffic(n.ID, "", now.Add(-TrafficRetention-time.Hour), 10, 0)                       // expired
	s.AddTraffic(n.ID, "", now.Add(-time.Hour), 20, 0)                                        // fresh

	pruned, err := s.PruneHistory(now)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 2 {
		t.Errorf("expected 2 rows pruned, got %d", pruned)
	}

	samples, _ := s.NodeSamples(n.ID, now.Add(-NodeSampleRetention), now.Add(time.Hour))
	if len(samples) != 1 || samples[0].CPUPercent != 2 {
		t.Errorf("wrong samples survived: %+v", samples)
	}
	points, _ := s.TrafficSeries("", "", now.Add(-TrafficRetention), now.Add(time.Hour))
	if len(points) != 1 || points[0].UpBytes != 20 {
		t.Errorf("wrong traffic survived: %+v", points)
	}
}

func TestPruneOnEmptyDatabaseIsHarmless(t *testing.T) {
	s := testStore(t, storeTestKey)
	if n, err := s.PruneHistory(time.Now()); err != nil || n != 0 {
		t.Errorf("n=%d err=%v", n, err)
	}
}

func TestCredentialOwnerResolvesTheSeriesKeys(t *testing.T) {
	s := testStore(t, storeTestKey)
	u := seedUser(t, s, "alice")
	p, _ := s.CreateProfile("p")
	n, _ := s.CreateNode("tokyo-1", "h")
	if _, err := s.PutCredential(Credential{
		UserID: u.ID, ProfileID: p.ID, NodeID: n.ID, Email: "alice.x@p.n", Secret: "s",
	}); err != nil {
		t.Fatal(err)
	}
	userID, nodeID, err := s.CredentialOwner("alice.x@p.n")
	if err != nil {
		t.Fatal(err)
	}
	if userID != u.ID || nodeID != n.ID {
		t.Errorf("got user=%s node=%s", userID, nodeID)
	}
	if _, _, err := s.CredentialOwner("ghost@nowhere"); !IsNotFound(err) {
		t.Errorf("expected a not-found for an unknown email, got %v", err)
	}
}

package upgrade

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/release"

	"github.com/SayukiOvO/chiral/core/internal/alert"
	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type fakeAlerts struct {
	mu     sync.Mutex
	events []alert.Event
}

func (f *fakeAlerts) Announce(_ context.Context, e alert.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func (f *fakeAlerts) all() []alert.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]alert.Event(nil), f.events...)
}

// canaryFixture builds a service with a fleet whose upgrade can be driven by
// hand, without any release lookup — these tests are about the state machine.
func canaryFixture(t *testing.T, extraNodes int) (*Service, *store.Store, *fakeAlerts, store.Node) {
	t.Helper()
	svc, st, _, canary := fixture(t)
	// Point release lookups at a closed port: these tests are about the state
	// machine, and reaching GitHub would make them slow and flaky without
	// testing anything they are about.
	svc.releases = release.Client{HTTP: &http.Client{Timeout: 200 * time.Millisecond}}
	alerts := &fakeAlerts{}
	svc.EnableAlerts(alerts)
	for i := 0; i < extraNodes; i++ {
		n, err := st.CreateNode(string(rune('a'+i))+"-node", "join-"+string(rune('a'+i)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetNodePlatform(n.ID, "linux/amd64"); err != nil {
			t.Fatal(err)
		}
	}
	return svc, st, alerts, canary
}

// openUpgrade puts an upgrade straight into a state, skipping the install that
// would need the network.
func openUpgrade(t *testing.T, st *store.Store, version, canaryID, state string) store.Upgrade {
	t.Helper()
	u, err := st.StartUpgrade(version, canaryID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if state != store.UpgradeCanary {
		if ok, err := st.SetUpgradeState(u.ID, store.UpgradeCanary, state, "", time.Now()); err != nil || !ok {
			t.Fatalf("could not open the upgrade in %s: ok=%v err=%v", state, ok, err)
		}
		u.State = state
	}
	return u
}

func activeState(t *testing.T, st *store.Store) store.Upgrade {
	t.Helper()
	u, err := st.ActiveUpgrade()
	if err != nil {
		t.Fatalf("no active upgrade: %v", err)
	}
	return u
}

// The whole point of a canary: it does not continue on its own. An upgrade that
// promoted itself would turn one bad release into a fleet-wide outage at
// machine speed.
func TestASuccessfulCanaryWaitsForAPerson(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 2)
	u := openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeCanary)
	if err := st.StartXrayInstall(store.XrayInstall{
		NodeID: canary.ID, Version: "26.9.1", SHA256: strings.Repeat("a", 64), Phase: "ACTIVATING",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}

	svc.HandleStatus(canary.ID, &chiralv1.XrayStatus{
		Version:        "26.9.1",
		Phase:          chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE,
		RunningVersion: "26.9.1",
	})

	got := activeState(t, st)
	if got.State != store.UpgradeAwaitingPromote {
		t.Fatalf("state = %q, want %q", got.State, store.UpgradeAwaitingPromote)
	}
	if got.ID != u.ID {
		t.Fatalf("a different upgrade became active")
	}
}

// INCONCLUSIVE still waits, but must say what it could not confirm — the
// operator deciding whether to move the whole fleet needs to know which of the
// two answers they are looking at.
func TestAnInconclusiveCanarySaysWhatItCouldNotConfirm(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 1)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeCanary)
	st.StartXrayInstall(store.XrayInstall{
		NodeID: canary.ID, Version: "26.9.1", SHA256: strings.Repeat("a", 64), Phase: "ACTIVATING",
	}, time.Now())

	svc.HandleStatus(canary.ID, &chiralv1.XrayStatus{
		Version: "26.9.1",
		Phase:   chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INCONCLUSIVE,
		Message: "no reachable Xray API",
	})

	got := activeState(t, st)
	if got.State != store.UpgradeAwaitingPromote {
		t.Fatalf("state = %q", got.State)
	}
	if !strings.Contains(got.Message, "could not confirm") {
		t.Fatalf("the message hides the weaker claim: %q", got.Message)
	}
}

// A rollback blocks the fleet and tells somebody immediately.
func TestARolledBackCanaryBlocksAndAlerts(t *testing.T) {
	svc, st, alerts, canary := canaryFixture(t, 2)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeCanary)
	st.StartXrayInstall(store.XrayInstall{
		NodeID: canary.ID, Version: "26.9.1", SHA256: strings.Repeat("a", 64), Phase: "ACTIVATING",
	}, time.Now())

	svc.HandleStatus(canary.ID, &chiralv1.XrayStatus{
		Version:        "26.9.1",
		Phase:          chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK,
		Message:        "infra/conf: unknown transport protocol: xhttp",
		RunningVersion: "26.3.27",
	})

	got := activeState(t, st)
	if got.State != store.UpgradeBlocked {
		t.Fatalf("state = %q, want blocked", got.State)
	}
	if !strings.Contains(got.Message, "xhttp") {
		t.Fatalf("the diagnosis was lost: %q", got.Message)
	}
	events := alerts.all()
	if len(events) != 1 {
		t.Fatalf("%d alerts sent, want 1", len(events))
	}
	// After a rollback the node is serving again, and somebody woken by this
	// needs to know whether they are looking at an outage or at a decision.
	if !strings.Contains(events[0].Body, "serving again") {
		t.Errorf("the alert does not say the node recovered: %q", events[0].Body)
	}
	if !events[0].Online {
		t.Error("a rollback was announced as if the node were down")
	}
}

// A second node failing must not announce the block a second time.
func TestOnlyTheFirstFailureAlerts(t *testing.T) {
	svc, st, alerts, canary := canaryFixture(t, 2)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradePromoting)
	others, _ := st.ListNodes()
	for _, n := range others {
		st.StartXrayInstall(store.XrayInstall{
			NodeID: n.ID, Version: "26.9.1", SHA256: strings.Repeat("a", 64), Phase: "ACTIVATING",
		}, time.Now())
	}
	fail := &chiralv1.XrayStatus{
		Version: "26.9.1",
		Phase:   chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK,
		Message: "boom",
	}
	for _, n := range others {
		svc.HandleStatus(n.ID, fail)
	}
	if got := len(alerts.all()); got != 1 {
		t.Fatalf("%d alerts for one blocked upgrade, want 1", got)
	}
}

func TestPromoteOnlyWorksFromAwaitingPromote(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 1)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeCanary)

	if _, err := svc.Promote(context.Background()); err == nil {
		t.Fatal("an upgrade still on its canary was promoted")
	}
	if got := activeState(t, st); got.State != store.UpgradeCanary {
		t.Fatalf("state = %q; a refused promotion moved it anyway", got.State)
	}
}

// Retry is the fifth requirement: after the admin fixes the cause, one button
// takes the fleet to the target version again — through the canary, because the
// fix is a hypothesis until one node proves it.
func TestRetryGoesBackThroughTheCanary(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 1)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeBlocked)

	// InstallOn will fail (no network in tests), but the transition it makes
	// first is what matters: a retry must not jump straight to the fleet.
	svc.Retry(context.Background())
	got := activeState(t, st)
	if got.State == store.UpgradePromoting || got.State == store.UpgradeDone {
		t.Fatalf("state = %q; a retry skipped the canary", got.State)
	}
}

func TestOnlyABlockedUpgradeCanBeRetried(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 1)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeAwaitingPromote)
	if _, err := svc.Retry(context.Background()); err == nil {
		t.Fatal("an upgrade waiting to be promoted was retried")
	}
}

// Two fleet upgrades to different versions at once is a state with no correct
// meaning, and the database is what refuses it — a check in Go would have a
// window, and this is exactly the button two admins press together.
func TestOnlyOneUpgradeMayBeInFlight(t *testing.T) {
	_, st, _, canary := canaryFixture(t, 1)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeCanary)

	if _, err := st.StartUpgrade("26.10.1", canary.ID, time.Now()); err == nil {
		t.Fatal("a second upgrade started while one was in flight")
	}
	// A finished one frees the slot.
	u := activeState(t, st)
	if ok, err := st.SetUpgradeState(u.ID, u.State, store.UpgradeDone, "", time.Now()); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err := st.StartUpgrade("26.10.1", canary.ID, time.Now()); err != nil {
		t.Fatalf("a finished upgrade still held the slot: %v", err)
	}
}

// Without an escape hatch a blocked upgrade sits in the uniqueness index
// forever and no other upgrade can ever start.
func TestAbandonFreesTheSlot(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 1)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeBlocked)

	if _, err := svc.Abandon(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveUpgrade(); err == nil {
		t.Fatal("an abandoned upgrade is still active")
	}
	if _, err := st.StartUpgrade("26.10.1", canary.ID, time.Now()); err != nil {
		t.Fatalf("abandoning did not free the slot: %v", err)
	}
}

// A status for a version other than the one being rolled out must not move the
// fleet state machine.
func TestAStatusForAnotherVersionDoesNotAdvanceTheUpgrade(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 1)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeCanary)
	st.StartXrayInstall(store.XrayInstall{
		NodeID: canary.ID, Version: "26.10.1", SHA256: strings.Repeat("a", 64), Phase: "ACTIVATING",
	}, time.Now())

	svc.HandleStatus(canary.ID, &chiralv1.XrayStatus{
		Version: "26.10.1",
		Phase:   chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE,
	})
	if got := activeState(t, st); got.State != store.UpgradeCanary {
		t.Fatalf("state = %q; an unrelated version advanced the upgrade", got.State)
	}
}

// A non-canary node reporting success while the canary is still working must
// not stand in for the canary.
func TestAnotherNodeCannotStandInForTheCanary(t *testing.T) {
	svc, st, _, canary := canaryFixture(t, 2)
	openUpgrade(t, st, "26.9.1", canary.ID, store.UpgradeCanary)
	all, _ := st.ListNodes()
	var other store.Node
	for _, n := range all {
		if n.ID != canary.ID {
			other = n
			break
		}
	}
	st.StartXrayInstall(store.XrayInstall{
		NodeID: other.ID, Version: "26.9.1", SHA256: strings.Repeat("a", 64), Phase: "ACTIVATING",
	}, time.Now())

	svc.HandleStatus(other.ID, &chiralv1.XrayStatus{
		Version: "26.9.1",
		Phase:   chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE,
	})
	if got := activeState(t, st); got.State != store.UpgradeCanary {
		t.Fatalf("state = %q; a node that is not the canary promoted the upgrade", got.State)
	}
}

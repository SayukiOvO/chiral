package upgrade

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/alert"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// The fleet upgrade: one node first, then a person decides.
//
// Nothing here advances past the canary on its own. An upgrade that promoted
// itself would turn one bad release into a fleet-wide outage at machine speed,
// and the entire value of going first with one node is that somebody looks at
// the result before the rest follow.

// Announcer delivers a notification immediately, without the liveness
// debounce. Implemented by alert.Service.
type Announcer interface {
	Announce(ctx context.Context, e alert.Event)
}

// EnableAlerts wires rollback notifications.
func (s *Service) EnableAlerts(a Announcer) { s.alerts = a }

// StartCanary begins a fleet upgrade by installing on one node.
func (s *Service) StartCanary(ctx context.Context, version, canaryNodeID string) (store.Upgrade, error) {
	version = template.NormalizeVersion(version)
	if version == "" {
		return store.Upgrade{}, fmt.Errorf("a version is required")
	}
	if canaryNodeID == "" {
		return store.Upgrade{}, fmt.Errorf("a canary node is required")
	}
	u, err := s.st.StartUpgrade(version, canaryNodeID, time.Now())
	if err != nil {
		return store.Upgrade{}, err
	}
	s.logger.Info("kernel canary started", "version", version, "node", canaryNodeID)
	s.installAsync(u, canaryNodeID, "could not start the canary")
	return u, nil
}

// installAsync runs one install off the caller's goroutine, on the service's
// own context.
//
// The panel may have to fetch a 21 MB archive before it can name a version, and
// that cannot happen inside an HTTP request: the handler would block for
// minutes, and the browser giving up would cancel the download. Failures land
// on the upgrade as `blocked` with the reason, which is where the operator is
// already looking.
func (s *Service) installAsync(u store.Upgrade, nodeID, what string) {
	go func() {
		if _, err := s.InstallOn(s.base, nodeID, u.Version, true); err != nil {
			// Blocked rather than deleted. An operator pressed a button; that
			// it failed is a fact worth showing, and a row that vanishes reads
			// as a click that did not register.
			s.finish(u, u.State, store.UpgradeBlocked, what+": "+err.Error())
			s.logger.Error(what, "version", u.Version, "node", nodeID, "err", err)
		}
	}()
}

// Promote rolls the canary's version out to every other node.
//
// Only from awaiting_promote, and only by hand. A blocked upgrade cannot be
// promoted — Retry is the way out of that, and it goes back through the canary
// rather than around it.
func (s *Service) Promote(ctx context.Context) (store.Upgrade, error) {
	u, err := s.st.ActiveUpgrade()
	if err != nil {
		return store.Upgrade{}, fmt.Errorf("no kernel upgrade is in progress")
	}
	if u.State != store.UpgradeAwaitingPromote {
		return u, fmt.Errorf("this upgrade is %s, not waiting to be promoted", u.State)
	}
	moved, err := s.st.SetUpgradeState(u.ID, store.UpgradeAwaitingPromote, store.UpgradePromoting, "", time.Now())
	if err != nil {
		return u, err
	}
	if !moved {
		return u, fmt.Errorf("the upgrade changed state; reload and try again")
	}
	u.State = store.UpgradePromoting

	targets, err := s.promotionTargets(u)
	if err != nil {
		return u, err
	}
	if len(targets) == 0 {
		s.finish(u, store.UpgradePromoting, store.UpgradeDone, "only the canary needed upgrading")
		u.State = store.UpgradeDone
		return u, nil
	}
	// Off the request goroutine for the same reason as the canary, and because
	// this one is N nodes deep.
	go s.fanOut(u, targets)
	return u, nil
}

func (s *Service) fanOut(u store.Upgrade, targets []store.Node) {
	var failures []string
	for _, n := range targets {
		if _, err := s.InstallOn(s.base, n.ID, u.Version, true); err != nil {
			failures = append(failures, n.Name+": "+err.Error())
			s.logger.Warn("promoting a node failed", "node", n.Name, "err", err)
		}
	}
	if len(failures) > 0 {
		// Some nodes could not even be told. Blocked, not done: the fleet is
		// now split across two kernel versions, which is exactly the state
		// somebody must decide what to do about.
		s.finish(u, store.UpgradePromoting, store.UpgradeBlocked,
			fmt.Sprintf("%d of %d nodes could not be reached: %v", len(failures), len(targets), failures))
		return
	}
	s.logger.Info("kernel upgrade promoted", "version", u.Version, "nodes", len(targets))
}

// Retry re-runs the canary for a blocked upgrade.
//
// This is the last of the five requirements: after an admin has fixed whatever
// the rollback exposed, one button takes the fleet to the target version again.
// It goes back through the canary rather than straight to the fleet — the fix
// is a hypothesis until one node proves it.
func (s *Service) Retry(ctx context.Context) (store.Upgrade, error) {
	u, err := s.st.ActiveUpgrade()
	if err != nil {
		return store.Upgrade{}, fmt.Errorf("no kernel upgrade is in progress")
	}
	if u.State != store.UpgradeBlocked {
		return u, fmt.Errorf("this upgrade is %s; only a blocked one can be retried", u.State)
	}
	if u.CanaryNodeID == "" {
		return u, fmt.Errorf("this upgrade has no canary node to retry on")
	}
	moved, err := s.st.SetUpgradeState(u.ID, store.UpgradeBlocked, store.UpgradeCanary, "", time.Now())
	if err != nil || !moved {
		return u, fmt.Errorf("the upgrade changed state; reload and try again")
	}
	u.State, u.Message = store.UpgradeCanary, ""
	s.installAsync(u, u.CanaryNodeID, "could not restart the canary")
	return u, nil
}

// Abandon closes an in-flight upgrade without changing any node.
//
// The escape hatch for a fleet that has been deliberately left split, or an
// upgrade nobody intends to finish. Without it a blocked upgrade would sit in
// the uniqueness index forever, and no other upgrade could ever start.
func (s *Service) Abandon() (store.Upgrade, error) {
	u, err := s.st.ActiveUpgrade()
	if err != nil {
		return store.Upgrade{}, fmt.Errorf("no kernel upgrade is in progress")
	}
	moved, err := s.st.SetUpgradeState(u.ID, u.State, store.UpgradeDone, "abandoned by an operator", time.Now())
	if err != nil || !moved {
		return u, fmt.Errorf("the upgrade changed state; reload and try again")
	}
	u.State = store.UpgradeDone
	return u, nil
}

// promotionTargets is every node except the canary, ordered by name so a
// rollout is reproducible.
func (s *Service) promotionTargets(u store.Upgrade) ([]store.Node, error) {
	all, err := s.st.ListNodes()
	if err != nil {
		return nil, err
	}
	var out []store.Node
	for _, n := range all {
		if n.ID == u.CanaryNodeID {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// advance reacts to one node's terminal install phase.
//
// Called from HandleStatus, so the state machine moves on the agent's report
// rather than on a timer. A timer would have to guess how long a 21 MB download
// takes on an unknown uplink, and would call an upgrade failed for being slow.
func (s *Service) advance(nodeID string, st *chiralv1.XrayStatus) {
	u, err := s.st.ActiveUpgrade()
	if err != nil {
		return // no fleet upgrade; this was a one-off install
	}
	if template.NormalizeVersion(st.GetVersion()) != u.Version {
		return
	}
	if u.State == store.UpgradeBlocked {
		// Already stopped. A second node failing changes nothing about the
		// fleet's state, and the first failure is the one worth keeping as the
		// reason — later ones are usually the same cause seen again. The
		// per-node row still records this node's own outcome.
		return
	}
	phase := st.GetPhase()
	switch phase {
	case chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK,
		chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_FAILED:
		s.block(u, nodeID, st)
		return
	case chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE,
		chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INCONCLUSIVE:
	default:
		return // still in progress
	}

	switch u.State {
	case store.UpgradeCanary:
		if nodeID != u.CanaryNodeID {
			return
		}
		msg := "the canary is running " + u.Version
		if phase == chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_INCONCLUSIVE {
			// Surfaced, not swallowed. The canary is up but nothing confirmed
			// it is serving, and an operator deciding whether to move the whole
			// fleet needs to know which of those two they are looking at.
			msg = "the canary is running " + u.Version +
				", but the agent could not confirm it is serving: " + st.GetMessage()
		}
		s.finish(u, store.UpgradeCanary, store.UpgradeAwaitingPromote, msg)
		s.logger.Info("canary succeeded, awaiting promotion", "version", u.Version, "detail", msg)
	case store.UpgradePromoting:
		s.checkPromotionComplete(u)
	}
}

// checkPromotionComplete closes the upgrade once every node has settled.
func (s *Service) checkPromotionComplete(u store.Upgrade) {
	installs, err := s.st.XrayInstalls()
	if err != nil {
		s.logger.Error("reading installs during promotion failed", "err", err)
		return
	}
	targets, err := s.promotionTargets(u)
	if err != nil {
		return
	}
	byNode := make(map[string]store.XrayInstall, len(installs))
	for _, in := range installs {
		byNode[in.NodeID] = in
	}
	for _, n := range targets {
		in, ok := byNode[n.ID]
		if !ok || in.Version != u.Version {
			return // not yet reported
		}
		switch in.Phase {
		case "ACTIVE", "INCONCLUSIVE", "INSTALLED":
		default:
			return // still working, or failed — a failure blocks via advance
		}
	}
	s.finish(u, store.UpgradePromoting, store.UpgradeDone,
		fmt.Sprintf("%s is running on %d nodes", u.Version, len(targets)+1))
	s.logger.Info("kernel upgrade complete", "version", u.Version)
}

// block moves an upgrade to blocked and tells somebody.
func (s *Service) block(u store.Upgrade, nodeID string, st *chiralv1.XrayStatus) {
	name := nodeID
	if n, err := s.st.GetNode(nodeID); err == nil {
		name = n.Name
	}
	rolledBack := st.GetPhase() == chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ROLLED_BACK
	verb := "could not install"
	if rolledBack {
		verb = "rolled back from"
	}
	msg := fmt.Sprintf("%s %s %s: %s", name, verb, u.Version, st.GetMessage())
	if !s.finish(u, u.State, store.UpgradeBlocked, msg) {
		return // already blocked by an earlier node; do not alert twice
	}
	s.logger.Error("kernel upgrade blocked", "version", u.Version, "node", name, "detail", st.GetMessage())

	if s.alerts == nil {
		return
	}
	title := "Kernel upgrade blocked"
	body := msg
	if rolledBack {
		// Say the reassuring half explicitly. After a rollback the node is
		// serving again, and an operator woken by this needs to know whether
		// they are looking at an outage or at a decision.
		body += "\n\nThe node is serving again on " + st.GetRunningVersion() + "."
	}
	s.alerts.Announce(context.Background(), alert.Event{
		Title: title,
		Body:  body,
		Node:  name,
		// The node is up; this is not a liveness event, and a webhook consumer
		// keying off Online must not read it as one.
		Online: rolledBack,
		At:     time.Now().Unix(),
	})
}

// finish applies a state transition and reports whether it was this call that
// made it. Guarded on `from`, so a second node failing does not re-announce a
// block the first one already caused.
func (s *Service) finish(u store.Upgrade, from, to, message string) bool {
	if from == to {
		// A no-op transition still matches the guarded UPDATE and would report
		// success, which is how one blocked upgrade announces itself once per
		// failing node.
		return false
	}
	moved, err := s.st.SetUpgradeState(u.ID, from, to, message, time.Now())
	if err != nil {
		s.logger.Error("moving the upgrade state failed", "id", u.ID, "err", err)
		return false
	}
	return moved
}

package xray

import (
	"context"
	"fmt"
	"time"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

const chiralXrayRunning = chiralv1.XrayState_XRAY_STATE_RUNNING

// Switching the running kernel to a different binary, and putting it back when
// that goes wrong.
//
// The agent rolls back and never rolls forward. That asymmetry is the whole
// safety argument: a node that has just failed to start a new kernel is the
// least qualified thing in the system to decide what to try next, and two nodes
// making that decision independently is how a fleet ends up in states nobody
// designed. Going forward again is Core's call, on an operator's instruction.

const (
	// settleWindow is how long the new kernel must stay up before it is
	// credited with having started. Xray fails fast — a bad config or a missing
	// asset aborts within a second — so this is generous rather than tuned.
	settleWindow = 8 * time.Second
	// dataProbeTimeout bounds one end-to-end check: starting a throwaway
	// kernel, then a request through it and out to the internet.
	dataProbeTimeout = 40 * time.Second
)

// Outcome is the three-valued verdict on an activation.
//
// Three values and not two because "it did not fail" and "it works" are
// different claims, and only the second should let an operator roll an upgrade
// out to the rest of the fleet. A node that could not be tested at all cannot
// be reported as success without making every canary on it a rubber stamp.
type Outcome int

const (
	// OutcomeActive: a client got online through this node. Not "the process is
	// up", and not "the kernel answers its own API" — both of those are true of
	// a kernel whose transport layer is broken and whose subscribers are all
	// dark, which is precisely the failure an upgrade causes.
	OutcomeActive Outcome = iota
	// OutcomeInconclusive: running, but nothing could confirm traffic flows.
	OutcomeInconclusive
	// OutcomeFailed: did not start, or stopped answering something it used to
	// answer. The old binary has been put back.
	OutcomeFailed
)

func (o Outcome) String() string {
	switch o {
	case OutcomeActive:
		return "active"
	case OutcomeInconclusive:
		return "inconclusive"
	default:
		return "failed"
	}
}

// Activate restarts the kernel from bin, verifies a client can get online
// through it, and restores the previous binary if one cannot.
//
// The baseline probe before the swap is what keeps this from flapping, and it
// carries more weight now that the probe traverses the internet. If traffic was
// already not flowing under the old binary — a node with no users, a template
// with no xray-json client, an upstream that is down, a datacentre whose
// egress is blocked — then a new binary that also cannot get online has told us
// nothing, and rolling back would be a reaction to a condition the rollback
// cannot fix. Only a path that used to work and now does not counts as a
// failure.
//
// That comparison is what makes it safe to demand something as ambitious as a
// real fetch: every reason the fetch might fail that is not the new kernel's
// fault is already failing before the kernel is touched.
func (m *Manager) Activate(ctx context.Context, bin string) (Outcome, string, error) {
	prev := m.Binary()
	if prev == bin {
		return OutcomeInconclusive, "already running this binary", nil
	}
	// Establish what "working" looked like before touching anything, using the
	// binary that is serving right now.
	baseline := m.probe(ctx, prev)
	baselineOK := baseline == nil
	if ctx.Err() != nil {
		// The stream to Core died before we changed anything. Nothing has been
		// touched, so there is nothing to undo and nothing to report about the
		// release.
		return OutcomeInconclusive, "the panel connection dropped before the upgrade started", nil
	}

	m.setBinary(bin)
	if err := m.Restart(); err != nil {
		// Could not even start the process. Put the old one back immediately;
		// there is nothing to observe.
		m.setBinary(prev)
		msg := fmt.Sprintf("could not start %s: %v", bin, err)
		if rbErr := m.Restart(); rbErr != nil {
			// Both binaries failed to start, which means the problem is not the
			// binary. Say so rather than blaming the upgrade.
			return OutcomeFailed, msg + fmt.Sprintf("; and the previous binary would not start either: %v", rbErr), nil
		}
		return OutcomeFailed, msg, nil
	}

	// Give it the settle window, but notice a crash the moment it happens
	// rather than sleeping through it.
	if !m.staysUp(ctx, settleWindow) {
		msg := "the new kernel exited during startup"
		if tail := m.StderrTail(10); tail != "" {
			msg += ":\n" + tail
		}
		return m.rollback(prev, msg), msg, nil
	}

	if !baselineOK {
		// Up, and there was never a working path to compare against. Not a
		// success — say what was missing, because the fix is usually a
		// one-liner (bind a profile, add an xray-json template, enable a user)
		// and an operator who is only told "inconclusive" will not find it.
		return OutcomeInconclusive, fmt.Sprintf(
			"running %s, but nothing confirmed a client can get online through it: %v", bin, baseline), nil
	}
	if err := m.probe(ctx, bin); err != nil {
		// Losing the panel mid-check is not evidence against the release, and
		// saying it is would be a lie an operator then acts on. Still roll back:
		// the new kernel is unverified and, with the control plane gone, nobody
		// is watching it — the known-good binary is where an unattended node
		// belongs. What changes is the reason attached to it.
		if ctx.Err() != nil {
			msg := "the panel connection dropped while checking whether traffic flows; " +
				"the new kernel was never confirmed, so the previous one was put back"
			return m.rollback(prev, msg), msg, nil
		}
		msg := fmt.Sprintf("a client could get online through this node before the upgrade and cannot now: %v", err)
		if tail := m.StderrTail(10); tail != "" {
			msg += "\n" + tail
		}
		return m.rollback(prev, msg), msg, nil
	}
	return OutcomeActive, "a client got online through this node on " + bin, nil
}

// rollback restores prev and reports whether the node is serving again.
func (m *Manager) rollback(prev, why string) Outcome {
	m.logger.Error("rolling back the kernel", "to", prev, "reason", why)
	m.setBinary(prev)
	if err := m.Restart(); err != nil {
		m.logger.Error("rollback failed; the node is not serving", "err", err)
	}
	return OutcomeFailed
}

// staysUp reports whether the kernel is still running after d, polling so a
// crash is noticed promptly instead of at the end of the window.
func (m *Manager) staysUp(ctx context.Context, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if m.State() != chiralXrayRunning {
			return false
		}
		select {
		case <-ctx.Done():
			return m.State() == chiralXrayRunning
		case <-time.After(250 * time.Millisecond):
		}
	}
	return m.State() == chiralXrayRunning
}

// probe asks the only question worth asking: can a client get online through
// this node? Returns nil when traffic flowed, and an error when it did not or
// when there was nothing to test with — the caller distinguishes those two by
// comparing against the baseline.
//
// bin is which binary runs the throwaway client, and it is deliberately the one
// under test rather than whatever is convenient. A new kernel that cannot talk
// to itself is a new kernel two versions of which cannot talk to each other,
// which is the same outage seen from the subscriber's side.
func (m *Manager) probe(ctx context.Context, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, dataProbeTimeout)
	defer cancel()
	return m.DataPath(ctx, bin)
}

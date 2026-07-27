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
	// probeTimeout bounds one API probe.
	probeTimeout = 5 * time.Second
)

// Outcome is the three-valued verdict on an activation.
//
// Three values and not two because "it did not fail" and "it works" are
// different claims, and only the second should let an operator roll an upgrade
// out to the rest of the fleet. A node with no API inbound cannot be probed at
// all; reporting that as success would make every canary on such a node a
// rubber stamp.
type Outcome int

const (
	// OutcomeActive: running, and answering its own control plane.
	OutcomeActive Outcome = iota
	// OutcomeInconclusive: running, but there was nothing to ask.
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

// Activate restarts the kernel from bin, verifies it came up, and restores the
// previous binary if it did not.
//
// The baseline probe before the swap is what keeps this from flapping. If the
// API was already unreachable under the old binary — no API inbound, a
// misconfigured one, a node that has never had a working config — then a new
// binary that also does not answer has told us nothing, and rolling back would
// be a reaction to a condition the rollback cannot fix. Only a probe that used
// to succeed and now does not counts as a failure.
func (m *Manager) Activate(ctx context.Context, bin string) (Outcome, string, error) {
	prev := m.Binary()
	if prev == bin {
		return OutcomeInconclusive, "already running this binary", nil
	}
	// Establish what "working" looked like before touching anything.
	baselineOK := m.probe(ctx) == nil

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
		// Up, and there was never anything to ask. Not a success.
		return OutcomeInconclusive, "running, but the agent has no reachable Xray API to confirm it is serving", nil
	}
	if err := m.probe(ctx); err != nil {
		msg := fmt.Sprintf("the new kernel is up but stopped answering the API it answered before: %v", err)
		if tail := m.StderrTail(10); tail != "" {
			msg += "\n" + tail
		}
		return m.rollback(prev, msg), msg, nil
	}
	return OutcomeActive, "running and answering", nil
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

// probe asks the kernel something only a working kernel can answer. Returns nil
// when it answered, and an error when it did not or when there was nothing to
// ask — the caller distinguishes those two by comparing against the baseline.
func (m *Manager) probe(ctx context.Context) error {
	addr := m.APIAddress()
	if addr == "" {
		return fmt.Errorf("no API endpoint in the applied config")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	_, err := m.runAPI(ctx, addr, "statsquery")
	return err
}

// Package agentlock enforces one agent per state directory.
package agentlock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Acquire takes an exclusive lock on dir, failing immediately if another agent
// already holds it. The returned closer releases it.
//
// Two agents sharing a state directory was survivable while the directory held
// only an identity file and a config: the second one would supervise a second
// Xray, both would bind the same ports, and one would lose noisily. Runtime
// binary upgrades change the stakes — two supervisors swapping files under each
// other's running kernel, each rolling back what the other just installed, is a
// state no amount of care inside a single agent can recover from. Refusing to
// start is the only outcome that stays diagnosable.
//
// flock, not a pidfile: the kernel drops it when the process dies, so a
// crashed agent does not leave a lock nobody can clear. Advisory and per-fd, so
// a container restart with the same mount reacquires cleanly.
func Acquire(dir string) (func() error, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "agent.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another chiral-agent is already using %s: %w", dir, err)
	}
	// Best-effort breadcrumb for a human reading the directory; the lock is
	// the flock, not this content.
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() error {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return f.Close()
	}, nil
}

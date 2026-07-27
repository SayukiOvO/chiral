package xray

import (
	"strings"
	"sync"
)

// A bounded tail of whatever Xray last wrote to stderr.
//
// Exists because of what a failed upgrade looks like today: startLocked wires
// the child's stderr straight to the agent's own, so the kernel's own
// explanation of why it would not start goes to the container log and nowhere
// else. Core learns only "exit status 23".
//
// That is the thing an admin needs in order to fix whatever broke — and after
// an automatic rollback the node is serving again, so nobody has a reason to
// go and read that container's log. The reason has to travel with the failure.
//
// Bounded on purpose: a kernel that crash-loops writes without limit, and this
// is held in memory on a node whose whole job is elsewhere.
type stderrTail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newStderrTail(max int) *stderrTail {
	if max <= 0 {
		max = 40
	}
	return &stderrTail{max: max}
}

// Write satisfies io.Writer so it can be handed to exec.Cmd alongside the real
// stderr. Never returns an error: losing the tail must not kill the process it
// is watching.
func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		t.lines = append(t.lines, line)
	}
	if n := len(t.lines) - t.max; n > 0 {
		t.lines = append(t.lines[:0], t.lines[n:]...)
	}
	return len(p), nil
}

// Tail returns the most recent lines, newest last, joined.
func (t *stderrTail) Tail(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n <= 0 || n > len(t.lines) {
		n = len(t.lines)
	}
	return strings.Join(t.lines[len(t.lines)-n:], "\n")
}

// Reset clears the buffer, so the lines attributed to one start are not the
// previous process's.
func (t *stderrTail) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = t.lines[:0]
}

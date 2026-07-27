// Package xray manages the local Xray-core child process: config persistence,
// pre-apply `xray -test` validation, start/stop/restart, and crash events.
package xray

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// EventFunc receives asynchronous process events (crash/restart) for
// forwarding to Core. It must not block.
type EventFunc func(kind chiralv1.EventKind, message string)

type Manager struct {
	bin        string // xray binary path or name resolved via PATH
	configPath string
	logger     *slog.Logger
	onEvent    EventFunc

	mu            sync.Mutex
	cmd           *exec.Cmd
	state         chiralv1.XrayState
	configVersion int64
	// runningBin and runningVersion describe the process that is ACTUALLY
	// running, snapshotted at start. `bin` is where the next start would look;
	// once the binary can be swapped underneath, reporting from `bin` would
	// name the version that is precisely not running.
	runningBin     string
	runningVersion string
	// stderrTail keeps the kernel's own last words, so a failed start can be
	// explained to an admin who will never read this container's log.
	stderrTail *stderrTail
	// waitDone is closed by the current process's waiter goroutine once
	// cmd.Wait returns (i.e. the child is reaped).
	waitDone chan struct{}
	// generation invalidates the waiter goroutine of an older process so an
	// intentional stop/restart is not reported as a crash.
	generation int
}

func New(bin, dataDir string, logger *slog.Logger, onEvent EventFunc) *Manager {
	return &Manager{
		bin:        bin,
		configPath: filepath.Join(dataDir, "config.json"),
		logger:     logger,
		onEvent:    onEvent,
		state:      chiralv1.XrayState_XRAY_STATE_STOPPED,
		stderrTail: newStderrTail(40),
	}
}

// BinaryVersion reports the version of the binary at the CONFIGURED path,
// i.e. what the next start would run.
func (m *Manager) BinaryVersion() string { return versionOf(m.bin) }

// RunningVersion reports the version the LIVE process was started from.
//
// The two differ for exactly as long as a swap has happened but no restart
// has, and that window is where an upgrade is judged — so telling Core the
// configured one would mean reporting the version that is precisely not
// serving traffic.
func (m *Manager) RunningVersion() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != chiralv1.XrayState_XRAY_STATE_RUNNING {
		return ""
	}
	return m.runningVersion
}

// apiBin is the binary whose CLI should talk to the running kernel: the one it
// was started from, falling back to the configured path when nothing runs.
func (m *Manager) apiBin() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runningBin != "" {
		return m.runningBin
	}
	return m.bin
}

// StderrTail returns the running kernel's most recent stderr lines.
func (m *Manager) StderrTail(lines int) string { return m.stderrTail.Tail(lines) }

// versionOf asks a binary what it is. Canonical form, no leading "v" — the
// binary prints "Xray 26.3.27" while GitHub tags say "v26.7.11", and every
// comparison in the upgrade path depends on those never being mixed.
func versionOf(bin string) string {
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		return ""
	}
	// First line looks like: "Xray 25.1.1 (Xray, Penetrates Everything.) ...".
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return strings.TrimPrefix(fields[1], "v")
	}
	return strings.TrimSpace(line)
}

func (m *Manager) State() chiralv1.XrayState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Manager) ConfigVersion() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.configVersion
}

// Apply validates the pushed config with `xray -test`, persists it, and
// (re)starts Xray-core on it. On any error the previous process keeps
// running with its old config.
//
// Two guards keep re-pushes (reconnect, heartbeat reconciliation) harmless:
// stale versions are rejected, and identical content on an already-running
// process is acked without a disruptive restart.
func (m *Manager) Apply(version int64, configJSON []byte) error {
	m.mu.Lock()
	current := m.configVersion
	running := m.state == chiralv1.XrayState_XRAY_STATE_RUNNING
	m.mu.Unlock()
	if current != 0 && version < current {
		return fmt.Errorf("stale config version %d (already at %d)", version, current)
	}
	if running {
		if onDisk, err := os.ReadFile(m.configPath); err == nil && bytesEqual(onDisk, configJSON) {
			m.mu.Lock()
			m.configVersion = version
			m.mu.Unlock()
			m.logger.Info("config unchanged; skipping restart", "version", version)
			return nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
		return err
	}
	tmp := m.configPath + ".next"
	if err := os.WriteFile(tmp, configJSON, 0o600); err != nil {
		return err
	}
	defer os.Remove(tmp)

	if _, err := exec.LookPath(m.bin); err != nil {
		return fmt.Errorf("xray binary %q not found", m.bin)
	}
	// -format json is required: Xray infers format from the file extension,
	// and the temp file is not named *.json.
	if out, err := exec.Command(m.bin, "-test", "-c", tmp, "-format", "json").CombinedOutput(); err != nil {
		return fmt.Errorf("xray -test failed: %s", firstLines(string(out), 5))
	}
	if err := os.Rename(tmp, m.configPath); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	if err := m.startLocked(); err != nil {
		m.state = chiralv1.XrayState_XRAY_STATE_ERROR
		return err
	}
	m.configVersion = version
	return nil
}

// Start launches Xray-core on the current persisted config.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == chiralv1.XrayState_XRAY_STATE_RUNNING {
		return nil
	}
	return m.startLocked()
}

func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
}

func (m *Manager) Restart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopLocked()
	return m.startLocked()
}

func (m *Manager) startLocked() error {
	if _, err := os.Stat(m.configPath); err != nil {
		return fmt.Errorf("no config to run: %w", err)
	}
	bin := m.bin
	cmd := exec.Command(bin, "run", "-c", m.configPath, "-format", "json")
	cmd.Stdout = os.Stdout

	// Tee stderr: the container log still gets everything, and the tail keeps
	// enough to explain a failure to someone who is not reading that log.
	//
	// The pipe is made here rather than handing exec an io.Writer, and the
	// difference is not cosmetic. Given a plain writer, exec builds the pipe
	// itself and makes Wait() block until every writer end is closed — which
	// includes any process that inherited the descriptor. One orphaned
	// grandchild would then hang Wait() forever, and stopLocked waits on Wait()
	// while holding m.mu, so the whole manager would deadlock. Passing an
	// *os.File keeps Wait() waiting on the process and nothing else; the copier
	// below is ours to outlive.
	m.stderrTail.Reset()
	pr, pw, err := os.Pipe()
	if err != nil {
		m.state = chiralv1.XrayState_XRAY_STATE_ERROR
		return err
	}
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		m.state = chiralv1.XrayState_XRAY_STATE_ERROR
		return err
	}
	// The child holds its own descriptor now; drop ours so the copier sees EOF
	// once every writer is gone.
	pw.Close()
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		io.Copy(io.MultiWriter(os.Stderr, m.stderrTail), pr)
		pr.Close()
	}()
	m.cmd = cmd
	// Snapshot what is now running, under the lock the caller already holds.
	m.runningBin = bin
	m.runningVersion = versionOf(bin)
	m.state = chiralv1.XrayState_XRAY_STATE_RUNNING
	m.generation++
	gen := m.generation
	waitDone := make(chan struct{})
	m.waitDone = waitDone
	m.logger.Info("xray-core started", "pid", cmd.Process.Pid)

	go func() {
		err := cmd.Wait()
		// Close before taking the lock: stopLocked waits on this channel
		// while holding the mutex.
		close(waitDone)
		m.mu.Lock()
		// Only the waiter of the current process may report a crash; stale
		// waiters belong to intentionally stopped processes.
		if m.generation != gen {
			m.mu.Unlock()
			return
		}
		m.cmd = nil
		m.runningBin, m.runningVersion = "", ""
		m.state = chiralv1.XrayState_XRAY_STATE_ERROR
		m.mu.Unlock()
		msg := "xray-core exited unexpectedly"
		if err != nil {
			msg = fmt.Sprintf("xray-core exited unexpectedly: %v", err)
		}
		// Carry the kernel's own last words. "exit status 23" tells an operator
		// nothing they can act on; the lines above it are the whole diagnosis,
		// and after an automatic rollback nobody has a reason to go and read
		// this node's container log to find them.
		//
		// Wait() can return before the copier has drained the pipe, so give it a
		// moment — but bounded, since a descendant holding the write end would
		// otherwise keep the crash report from ever being sent. Losing the tail
		// is bad; losing the notification is worse.
		select {
		case <-copyDone:
		case <-time.After(2 * time.Second):
		}
		if tail := m.stderrTail.Tail(10); tail != "" {
			msg += "\n" + tail
		}
		m.logger.Error(msg)
		if m.onEvent != nil {
			m.onEvent(chiralv1.EventKind_EVENT_KIND_XRAY_CRASHED, msg)
		}
	}()
	return nil
}

func (m *Manager) stopLocked() {
	if m.cmd == nil || m.cmd.Process == nil {
		m.state = chiralv1.XrayState_XRAY_STATE_STOPPED
		return
	}
	// Bump generation first so the waiter treats this exit as intentional.
	m.generation++
	m.cmd.Process.Signal(os.Interrupt)
	select {
	case <-m.waitDone:
	case <-time.After(5 * time.Second):
		m.cmd.Process.Kill()
		<-m.waitDone
	}
	m.cmd = nil
	m.runningBin, m.runningVersion = "", ""
	m.state = chiralv1.XrayState_XRAY_STATE_STOPPED
	m.logger.Info("xray-core stopped")
}

// HasPersistedConfig reports whether a config.json from a previous run is on
// disk, so the agent can start Xray-core before Core is even reachable.
func (m *Manager) HasPersistedConfig() bool {
	_, err := os.Stat(m.configPath)
	return err == nil
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

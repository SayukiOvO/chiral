// Package xray manages the local Xray-core child process: config persistence,
// pre-apply `xray -test` validation, start/stop/restart, and crash events.
package xray

import (
	"fmt"
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
	}
}

// BinaryVersion returns the Xray-core version string, or "" when the binary
// is unavailable.
func (m *Manager) BinaryVersion() string {
	out, err := exec.Command(m.bin, "version").Output()
	if err != nil {
		return ""
	}
	// First line looks like: "Xray 25.1.1 (Xray, Penetrates Everything.) ...".
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		return fields[1]
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
	cmd := exec.Command(m.bin, "run", "-c", m.configPath, "-format", "json")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		m.state = chiralv1.XrayState_XRAY_STATE_ERROR
		return err
	}
	m.cmd = cmd
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
		m.state = chiralv1.XrayState_XRAY_STATE_ERROR
		m.mu.Unlock()
		msg := "xray-core exited unexpectedly"
		if err != nil {
			msg = fmt.Sprintf("xray-core exited unexpectedly: %v", err)
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

package xray

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// fakeKernel writes a stand-in for the xray binary: it answers `version` like
// the real one, and on `run` prints the given lines to stderr and exits.
//
// A script rather than a real kernel because the behaviour under test is what
// the agent does with a kernel that refuses to start, and a real one can only
// be made to fail in ways `xray -test` would have caught first.
func fakeKernel(t *testing.T, version string, stderrLines []string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-xray")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo \"Xray " + version + " (Xray, Penetrates Everything.)\"; exit 0; fi\n"
	for _, l := range stderrLines {
		script += "echo '" + l + "' >&2\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func managerWith(t *testing.T, bin string, onEvent EventFunc) *Manager {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return New(bin, dir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1})), onEvent)
}

// The whole reason the tail exists: after an automatic rollback the node is
// serving again and nobody has a reason to open its container log, so the
// kernel's explanation has to travel with the failure.
func TestACrashCarriesTheKernelsOwnDiagnostics(t *testing.T) {
	bin := fakeKernel(t, "26.7.11", []string{
		"Failed to start: main: failed to load config files",
		"infra/conf: unknown transport protocol: xhttp",
	}, 23)

	events := make(chan string, 4)
	m := managerWith(t, bin, func(kind chiralv1.EventKind, msg string) {
		if kind == chiralv1.EventKind_EVENT_KIND_XRAY_CRASHED {
			events <- msg
		}
	})
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	select {
	case msg := <-events:
		if !strings.Contains(msg, "unknown transport protocol: xhttp") {
			t.Fatalf("the crash event does not say why:\n%s", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no crash event")
	}
}

// The distinction the upgrade path is built on: after a swap, "installed" moves
// and "running" does not, until a restart. Collapsing them would report the
// upgrade as landed the moment the file was written.
func TestRunningVersionTracksTheProcessNotTheConfiguredPath(t *testing.T) {
	// A kernel that stays up, so there is a running process to ask about.
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-xray")
	if err := os.WriteFile(bin, []byte(
		"#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray 26.7.11 (Xray, Penetrates Everything.)'; exit 0; fi\nexec sleep 300\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	m := managerWith(t, bin, nil)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	if got := m.RunningVersion(); got != "26.7.11" {
		t.Fatalf("RunningVersion() = %q, want 26.7.11", got)
	}

	// Swap the binary under the running process, as an upgrade would.
	if err := os.WriteFile(bin, []byte(
		"#!/bin/sh\nif [ \"$1\" = version ]; then echo 'Xray 26.9.1 (Xray, Penetrates Everything.)'; exit 0; fi\nexec sleep 300\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := m.BinaryVersion(); got != "26.9.1" {
		t.Fatalf("BinaryVersion() = %q after the swap, want 26.9.1", got)
	}
	if got := m.RunningVersion(); got != "26.7.11" {
		t.Fatalf("RunningVersion() = %q after a swap with no restart; the old process is still serving", got)
	}

	if err := m.Restart(); err != nil {
		t.Fatal(err)
	}
	if got := m.RunningVersion(); got != "26.9.1" {
		t.Fatalf("RunningVersion() = %q after the restart, want 26.9.1", got)
	}
}

// Teeing stderr introduced a way for Stop() to hang forever, and this is the
// shape of it: hand exec a plain io.Writer and it builds the pipe itself, then
// makes Wait() block until every writer end closes — including one inherited by
// a process that outlives the kernel. stopLocked waits on Wait() while holding
// m.mu, so that hang takes the whole manager with it.
//
// The kernel here leaves a descendant holding the descriptor. Stop must still
// return.
func TestStopIsNotHeldHostageByAnOrphanHoldingStderr(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-xray")
	if err := os.WriteFile(bin, []byte(
		"#!/bin/sh\n"+
			"if [ \"$1\" = version ]; then echo 'Xray 26.7.11 (Xray, Penetrates Everything.)'; exit 0; fi\n"+
			// Inherits the stderr pipe and survives the kill below. stdout is
			// redirected away so it does not also hold the test binary's own
			// stdout open, which would strand `go test` rather than exercise
			// anything.
			"sleep 30 >/dev/null &\n"+
			"exec sleep 30\n",
	), 0o755); err != nil {
		t.Fatal(err)
	}
	m := managerWith(t, bin, nil)
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}

	stopped := make(chan struct{})
	go func() {
		m.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		t.Fatal("Stop() blocked on a descendant holding the stderr pipe")
	}
	if got := m.State(); got != chiralv1.XrayState_XRAY_STATE_STOPPED {
		t.Errorf("state = %v after Stop, want STOPPED", got)
	}
}

// A stopped kernel has no running version, and saying otherwise would let a
// dead node look upgraded.
func TestRunningVersionIsEmptyWhenNothingRuns(t *testing.T) {
	bin := fakeKernel(t, "26.7.11", nil, 0)
	m := managerWith(t, bin, nil)
	if got := m.RunningVersion(); got != "" {
		t.Fatalf("RunningVersion() = %q before any start", got)
	}
	if got := m.BinaryVersion(); got != "26.7.11" {
		t.Fatalf("BinaryVersion() = %q, want 26.7.11", got)
	}
}

package agentlock

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestASecondAcquireIsRefused(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// flock is per-fd, not per-process, so a second Acquire in this same
	// process exercises the same path a second agent would take.
	if _, err := Acquire(dir); err == nil {
		t.Fatal("a second agent was allowed to lock the same state directory")
	}
}

func TestReleasingAllowsTheNextAgentIn(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("a released directory stayed locked: %v", err)
	}
	release2()
}

func TestDistinctDirectoriesDoNotContend(t *testing.T) {
	a, err := Acquire(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := Acquire(t.TempDir())
	if err != nil {
		t.Fatalf("two agents on separate directories contended: %v", err)
	}
	defer b()
}

// The property a pidfile would not have: a lock held by a process that dies
// without cleanup must not outlive it. This is the failure that matters —
// an OOM-killed agent whose container restarts must not be locked out of its
// own state directory forever.
func TestAKilledHolderDoesNotStrandTheLock(t *testing.T) {
	dir := t.TempDir()
	helper := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLock")
	helper.Env = append(os.Environ(), "CHIRAL_LOCK_HELPER_DIR="+dir)
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait for it to report that it holds the lock.
	buf := make([]byte, 8)
	if _, err := stdout.Read(buf); err != nil {
		helper.Process.Kill()
		t.Fatalf("helper never took the lock: %v", err)
	}
	if _, err := Acquire(dir); err == nil {
		helper.Process.Kill()
		t.Fatal("acquired a directory the helper process was holding")
	}

	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()

	release, err := Acquire(dir)
	if err != nil {
		t.Fatalf("the lock survived its holder's death: %v", err)
	}
	release()

	if _, err := os.Stat(filepath.Join(dir, "agent.lock")); err != nil {
		t.Errorf("lock file missing: %v", err)
	}
}

// TestHelperHoldsLock is not a test; it is the subprocess body for the case
// above, selected by the environment variable.
func TestHelperHoldsLock(t *testing.T) {
	dir := os.Getenv("CHIRAL_LOCK_HELPER_DIR")
	if dir == "" {
		t.Skip("helper process only")
	}
	if _, err := Acquire(dir); err != nil {
		t.Fatal(err)
	}
	os.Stdout.WriteString("locked\n")
	select {} // killed by the parent
}

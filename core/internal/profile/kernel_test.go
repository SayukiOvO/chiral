package profile

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/kernel"
	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// A kernel that accepts every config and reports a version. The point of these
// tests is which binary gets chosen, not what it says about the config.
func stubKernel(t *testing.T, dir, version string) {
	t.Helper()
	vdir := filepath.Join(dir, version)
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then echo \"Xray " + version + " (Xray, Penetrates Everything.)\"; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(vdir, kernel.BinaryName), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func kernelFixture(t *testing.T, baked string) (*Service, *store.Store, *kernel.Registry) {
	t.Helper()
	box, err := secret.NewBox("profile-test-key-0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"), box)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	dir := t.TempDir()
	bakedDir := t.TempDir()
	stubKernel(t, bakedDir, baked)
	reg := kernel.New(dir, template.Xray{Bin: filepath.Join(bakedDir, baked, kernel.BinaryName)})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	push := &fakePusher{}
	return NewService(st, reg, push, push, user.NewService(st, logger), logger), st, reg
}

// The construct the whole milestone rests on: validation follows the node, not
// the panel. A node running something the panel also has must be judged by that
// build, or every feature the upgrade was for gets rejected as unknown.
func TestPreviewValidatesWithTheNodesOwnKernel(t *testing.T) {
	svc, st, reg := kernelFixture(t, "26.3.27")
	stubKernel(t, reg.Dir(), "26.9.1")
	reg.Rescan()

	n, err := st.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetXrayVersions(n.ID, "26.9.1", "26.9.1"); err != nil {
		t.Fatal(err)
	}

	pv, err := svc.Preview(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pv.KernelVersion != "26.9.1" {
		t.Fatalf("validated with %q, want the node's own 26.9.1", pv.KernelVersion)
	}
	if !pv.KernelExact {
		t.Errorf("exact match reported as inexact: %s", pv.KernelNote)
	}
}

// The window that matters: the binary has been swapped but the process has not
// restarted. A config pushed now is applied by restarting INTO the new build,
// so the installed version is the one that will judge it — using the running
// one would validate against the build about to be replaced.
func TestValidationFollowsTheInstalledVersionNotTheRunningOne(t *testing.T) {
	svc, st, reg := kernelFixture(t, "26.3.27")
	stubKernel(t, reg.Dir(), "26.9.1")
	reg.Rescan()

	n, err := st.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	// Swapped to 26.9.1 on disk, still running 26.3.27.
	if _, err := st.SetXrayVersions(n.ID, "26.3.27", "26.9.1"); err != nil {
		t.Fatal(err)
	}

	pv, err := svc.Preview(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pv.KernelVersion != "26.9.1" {
		t.Fatalf("validated with %q; a config pushed now restarts into 26.9.1", pv.KernelVersion)
	}
}

// A node the panel has not caught up with must still be previewable, and must
// not be told its config was verified against a build nobody has.
func TestAPreviewForAnUnknownKernelSaysSo(t *testing.T) {
	svc, st, _ := kernelFixture(t, "26.3.27")

	n, err := st.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetXrayVersions(n.ID, "27.1.1", "27.1.1"); err != nil {
		t.Fatal(err)
	}

	pv, err := svc.Preview(context.Background(), n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pv.KernelExact {
		t.Fatal("a kernel the panel does not have was reported as an exact match")
	}
	if pv.KernelNote == "" {
		t.Fatal("an inexact validation explained nothing to the operator")
	}
	if !pv.Tested {
		t.Error("the preview was not validated at all; the fallback exists to avoid that")
	}
}

// Refusing here would strand every node mid-upgrade — the node's own
// `xray -test`, which runs the right binary, is the actual gate.
func TestApplyIsNotRefusedJustBecauseTheKernelIsUnknown(t *testing.T) {
	svc, st, _ := kernelFixture(t, "26.3.27")

	n, err := st.CreateNode("tokyo-1", "join-hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetXrayVersions(n.ID, "27.1.1", "27.1.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(context.Background(), n.ID); err != nil {
		t.Fatalf("Apply refused a node whose kernel the panel does not have: %v", err)
	}
}

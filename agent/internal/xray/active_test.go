package xray

import (
	"os"
	"path/filepath"
	"testing"
)

// installVersion puts a working stub at <kernels>/<version>/xray.
func installVersion(t *testing.T, in *Installer, version, reports string) {
	t.Helper()
	dir := in.VersionDir(version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nif [ \"$1\" = version ]; then echo \"Xray " + reports + " (Xray, Penetrates Everything.)\"; exit 0; fi\nexec sleep 60\n"
	if err := os.WriteFile(filepath.Join(dir, "xray"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The failure this exists to prevent: a container restart after an upgrade puts
// the node back on the image's kernel while Core still believes the new one is
// installed — a downgrade nobody asked for and nobody is told about.
func TestARecordedVersionSurvivesARestart(t *testing.T) {
	in, _, _ := installerFixture(t, "26.3.27")
	installVersion(t, in, "26.9.1", "26.9.1")
	if err := in.SetActive("26.9.1"); err != nil {
		t.Fatal(err)
	}
	got := in.ActiveBinary()
	if got != in.BinaryFor("26.9.1") {
		t.Fatalf("ActiveBinary() = %q, want the installed 26.9.1", got)
	}
}

// The image's binary is the floor. With no record, that is what runs.
func TestNoRecordMeansTheBakedBinary(t *testing.T) {
	in, _, _ := installerFixture(t, "26.3.27")
	if got := in.ActiveBinary(); got != "" {
		t.Fatalf("ActiveBinary() = %q with nothing recorded", got)
	}
}

// A pointer to something absent is worse than no pointer: it would leave the
// agent with a path it cannot execute.
func TestARecordForAVersionThatIsGoneFallsBack(t *testing.T) {
	in, _, _ := installerFixture(t, "26.3.27")
	installVersion(t, in, "26.9.1", "26.9.1")
	in.SetActive("26.9.1")
	os.RemoveAll(in.VersionDir("26.9.1"))

	if got := in.ActiveBinary(); got != "" {
		t.Fatalf("ActiveBinary() = %q for a version that is no longer installed", got)
	}
}

// And a pointer to something mislabelled is worse still: it would silently run
// a build nobody named, which is the whole thing per-version validation exists
// to prevent.
func TestARecordWhoseBinaryLiesIsRejected(t *testing.T) {
	in, _, _ := installerFixture(t, "26.3.27")
	installVersion(t, in, "26.9.1", "26.3.27") // directory says one thing, binary another
	in.SetActive("26.9.1")

	if got := in.ActiveBinary(); got != "" {
		t.Fatalf("ActiveBinary() = %q for a mislabelled build", got)
	}
}

func TestAnEmptyRecordIsIgnored(t *testing.T) {
	in, _, _ := installerFixture(t, "26.3.27")
	os.MkdirAll(in.Dir(), 0o755)
	os.WriteFile(filepath.Join(in.Dir(), activeFile), []byte("\n"), 0o644)
	if got := in.ActiveBinary(); got != "" {
		t.Fatalf("ActiveBinary() = %q for an empty record", got)
	}
}

package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/template"
)

// fakeKernel writes a binary that answers `version` and nothing else. Enough
// for the registry, whose entire job is choosing WHICH binary — running one is
// somebody else's problem.
func fakeKernel(t *testing.T, dir, version string) string {
	t.Helper()
	vdir := filepath.Join(dir, version)
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(vdir, BinaryName)
	script := "#!/bin/sh\necho \"Xray " + version + " (Xray, Penetrates Everything.)\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func bakedKernel(t *testing.T, version string) template.Xray {
	t.Helper()
	return template.Xray{Bin: fakeKernel(t, t.TempDir(), version)}
}

func TestAnInstalledVersionResolvesExactly(t *testing.T) {
	dir := t.TempDir()
	fakeKernel(t, dir, "26.9.1")
	r := New(dir, bakedKernel(t, "26.3.27"))

	res := r.For("26.9.1")
	if !res.Exact {
		t.Fatalf("26.9.1 is installed but resolved inexactly: %s", res.Describe())
	}
	if res.Version != "26.9.1" {
		t.Errorf("Version = %q, want 26.9.1", res.Version)
	}
}

// The failure this whole package exists to prevent, in both directions: judging
// an upgraded node's config with the old binary rejects every new feature, and
// judging a not-yet-upgraded node's with the new one accepts a config that will
// not start. Falling back is allowed; calling it exact is not.
func TestAnUnknownVersionFallsBackButNeverClaimsToBeExact(t *testing.T) {
	r := New(t.TempDir(), bakedKernel(t, "26.3.27"))

	res := r.For("26.9.1")
	if res.Exact {
		t.Fatal("a version the panel does not have was reported as an exact match")
	}
	if res.Version != "26.3.27" {
		t.Errorf("fell back to %q, want the baked 26.3.27", res.Version)
	}
	if res.Want != "26.9.1" {
		t.Errorf("Want = %q, want 26.9.1", res.Want)
	}
	if res.Describe() == "" {
		t.Error("an inexact resolution explains nothing")
	}
}

// A node that has never connected has no version, and refusing to preview its
// config would make the panel useless for the one node an operator is trying to
// bring up.
func TestAnEmptyVersionResolvesInexactlyRatherThanFailing(t *testing.T) {
	r := New(t.TempDir(), bakedKernel(t, "26.3.27"))
	res := r.For("")
	if res.Exact {
		t.Error("an unknown version was called exact")
	}
	if !res.Xray.Available() {
		t.Error("no usable binary for a node with no reported version")
	}
}

// The baked binary is a real installed version, not a lesser one.
func TestTheBakedVersionCountsAsExact(t *testing.T) {
	r := New(t.TempDir(), bakedKernel(t, "26.3.27"))
	if res := r.For("26.3.27"); !res.Exact {
		t.Fatalf("the baked version resolved inexactly: %s", res.Describe())
	}
	if !r.Has("26.3.27") {
		t.Error("Has() denies the baked version")
	}
}

// The tag namespace leaks in from GitHub; a "v" must not create a second,
// separate entry for the same build.
func TestTheTagPrefixIsNormalisedAwayOnBothSides(t *testing.T) {
	dir := t.TempDir()
	fakeKernel(t, dir, "v26.9.1") // a directory named with the tag form
	r := New(dir, bakedKernel(t, "26.3.27"))

	if res := r.For("26.9.1"); !res.Exact {
		t.Errorf("a v-prefixed directory did not answer the bare version: %s", res.Describe())
	}
	if res := r.For("v26.9.1"); !res.Exact {
		t.Errorf("a v-prefixed query did not resolve: %s", res.Describe())
	}
}

// A directory with no binary in it, or one that is not executable, is not a
// version the panel can validate for — and claiming it is would produce a
// confident answer from a binary that cannot run.
func TestIncompleteInstallsAreNotOffered(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "26.9.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	notExec := filepath.Join(dir, "26.8.1")
	if err := os.MkdirAll(notExec, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notExec, BinaryName), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New(dir, bakedKernel(t, "26.3.27"))

	if r.Has("26.9.1") {
		t.Error("a directory with no binary was offered as installed")
	}
	if r.Has("26.8.1") {
		t.Error("a non-executable file was offered as installed")
	}
}

// The upgrade path installs while Core runs; a version fetched for a node has
// to become validatable without a restart.
func TestRescanPicksUpAFreshInstall(t *testing.T) {
	dir := t.TempDir()
	r := New(dir, bakedKernel(t, "26.3.27"))
	if r.Has("26.9.1") {
		t.Fatal("26.9.1 is not installed yet")
	}
	fakeKernel(t, dir, "26.9.1")
	r.Rescan()
	if !r.Has("26.9.1") {
		t.Fatal("a freshly installed version is still invisible after Rescan")
	}
}

func TestVersionsAreOrderedOldestFirstAndIncludeTheBaked(t *testing.T) {
	dir := t.TempDir()
	fakeKernel(t, dir, "26.10.1")
	fakeKernel(t, dir, "26.9.1")
	r := New(dir, bakedKernel(t, "26.3.27"))

	got := r.Versions()
	want := []string{"26.3.27", "26.9.1", "26.10.1"}
	if len(got) != len(want) {
		t.Fatalf("Versions() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Versions() = %v, want %v", got, want)
		}
	}
}

// A missing directory is the normal state of a panel that has never upgraded
// anything, not a configuration error.
func TestAMissingDirectoryIsNotAnError(t *testing.T) {
	r := New(filepath.Join(t.TempDir(), "does-not-exist"), bakedKernel(t, "26.3.27"))
	if res := r.For("26.3.27"); !res.Exact {
		t.Fatal("the baked binary stopped working because the install dir is absent")
	}
}

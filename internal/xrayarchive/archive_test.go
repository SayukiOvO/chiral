package xrayarchive

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildZip writes a zip with the given entries, mirroring the real archive's
// flat shape.
func buildZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestVerifyAcceptsTheMatchingDigest(t *testing.T) {
	path := buildZip(t, map[string]string{"xray": "binary"})
	if err := Verify(path, sha256Of(t, path)); err != nil {
		t.Fatal(err)
	}
	// Case is not meaningful in hex, and a mixed-case digest from some other
	// tool must not read as a mismatch.
	if err := Verify(path, strings.ToUpper(sha256Of(t, path))); err != nil {
		t.Fatalf("an upper-case digest was rejected: %v", err)
	}
}

func TestVerifyRejectsAMismatch(t *testing.T) {
	path := buildZip(t, map[string]string{"xray": "binary"})
	wrong := strings.Repeat("a", 64)
	if err := Verify(path, wrong); err == nil {
		t.Fatal("a mismatched archive verified")
	}
}

// An empty or short digest must be a refusal, not a skip. "No checksum to
// compare" silently becoming "nothing to check" is how an unverified binary
// ends up running as root on every node.
func TestVerifyRefusesAnAbsentDigest(t *testing.T) {
	path := buildZip(t, map[string]string{"xray": "binary"})
	for _, d := range []string{"", "abc", strings.Repeat("a", 63)} {
		if err := Verify(path, d); err == nil {
			t.Errorf("Verify accepted the digest %q", d)
		}
	}
}

func TestUnpackTakesTheBinaryAndTheGeoDatabases(t *testing.T) {
	path := buildZip(t, map[string]string{
		"xray":        "the binary",
		"geoip.dat":   "ip data",
		"geosite.dat": "site data",
		"README.md":   "docs",
		"LICENSE":     "mpl",
	})
	dest := t.TempDir()
	bin, err := Unpack(path, dest)
	if err != nil {
		t.Fatal(err)
	}
	if bin != filepath.Join(dest, "xray") {
		t.Errorf("binary landed at %q", bin)
	}
	for _, name := range []string{"xray", "geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("%s was not extracted: %v", name, err)
		}
	}
	// Without these, every routing rule using geosite:/geoip: fails to load —
	// which is most real configs — and it fails at `-test` time, so the node
	// would refuse a config that is perfectly correct.
	for _, name := range []string{"README.md", "LICENSE"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err == nil {
			t.Errorf("%s was extracted; the whitelist should have dropped it", name)
		}
	}
}

func TestUnpackedBinaryIsExecutable(t *testing.T) {
	path := buildZip(t, map[string]string{"xray": "the binary"})
	bin, err := Unpack(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&0o111 == 0 {
		t.Fatalf("mode is %v; the binary is not executable", st.Mode())
	}
}

// A whitelist is what keeps a hostile archive from writing whatever it likes,
// and an archive downloaded from the internet is exactly the thing whose
// contents you do not get to trust. Both halves matter: the traversal is
// flattened AND the flattened name still has to be one we asked for.
func TestUnpackIgnoresPathTraversal(t *testing.T) {
	outside := t.TempDir()
	dest := filepath.Join(outside, "dest")
	path := buildZip(t, map[string]string{
		"xray":                   "the binary",
		"../../../etc/passwd":    "root::0:0",
		"../sibling/geoip.dat":   "traversed",
		"nested/dir/geosite.dat": "also traversed",
	})
	if _, err := Unpack(path, dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "passwd")); err == nil {
		t.Fatal("an entry escaped the destination directory")
	}
	if _, err := os.Stat(filepath.Join(outside, "sibling")); err == nil {
		t.Fatal("a traversing entry created a sibling directory")
	}
	// The traversing names that flatten onto whitelisted ones land inside dest,
	// which is the intended outcome: contained, not escaped.
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("%s should have been flattened into the destination: %v", name, err)
		}
	}
}

func TestUnpackFailsWhenThereIsNoBinary(t *testing.T) {
	path := buildZip(t, map[string]string{"geoip.dat": "data", "README.md": "docs"})
	if _, err := Unpack(path, t.TempDir()); err == nil {
		t.Fatal("an archive with no xray binary unpacked successfully")
	}
}

// An interrupted extraction must not leave a partial file at the path something
// is about to execute.
func TestUnpackLeavesNoPartFiles(t *testing.T) {
	path := buildZip(t, map[string]string{"xray": "the binary", "geoip.dat": "data"})
	dest := t.TempDir()
	if _, err := Unpack(path, dest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("left behind %s", e.Name())
		}
	}
}

// The archive really is what this package assumes. Skipped unless a real one is
// pointed at, because it is 21 MB.
func TestAgainstARealArchive(t *testing.T) {
	path := os.Getenv("CHIRAL_TEST_ARCHIVE")
	if path == "" {
		t.Skip("set CHIRAL_TEST_ARCHIVE to a real Xray-linux-*.zip")
	}
	if want := os.Getenv("CHIRAL_TEST_ARCHIVE_SHA256"); want != "" {
		if err := Verify(path, want); err != nil {
			t.Fatalf("the published checksum does not cover the archive: %v", err)
		}
	}
	dest := t.TempDir()
	bin, err := Unpack(path, dest)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() < 1<<20 {
		t.Fatalf("the extracted binary is only %d bytes", st.Size())
	}
	for _, name := range []string{"geoip.dat", "geosite.dat"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("the real archive no longer carries %s: %v", name, err)
		}
	}
}

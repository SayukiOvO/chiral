package kernel

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/template"
)

// A registry with no baked binary: these tests are about paths and offsets,
// not about running anything.
func templateXrayZero() template.Xray { return template.Xray{} }

func chunkFile(t *testing.T, size int) (string, []byte) {
	t.Helper()
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(i % 251)
	}
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, body
}

// Reading the whole file back one chunk at a time has to reproduce it exactly.
// An off-by-one here is a corrupt binary on a node, discovered as a checksum
// failure after a 21 MB transfer.
func TestChunksReassembleExactly(t *testing.T) {
	for _, size := range []int{0, 1, 999, 1000, 1001, 4096} {
		path, want := chunkFile(t, size)
		var got []byte
		var off int64
		for i := 0; ; i++ {
			if i > 100 {
				t.Fatalf("size %d: the transfer never ended", size)
			}
			data, last, err := ReadChunk(path, off, 100)
			if err != nil {
				t.Fatalf("size %d at offset %d: %v", size, off, err)
			}
			got = append(got, data...)
			off += int64(len(data))
			if last {
				break
			}
			if len(data) == 0 {
				t.Fatalf("size %d: an empty chunk that was not the last", size)
			}
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("size %d: reassembled %d bytes, want %d", size, len(got), len(want))
		}
	}
}

// A file whose size is an exact multiple of the chunk size must still end,
// rather than needing one extra empty round trip that the agent would read as a
// stalled transfer.
func TestAnExactMultipleEndsOnTheLastFullChunk(t *testing.T) {
	path, _ := chunkFile(t, 1000)
	data, last, err := ReadChunk(path, 900, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 100 {
		t.Fatalf("got %d bytes, want 100", len(data))
	}
	if !last {
		t.Fatal("the chunk ending exactly at EOF was not marked last")
	}
}

func TestAnOffsetPastTheEndIsAnError(t *testing.T) {
	path, _ := chunkFile(t, 100)
	if _, _, err := ReadChunk(path, 101, 10); err == nil {
		t.Error("an offset past the end was accepted")
	}
	if _, _, err := ReadChunk(path, -1, 10); err == nil {
		t.Error("a negative offset was accepted")
	}
}

func TestReadingAMissingArchiveIsAnError(t *testing.T) {
	if _, _, err := ReadChunk(filepath.Join(t.TempDir(), "absent.zip"), 0, 10); err == nil {
		t.Error("reading a file that does not exist succeeded")
	}
}

// The archive path has to survive a platform string containing a slash, or
// every relay would try to write into a directory named after the OS.
func TestArchivePathFlattensThePlatform(t *testing.T) {
	f := NewFetcher(New(t.TempDir(), templateXrayZero()))
	p := f.ArchivePath("26.9.1", "linux/arm64")
	if filepath.Base(p) != "26.9.1_linux-arm64.zip" {
		t.Fatalf("ArchivePath produced %q", p)
	}
	if filepath.Base(filepath.Dir(p)) != archiveDir {
		t.Fatalf("archives are not under %s: %q", archiveDir, p)
	}
}

// The archive cache lives under the registry's directory, so the version scan
// must not mistake it for an installed version.
func TestTheArchiveCacheIsNotMistakenForAVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, archiveDir), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(dir, templateXrayZero())
	for _, v := range r.Versions() {
		if v == archiveDir {
			t.Fatalf("the archive cache was listed as a kernel version: %v", r.Versions())
		}
	}
}

// The race that broke the first real run: prewarming and an operator pressing
// "upgrade" named the same version at the same moment, both unpacked into a
// staging path derived from that version, and each one's cleanup deleted the
// other's half-written files. It surfaced as a rename failing on a file written
// a moment earlier, which points at nothing.
func TestConcurrentInstallsOfOneVersionDoNotFightOverStaging(t *testing.T) {
	archive := releaseArchive(t, "26.9.1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	f := NewFetcher(New(dir, templateXrayZero()))
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])

	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = f.Install(context.Background(), "26.9.1", srv.URL, digest)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("install %d failed: %v", i, err)
		}
	}
	if !f.reg.Has("26.9.1") {
		t.Fatal("26.9.1 is not installed after six concurrent installs")
	}
	// And nothing is left behind for the next scan to trip over.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".staging") {
			t.Errorf("left behind %s", e.Name())
		}
	}
}

// releaseArchive builds a zip shaped like the real one, whose binary reports
// the given version.
func releaseArchive(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"xray":        "#!/bin/sh\necho \"Xray " + version + " (Xray, Penetrates Everything.)\"\n",
		"geoip.dat":   strings.Repeat("ip", 4096),
		"geosite.dat": strings.Repeat("site", 4096),
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

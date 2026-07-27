package kernel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/SayukiOvO/chiral/internal/xrayarchive"
)

// Fetching release archives: once so the panel can validate configs for a
// version, and again per platform so it can relay those bytes to a node that
// cannot reach GitHub itself.
//
// Two different archives, and the difference is easy to get wrong: validation
// needs the build for CORE's platform (a linux/amd64 panel runs `xray -test`
// with a linux/amd64 binary, whatever the node is), while the relay needs the
// build for the NODE's platform, byte-identical to what GitHub would have
// served. Serving the panel's own copy to an arm64 node would hash correctly
// against the wrong checksum and fail at exec time with nothing useful to say.

const (
	fetchTimeout = 20 * time.Minute
	// archiveDir holds downloaded archives, kept for relaying. Dot-prefixed so
	// Rescan's version-directory walk never mistakes it for a version.
	archiveDir = ".archives"
)

// Platform is the panel's own GOOS/GOARCH, in the form release assets are
// keyed by.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// Fetcher downloads and caches release archives under a registry's directory.
type Fetcher struct {
	reg  *Registry
	HTTP *http.Client

	// One in-flight fetch per (version, platform). Without this, telling ten
	// nodes to upgrade at once starts ten downloads of the same 21 MB file into
	// the same path, and the last writer wins a race with the readers.
	mu       sync.Mutex
	inflight map[string]*fetchOnce
	// installing serialises Install per version, for the same reason one level
	// up: prewarming and an operator pressing "upgrade" can name the same
	// version at the same moment, and unpacking it twice at once is how one
	// call's cleanup deletes the other call's half-written directory.
	installing map[string]*sync.Mutex
}

type fetchOnce struct {
	done chan struct{}
	path string
	err  error
}

func NewFetcher(reg *Registry) *Fetcher {
	return &Fetcher{reg: reg, inflight: map[string]*fetchOnce{}, installing: map[string]*sync.Mutex{}}
}

func (f *Fetcher) http() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: fetchTimeout}
}

// ArchivePath is where a given version+platform archive is cached.
func (f *Fetcher) ArchivePath(version, platform string) string {
	// Platform contains a slash; flatten it so the whole thing is one filename.
	safe := ""
	for _, r := range platform {
		if r == '/' {
			r = '-'
		}
		safe += string(r)
	}
	return filepath.Join(f.reg.Dir(), archiveDir, version+"_"+safe+".zip")
}

// EnsureArchive downloads and verifies an archive if it is not already cached,
// and returns its path. Concurrent callers for the same archive share one
// download.
func (f *Fetcher) EnsureArchive(ctx context.Context, version, platform, url, sha256 string) (string, error) {
	dest := f.ArchivePath(version, platform)
	// A cached archive is re-verified rather than trusted by existence: the
	// file may be a truncated remnant of an interrupted download, and this is
	// the copy that gets handed to every node.
	if err := xrayarchive.Verify(dest, sha256); err == nil {
		return dest, nil
	}

	key := dest
	f.mu.Lock()
	if o, ok := f.inflight[key]; ok {
		f.mu.Unlock()
		select {
		case <-o.done:
			return o.path, o.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	o := &fetchOnce{done: make(chan struct{})}
	f.inflight[key] = o
	f.mu.Unlock()

	o.path, o.err = f.fetch(ctx, dest, url, sha256)
	close(o.done)
	f.mu.Lock()
	delete(f.inflight, key)
	f.mu.Unlock()
	return o.path, o.err
}

func (f *Fetcher) fetch(ctx context.Context, dest, url, sha256 string) (string, error) {
	if url == "" {
		return "", fmt.Errorf("no download URL")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := f.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned %s", url, resp.Status)
	}

	// Download to a temporary name and rename, so a reader never sees a
	// partially written archive at the path it is about to relay.
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(out, resp.Body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := xrayarchive.Verify(tmp, sha256); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return dest, nil
}

// Install makes a version validatable by the panel: it fetches the archive for
// CORE's own platform, unpacks the binary and the geo databases into the
// registry, and rescans.
//
// The geo databases matter as much as the binary. Without them every routing
// rule using geosite: or geoip: fails to load, so `xray -test` would reject the
// most common construct in real configs — and the panel would be confidently
// blocking pushes that are perfectly correct.
func (f *Fetcher) Install(ctx context.Context, version, url, sha256 string) error {
	if f.reg.Dir() == "" {
		return fmt.Errorf("no kernel directory configured (CHIRAL_KERNEL_DIR)")
	}
	lock := f.installLock(version)
	lock.Lock()
	defer lock.Unlock()
	// Re-check under the lock: whoever held it may have just finished this
	// exact install, and unpacking 66 MB again to reach the same state is pure
	// waste.
	if f.reg.Has(version) {
		return nil
	}

	archive, err := f.EnsureArchive(ctx, version, Platform(), url, sha256)
	if err != nil {
		return err
	}
	// A unique staging directory rather than a name derived from the version.
	// Two unpacks of one version sharing a path means each one's cleanup can
	// delete the other's half-written files, which surfaces as a rename failing
	// on a file that was written a moment ago — a genuinely baffling error,
	// found the first time this ran against a real release.
	staging, err := os.MkdirTemp(f.reg.Dir(), version+".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if _, err := xrayarchive.Unpack(archive, staging); err != nil {
		return err
	}
	final := filepath.Join(f.reg.Dir(), version)
	os.RemoveAll(final)
	if err := os.Rename(staging, final); err != nil {
		return err
	}
	f.reg.Rescan()

	// Ask it what it is. A URL pointing at the wrong build hashes correctly and
	// unpacks cleanly; only this catches it — and the whole point of keeping
	// per-version binaries is undone if one of them is mislabelled.
	if got := f.reg.For(version); !got.Exact {
		os.RemoveAll(final)
		f.reg.Rescan()
		return fmt.Errorf("the archive for %s unpacked to a binary reporting %q", version, got.Version)
	}
	return nil
}

func (f *Fetcher) installLock(version string) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.installing[version]; ok {
		return m
	}
	m := &sync.Mutex{}
	f.installing[version] = m
	return m
}

// ReadChunk returns up to n bytes of a cached archive at off, and whether that
// slice reaches the end.
func ReadChunk(path string, off int64, n int) ([]byte, bool, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil {
		return nil, false, err
	}
	if off > st.Size() || off < 0 {
		return nil, false, fmt.Errorf("offset %d is outside the %d byte archive", off, st.Size())
	}
	if remaining := st.Size() - off; int64(n) > remaining {
		n = int(remaining)
	}
	buf := make([]byte, n)
	// ReadAt, not Read: the handle is fresh at position zero, and reading from
	// there would return the head of the archive for every chunk — 21 MB that
	// transfers cleanly, reassembles to the right length, and fails its
	// checksum with nothing to point at.
	read, err := fh.ReadAt(buf, off)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false, err
	}
	buf = buf[:read]
	return buf, off+int64(read) >= st.Size(), nil
}

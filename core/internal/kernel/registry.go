// Package kernel keeps the Xray binaries the panel validates against — one per
// version any node might be running, not one for the panel as a whole.
//
// Why a set and not a single binary: the panel's job is to reject a config
// before it can take a node down, and `xray -test` only answers for the build
// that runs it. One panel-side binary was fine while every node ran whatever
// the panel ran. Runtime upgrades end that, and the failure is symmetric —
// validate a config for an upgraded node with the old binary and every new
// feature is rejected as unknown, so the upgrade buys nothing; validate for a
// not-yet-upgraded node with the new one and the panel cheerfully accepts a
// config that will not start, which is a node down until someone notices.
//
// So the panel keeps the binary of every version it has installed anywhere,
// and picks per node. The one Core ships with is the floor: always present,
// used when nothing better is known, and never silently passed off as an exact
// answer.
package kernel

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/SayukiOvO/chiral/core/internal/template"
)

// BinaryName is the file each version directory holds.
const BinaryName = "xray"

// Registry resolves a version string to a usable binary.
//
// Layout on disk is <dir>/<version>/xray, with version in canonical form (no
// leading "v"). The directory is the same one the upgrade path installs into,
// so "a version the panel can validate for" and "a version the panel has
// handed out" stay the same set by construction rather than by bookkeeping.
type Registry struct {
	dir   string
	baked template.Xray

	mu      sync.RWMutex
	scanned map[string]string // version -> binary path
}

// New builds a registry over dir, with baked as the always-available floor.
// A missing or unreadable dir is not an error: the floor still works.
func New(dir string, baked template.Xray) *Registry {
	r := &Registry{dir: dir, baked: baked, scanned: map[string]string{}}
	r.Rescan()
	return r
}

// Rescan re-reads the version directories. Called at startup and after an
// install, so a version fetched for a node becomes validatable immediately.
func (r *Registry) Rescan() {
	found := map[string]string{}
	if r.dir != "" {
		entries, err := os.ReadDir(r.dir)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				// Skip the archive cache and any in-progress unpack. A staging
				// directory holds a real executable partway through, so without
				// this a concurrent scan could register "26.9.1.staging-4711" as
				// a version of its own.
				if e.Name() == archiveDir || strings.Contains(e.Name(), ".staging-") {
					continue
				}
				version := template.NormalizeVersion(e.Name())
				if version == "" {
					continue
				}
				bin := filepath.Join(r.dir, e.Name(), BinaryName)
				if st, err := os.Stat(bin); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
					found[version] = bin
				}
			}
		}
	}
	r.mu.Lock()
	r.scanned = found
	r.mu.Unlock()
}

// Resolution is the answer to "which binary should judge this node's config",
// carried together with how confident that answer is.
//
// Exact is the whole point of returning a struct. A caller that only got the
// binary would have no way to distinguish "tested against the kernel that will
// run it" from "tested against something else and hoped", and would report both
// as verified — which is precisely the reassurance an operator must not be
// given about a node they are mid-upgrade.
type Resolution struct {
	Xray template.Xray
	// Version is what Xray actually is, canonical form.
	Version string
	// Want is the version that was asked for; differs from Version exactly
	// when Exact is false.
	Want  string
	Exact bool
}

// Describe renders the resolution for a log line or an operator-facing note.
func (r Resolution) Describe() string {
	if r.Exact {
		return "xray " + r.Version
	}
	if r.Want == "" {
		return "xray " + r.Version + " (the node has not reported a version)"
	}
	return fmt.Sprintf("xray %s (the node runs %s, which the panel does not have)", r.Version, r.Want)
}

// For resolves a version, falling back to the baked binary.
//
// An empty want is not an error and not a special case worth failing on: a node
// that has never connected has no version, and refusing to preview its config
// would make the panel useless for exactly the node an operator is trying to
// bring up. It comes back Exact=false, like any other miss.
func (r *Registry) For(want string) Resolution {
	want = template.NormalizeVersion(want)
	r.mu.RLock()
	bin, ok := r.scanned[want]
	r.mu.RUnlock()
	if ok && want != "" {
		return Resolution{Xray: template.Xray{Bin: bin}, Version: want, Want: want, Exact: true}
	}
	baked := r.baked
	bakedVersion := baked.Version()
	return Resolution{
		Xray:    baked,
		Version: bakedVersion,
		Want:    want,
		Exact:   want != "" && want == bakedVersion,
	}
}

// Baked is the binary Core ships with: the floor, and the one used for work
// that belongs to no particular node (key derivation, say).
func (r *Registry) Baked() template.Xray { return r.baked }

// Versions lists every version the registry can validate for, newest string
// last, including the baked one.
func (r *Registry) Versions() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.scanned)+1)
	for v := range r.scanned {
		out = append(out, v)
	}
	r.mu.RUnlock()
	if v := r.baked.Version(); v != "" && !contains(out, v) {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return template.CompareVersions(out[i], out[j]) < 0 })
	return out
}

// Has reports whether an exact binary for this version is available.
func (r *Registry) Has(version string) bool {
	version = template.NormalizeVersion(version)
	if version == "" {
		return false
	}
	r.mu.RLock()
	_, ok := r.scanned[version]
	r.mu.RUnlock()
	return ok || version == r.baked.Version()
}

// Dir is the install root, for the upgrade path to unpack into.
func (r *Registry) Dir() string { return r.dir }

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

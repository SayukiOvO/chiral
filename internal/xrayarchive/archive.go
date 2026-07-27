// Package xrayarchive verifies and unpacks an Xray-core release archive.
//
// Lives at the repository root rather than under core/ or agent/ because both
// perform exactly this operation and neither owns it: Core unpacks a release so
// it can validate configs against that build, the agent unpacks the same
// release so it can run it. Two copies of "check a hash, then unzip something
// downloaded from the internet" is precisely the code that must not drift.
//
// This is a deliberate, narrow amendment to the rule that core and agent share
// only proto/ (CLAUDE.md §7): a root-level internal/ package is importable by
// both without either reaching into the other's internal/, which is what that
// rule exists to prevent.
//
// Measured against the real thing (v26.7.11, Xray-linux-64.zip): a flat zip
// holding xray, geoip.dat, geosite.dat, README.md and LICENSE, with no
// directory prefix, and a .dgst whose SHA2-256 line is over the zip itself.
package xrayarchive

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BinaryName is the executable inside the archive, and the name it keeps once
// unpacked.
const BinaryName = "xray"

// wanted lists what is extracted and nothing else. README and LICENSE are
// dropped: 30 KB per version per node is not much, but a whitelist is what
// keeps a hostile archive from writing whatever it likes, and an archive is
// exactly the thing you should not trust the contents of.
var wanted = map[string]bool{
	BinaryName: true,
	// The geo databases. Without them any routing rule using geosite: or
	// geoip: fails to load — which is most real configs — and it fails at
	// `-test` time, so a node would refuse a config that is perfectly correct.
	"geoip.dat":   true,
	"geosite.dat": true,
}

// maxEntry bounds what will be written for a single file. The real binary is
// ~37 MB and geoip.dat ~18 MB; 256 MB leaves generous headroom while still
// refusing a zip bomb.
const maxEntry = 256 << 20

// Verify checks an archive's SHA-256 against the expected lowercase hex digest.
//
// Separate from Unpack, and always called first, because the ordering is the
// security property: unpacking writes attacker-controlled bytes to disk under a
// name we then execute. There is no version of "unpack and check afterwards"
// that is safe.
func Verify(path, wantHex string) error {
	if len(wantHex) != 64 {
		return fmt.Errorf("refusing to install without a SHA-256 (got %q)", wantHex)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantHex) {
		return fmt.Errorf("checksum mismatch: archive is %s, expected %s", got, wantHex)
	}
	return nil
}

// Unpack extracts the binary and geo databases from archivePath into destDir,
// which is created if needed. The binary is left executable.
//
// Returns the path of the extracted binary.
func Unpack(archivePath, destDir string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("opening the archive: %w", err)
	}
	defer zr.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}

	var binPath string
	for _, f := range zr.File {
		// Flatten and whitelist. path.Base alone would still let "../xray"
		// through as "xray", which is harmless here only because the name is
		// then checked against the whitelist — do both, in that order.
		name := filepath.Base(filepath.ToSlash(f.Name))
		if !wanted[name] {
			continue
		}
		mode := os.FileMode(0o644)
		if name == BinaryName {
			mode = 0o755
		}
		dest := filepath.Join(destDir, name)
		if err := extract(f, dest, mode); err != nil {
			return "", fmt.Errorf("extracting %s: %w", name, err)
		}
		if name == BinaryName {
			binPath = dest
		}
	}
	if binPath == "" {
		return "", fmt.Errorf("the archive contains no %s binary", BinaryName)
	}
	return binPath, nil
}

func extract(f *zip.File, dest string, mode os.FileMode) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// Write to a sibling then rename, so an interrupted extraction never leaves
	// a half-written binary at a path something else is about to execute.
	tmp := dest + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(rc, maxEntry+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if n > maxEntry {
		os.Remove(tmp)
		return fmt.Errorf("entry is larger than the %d byte limit", int64(maxEntry))
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

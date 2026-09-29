package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/migrations"
)

// SnapshotIdentity identifies the exact standalone database accepted by
// OpenSnapshot. It is evidence about the input, not a claim that a live
// database has been backed up or that its filesystem cannot change later.
type SnapshotIdentity struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type snapshotState struct {
	identity SnapshotIdentity
	path     string
	info     os.FileInfo
}

// SnapshotIdentity reports whether this store was opened by OpenSnapshot.
// Returning a value prevents callers from changing the trusted marker.
func (s *Store) SnapshotIdentity() (SnapshotIdentity, bool) {
	if s == nil || s.snapshot == nil {
		return SnapshotIdentity{}, false
	}
	return s.snapshot.identity, true
}

// VerifySnapshot rechecks the source after an offline export. It detects
// changed content, replaced files and newly created SQLite sidecars without
// exposing the private input path. This is a consistency check, not a lock
// against malicious concurrent writers; a sealed backup remains mandatory.
func (s *Store) VerifySnapshot() error {
	if s == nil || s.snapshot == nil {
		return errors.New("snapshot verification requires a read-only snapshot store")
	}
	state := s.snapshot
	info, err := snapshotFile(state.path)
	if err != nil || !os.SameFile(state.info, info) || !state.info.ModTime().Equal(info.ModTime()) {
		return errors.New("snapshot database changed during inspection")
	}
	identity, err := snapshotHash(state.path, info)
	if err != nil || identity != state.identity {
		return errors.New("snapshot database changed during inspection")
	}
	return nil
}

// OpenSnapshot opens an existing, offline SQLite .backup result without
// migrations, journal creation, or source writes. It must not be used on a
// live database: immutable SQLite connections deliberately do not coordinate
// with writers. The caller must keep this completed backup unchanged for the
// lifetime of the store. No error contains database content or driver output.
func OpenSnapshot(path string, box *secret.Box) (*Store, error) {
	if path == "" {
		return nil, errors.New("snapshot database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("snapshot database path is invalid")
	}
	info, err := snapshotFile(abs)
	if err != nil {
		return nil, err
	}
	identity, err := snapshotHash(abs, info)
	if err != nil {
		return nil, err
	}
	q := url.Values{
		"mode":      {"ro"},
		"immutable": {"1"},
		"_pragma":   {"query_only(1)", "foreign_keys(1)"},
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: q.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("snapshot database could not be opened read-only")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = db.Close()
		}
	}()
	if err := checkSnapshotDatabase(db); err != nil {
		return nil, err
	}
	// Check the pathname and content again after SQLite opened and inspected
	// them. This detects replacement or modification during the preflight;
	// it does not replace the requirement for an offline, stable input.
	after, err := snapshotFile(abs)
	if err != nil || !os.SameFile(info, after) {
		return nil, errors.New("snapshot database changed during inspection")
	}
	verified, err := snapshotHash(abs, after)
	if err != nil || verified != identity {
		return nil, errors.New("snapshot database changed during inspection")
	}
	if box == nil {
		box, _ = secret.NewBox("")
	}
	closeOnError = false
	return &Store{db: db, box: box, snapshot: &snapshotState{identity: identity, path: abs, info: after}}, nil
}

func snapshotFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("snapshot database must be an existing regular non-symlink file")
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("snapshot database must not have SQLite sidecar files")
		}
	}
	return info, nil
}

func snapshotHash(path string, expected os.FileInfo) (SnapshotIdentity, error) {
	f, err := os.Open(path)
	if err != nil {
		return SnapshotIdentity{}, errors.New("snapshot database could not be read")
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(expected, before) {
		return SnapshotIdentity{}, errors.New("snapshot database changed during inspection")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	if err != nil {
		return SnapshotIdentity{}, errors.New("snapshot database could not be read")
	}
	after, err := f.Stat()
	if err != nil || n != before.Size() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return SnapshotIdentity{}, errors.New("snapshot database changed during inspection")
	}
	return SnapshotIdentity{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: n}, nil
}

func checkSnapshotDatabase(db *sql.DB) error {
	var queryOnly int
	if err := db.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		return errors.New("snapshot database did not enable read-only query protection")
	}
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		return errors.New("snapshot database integrity check failed")
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return errors.New("snapshot database foreign-key check failed")
	}
	hasViolation := rows.Next()
	rowErr := rows.Err()
	closeErr := rows.Close()
	if hasViolation || rowErr != nil || closeErr != nil {
		return errors.New("snapshot database foreign-key check failed")
	}
	want, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(want) == 0 {
		return errors.New("snapshot schema compatibility could not be checked")
	}
	sort.Strings(want)
	rows, err = db.Query("SELECT name FROM schema_migrations ORDER BY name")
	if err != nil {
		return errors.New("snapshot database schema does not match this build")
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil || i >= len(want) || name != want[i] {
			return errors.New("snapshot database schema does not match this build")
		}
		i++
	}
	if rows.Err() != nil || i != len(want) {
		return errors.New("snapshot database schema does not match this build")
	}
	return nil
}

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/migrations"
)

func snapshotFixture(t *testing.T, mutate func(*Store)) (string, *secret.Box) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "snapshot.db")
	box, err := secret.NewBox(storeTestKey)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path, box)
	if err != nil {
		t.Fatal(err)
	}
	if _, trusted := st.SnapshotIdentity(); trusted {
		t.Fatal("a mutable store claims to be a read-only snapshot")
	}
	if err := st.VerifySnapshot(); err == nil {
		t.Fatal("a mutable store passed snapshot verification")
	}
	if mutate != nil {
		mutate(st)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return path, box
}

func snapshotReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenSnapshotReadsWithoutChangingTheStandaloneFile(t *testing.T) {
	path, box := snapshotFixture(t, func(st *Store) {
		if _, err := st.CreateNode("snapshot-node", "snapshot-join-hash"); err != nil {
			t.Fatal(err)
		}
	})
	before := snapshotReadFile(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	st, err := OpenSnapshot(path, box)
	if err != nil {
		t.Fatal(err)
	}
	identity, trusted := st.SnapshotIdentity()
	sum := sha256.Sum256(before)
	if !trusted || identity.Size != int64(len(before)) || identity.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("snapshot identity does not describe the input: %+v, %v", identity, trusted)
	}
	identity.SHA256 = "caller changed its copy"
	again, _ := st.SnapshotIdentity()
	if again.SHA256 == identity.SHA256 {
		t.Fatal("caller changed the trusted identity")
	}
	nodes, err := st.ListNodes()
	if err != nil || len(nodes) != 1 || nodes[0].Name != "snapshot-node" {
		t.Fatalf("snapshot read failed: nodes=%d, err=%v", len(nodes), err)
	}
	for _, stmt := range []string{
		"CREATE TABLE snapshot_write_escape (value TEXT)",
		"CREATE TEMP TABLE snapshot_temp_escape (value TEXT)",
		"UPDATE nodes SET name = 'changed'",
		"DELETE FROM schema_migrations",
	} {
		if _, err := st.db.Exec(stmt); err == nil {
			t.Fatalf("snapshot accepted mutation %q", stmt)
		}
	}
	if _, err := st.db.Exec("PRAGMA query_only = OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec("UPDATE nodes SET name = 'changed'"); err == nil {
		t.Fatal("disabling query_only bypassed SQLite read-only mode")
	}
	if err := st.VerifySnapshot(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	after := snapshotReadFile(t, path)
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("snapshot opening, reading or refused writes changed the source")
	}
	afterEntries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(afterEntries) {
		t.Fatal("snapshot opening created filesystem entries")
	}
	for i := range entries {
		if entries[i].Name() != afterEntries[i].Name() {
			t.Fatal("snapshot opening changed directory entries")
		}
	}
}

func TestOpenSnapshotRejectsMissingDirectoryAndSymlink(t *testing.T) {
	path, box := snapshotFixture(t, nil)
	for _, candidate := range []string{"", filepath.Join(filepath.Dir(path), "missing.db"), filepath.Dir(path)} {
		if st, err := OpenSnapshot(candidate, box); err == nil {
			st.Close()
			t.Fatal("accepted a non-file snapshot input")
		}
	}
	link := filepath.Join(filepath.Dir(path), "linked.db")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if st, err := OpenSnapshot(link, box); err == nil {
		st.Close()
		t.Fatal("accepted a symlink snapshot input")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), "missing.db")); !os.IsNotExist(err) {
		t.Fatal("missing snapshot was created")
	}
}

func TestOpenSnapshotRejectsEverySQLiteSidecar(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		for _, kind := range []string{"file", "directory", "dangling-symlink"} {
			t.Run(suffix+"/"+kind, func(t *testing.T) {
				path, box := snapshotFixture(t, nil)
				var err error
				switch kind {
				case "file":
					err = os.WriteFile(path+suffix, nil, 0600)
				case "directory":
					err = os.Mkdir(path+suffix, 0700)
				case "dangling-symlink":
					err = os.Symlink(filepath.Join(filepath.Dir(path), "absent"), path+suffix)
				}
				if err != nil {
					t.Fatal(err)
				}
				if st, err := OpenSnapshot(path, box); err == nil {
					st.Close()
					t.Fatal("accepted a snapshot with an SQLite sidecar")
				}
			})
		}
	}
}

func TestOpenSnapshotRejectsOlderNewerAndReplacedMigrationNames(t *testing.T) {
	for _, change := range []string{"older", "newer", "replaced", "missing-table"} {
		t.Run(change, func(t *testing.T) {
			path, box := snapshotFixture(t, func(st *Store) {
				names, err := fs.Glob(migrations.FS, "*.sql")
				if err != nil {
					t.Fatal(err)
				}
				sort.Strings(names)
				switch change {
				case "older":
					_, err = st.db.Exec("DELETE FROM schema_migrations WHERE name = ?", names[len(names)-1])
				case "newer":
					_, err = st.db.Exec("INSERT INTO schema_migrations(name,applied_at) VALUES ('9999_sensitive_value.sql',0)")
				case "replaced":
					_, err = st.db.Exec("UPDATE schema_migrations SET name = 'sensitive_value.sql' WHERE name = ?", names[0])
				case "missing-table":
					_, err = st.db.Exec("DROP TABLE schema_migrations")
				}
				if err != nil {
					t.Fatal(err)
				}
			})
			before := snapshotReadFile(t, path)
			st, err := OpenSnapshot(path, box)
			if err == nil {
				st.Close()
				t.Fatal("accepted incompatible migration history")
			}
			if err.Error() != "snapshot database schema does not match this build" {
				t.Fatalf("unexpected or unsafe schema diagnostic: %q", err)
			}
			if !reflect.DeepEqual(before, snapshotReadFile(t, path)) {
				t.Fatal("snapshot opener migrated or changed an incompatible database")
			}
		})
	}
}

func TestOpenSnapshotEscapesURIPathCharacters(t *testing.T) {
	path, box := snapshotFixture(t, nil)
	escaped := filepath.Join(filepath.Dir(path), "snapshot #?%& mode=rw+ 空格.db")
	if err := os.Rename(path, escaped); err != nil {
		t.Fatal(err)
	}
	st, err := OpenSnapshot(escaped, box)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.ListUsers(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec("CREATE TABLE unsafe (v TEXT)"); err == nil {
		t.Fatal("path characters disabled read-only mode")
	}
}

func TestOpenSnapshotRejectsCorruptionWithoutLeakingDriverData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-database-name.db")
	if err := os.WriteFile(path, []byte("not-sqlite-private-content"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := OpenSnapshot(path, nil)
	if err == nil {
		st.Close()
		t.Fatal("accepted a corrupt database")
	}
	if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "SQLITE_") || strings.Contains(err.Error(), path) {
		t.Fatalf("unsafe diagnostic: %q", err)
	}
}

func TestOpenSnapshotRejectsForeignKeyViolations(t *testing.T) {
	path, box := snapshotFixture(t, func(st *Store) {
		for _, statement := range []string{
			"PRAGMA foreign_keys = OFF",
			"CREATE TABLE private_parent (id INTEGER PRIMARY KEY)",
			"CREATE TABLE private_child (parent_id INTEGER REFERENCES private_parent(id))",
			"INSERT INTO private_child VALUES (987654321)",
		} {
			if _, err := st.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	})
	st, err := OpenSnapshot(path, box)
	if err == nil {
		st.Close()
		t.Fatal("accepted broken foreign keys")
	}
	if err.Error() != "snapshot database foreign-key check failed" {
		t.Fatalf("unexpected or unsafe foreign-key diagnostic: %q", err)
	}
}

func TestVerifySnapshotDetectsChangesAfterOpen(t *testing.T) {
	for _, change := range []string{"content", "replacement", "sidecar", "symlink", "removed"} {
		t.Run(change, func(t *testing.T) {
			path, box := snapshotFixture(t, nil)
			st, err := OpenSnapshot(path, box)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.VerifySnapshot(); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "content":
				body := snapshotReadFile(t, path)
				body[len(body)-1] ^= 1
				err = os.WriteFile(path, body, 0600)
			case "replacement":
				body := snapshotReadFile(t, path)
				target := filepath.Join(filepath.Dir(path), "replacement.db")
				if err = os.WriteFile(target, body, 0600); err == nil {
					err = os.Rename(target, path)
				}
			case "sidecar":
				err = os.WriteFile(path+"-wal", nil, 0600)
			case "symlink":
				target := filepath.Join(filepath.Dir(path), "moved.db")
				if err = os.Rename(path, target); err == nil {
					err = os.Symlink(target, path)
				}
			case "removed":
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := st.VerifySnapshot(); err == nil || err.Error() != "snapshot database changed during inspection" {
				t.Fatalf("changed source was accepted or diagnostic was unsafe: %v", err)
			}
		})
	}
}

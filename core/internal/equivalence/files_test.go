package equivalence

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrivateEvidenceRoundTripAndNoOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-evidence")
	b := Bundle{Version: 1, Records: []Record{{Kind: "subscription", ID: "synthetic", Body: []byte("private-token\x00\xff")}}}
	if err := Save(dir, b); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "bundle.json"): 0600} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("non-private mode: %o", info.Mode().Perm())
		}
	}
	path := filepath.Join(dir, "bundle.json")
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, b) {
		t.Fatal("evidence bytes changed")
	}
	if err := Save(dir, Bundle{}); err == nil {
		t.Fatal("existing evidence overwritten")
	}
	link := filepath.Join(t.TempDir(), "symlink.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Fatal("symlink evidence accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("public evidence accepted")
	}
}

func TestEvidenceErrorsDoNotEchoContent(t *testing.T) {
	for _, data := range []string{`{"secret-token":"DO-NOT-PRINT"}`, `{"version":1} DO-NOT-PRINT`, `{"version":"DO-NOT-PRINT"}`} {
		path := filepath.Join(t.TempDir(), "evidence.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || strings.Contains(err.Error(), "DO-NOT-PRINT") {
			t.Fatal("invalid or disclosed diagnostic")
		}
	}
}

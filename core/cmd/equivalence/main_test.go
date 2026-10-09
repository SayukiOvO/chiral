package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SayukiOvO/chiral/core/internal/equivalence"
	"github.com/SayukiOvO/chiral/core/internal/store"
)

func TestInvalidArgumentsNeverEchoSecrets(t *testing.T) {
	for _, args := range [][]string{nil, {"DO-NOT-PRINT"}, {"export", "--secret-key=DO-NOT-PRINT"}, {"compare", "--before=DO-NOT-PRINT"}} {
		var out, diagnostic bytes.Buffer
		if run(args, &out, &diagnostic) != 1 {
			t.Fatal("invalid arguments accepted")
		}
		if strings.Contains(out.String()+diagnostic.String(), "DO-NOT-PRINT") {
			t.Fatal("argument disclosed")
		}
	}
}

func TestExportCLIUsesOnlySnapshotAndSavesIncompleteEvidencePrivately(t *testing.T) {
	t.Setenv("CHIRAL_SECRET_KEY", "")
	root := t.TempDir()
	database := filepath.Join(root, "synthetic.db")
	st, err := store.Open(database, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "export")
	var out, diagnostic bytes.Buffer
	args := []string{"export", "--db", database, "--out", dir,
		"--public-url", "https://panel.example.test", "--at", "2032-01-01T00:00:00Z", "--revision", "fixture"}
	if got := run(args, &out, &diagnostic); got != 2 {
		t.Fatalf("exit=%d diagnostic=%s", got, diagnostic.String())
	}
	if !strings.Contains(out.String(), `"saved":true`) || !strings.Contains(out.String(), `"complete":false`) {
		t.Fatal("missing explicit incomplete result")
	}
	if _, err := equivalence.Load(filepath.Join(dir, "bundle.json")); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("CLI changed snapshot")
	}
	if got := run(args, &out, &diagnostic); got != 1 {
		t.Fatal("CLI overwrote existing evidence")
	}
}

func TestIdenticalIncompleteEvidenceReturnsNonzero(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := equivalence.Save(dir, equivalence.Bundle{Version: 1}); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if got := run([]string{"compare", "--before", filepath.Join(dir, "bundle.json"), "--after", filepath.Join(dir, "bundle.json")}, &out, &diagnostic); got != 2 {
		t.Fatalf("exit=%d", got)
	}
	if !strings.Contains(out.String(), `"equal":true`) || !strings.Contains(out.String(), `"passed":false`) {
		t.Fatalf("unexpected summary: %s", out.String())
	}
}

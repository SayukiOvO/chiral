package template

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The two namespaces this exists to reconcile.
func TestNormalizeVersionStripsTheTagPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"v26.7.11":  "26.7.11",
		"26.7.11":   "26.7.11",
		" v26.7.11": "26.7.11",
		"":          "",
	} {
		if got := NormalizeVersion(in); got != want {
			t.Errorf("NormalizeVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// The claim this whole helper rests on: the binary reports a version WITHOUT
// the "v" that GitHub tags carry. Asserted against the real binary rather than
// against my memory of its output, because if it ever gains a "v" the
// comparison silently inverts and every node looks permanently out of date.
func TestBinaryReportsVersionWithoutThePrefix(t *testing.T) {
	bin := os.Getenv("CHIRAL_XRAY_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("xray"); err != nil {
			t.Skip("no xray binary")
		}
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil {
		t.Fatalf("xray version: %v", err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(line)
	if len(fields) < 2 {
		t.Fatalf("unexpected version output: %q", line)
	}
	if strings.HasPrefix(fields[1], "v") {
		t.Fatalf("the binary now reports %q with a v prefix; NormalizeVersion and "+
			"every comparison built on it need revisiting", fields[1])
	}
	x := Xray{Bin: bin}
	if got := x.Version(); got != fields[1] {
		t.Errorf("Version() = %q, want %q", got, fields[1])
	}
}

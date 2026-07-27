package template

import "testing"

func TestCompareVersionsOrdersNumerically(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		// The reason this cannot be strings.Compare: Xray numbers releases by
		// date, so the month segment crosses from one digit to two and string
		// order silently inverts.
		{"26.3.27", "26.10.1", -1},
		{"26.10.1", "26.3.27", 1},
		{"26.9.1", "26.10.1", -1},
		{"25.12.31", "26.1.1", -1},
		{"26.3.27", "26.3.27", 0},
		{"26.3.27", "26.3.28", -1},
		// The "v" belongs to the tag namespace and must not affect ordering.
		{"v26.3.27", "26.3.27", 0},
		{"v26.10.1", "v26.3.27", 1},
		// Missing segments are zero, not garbage.
		{"26.3", "26.3.0", 0},
		{"26.3", "26.3.1", -1},
	}
	for _, c := range cases {
		if got := sign(CompareVersions(c.a, c.b)); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// The panel deliberately tracks prereleases, so both forms are in play at once
// and "is this node behind?" has to answer correctly for one sitting on the rc
// when the final ships.
func TestPrereleasesSortBeforeTheirRelease(t *testing.T) {
	if sign(CompareVersions("26.7.11-rc1", "26.7.11")) != -1 {
		t.Error("26.7.11-rc1 did not sort before 26.7.11")
	}
	if sign(CompareVersions("26.7.11", "26.7.11-rc1")) != 1 {
		t.Error("26.7.11 did not sort after its own prerelease")
	}
	if sign(CompareVersions("26.7.11-rc1", "26.7.11-rc2")) != -1 {
		t.Error("rc1 did not sort before rc2")
	}
	// A prerelease of a later base still beats an earlier final.
	if sign(CompareVersions("26.8.1-rc1", "26.7.11")) != 1 {
		t.Error("a prerelease of a later version sorted below an earlier release")
	}
}

// Garbage must sort low rather than win by accident: a version that compares
// high suppresses upgrades, which fails silently.
func TestUnparsableSegmentsSortLow(t *testing.T) {
	if sign(CompareVersions("26.x.1", "26.0.1")) != -1 {
		t.Error("an unparsable segment did not sort below a real one")
	}
	if sign(CompareVersions("", "0.0.1")) != -1 {
		t.Error("the empty version did not sort below a real one")
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

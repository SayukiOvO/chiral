package template

import (
	"strconv"
	"strings"
)

// NormalizeVersion is the one place a version string is put into canonical
// form, which is WITHOUT the leading "v".
//
// There are two namespaces and they do not match: `xray version` prints
// "Xray 26.3.27", while GitHub tags and asset URLs are "v26.7.11". Comparing
// one against the other is a guaranteed false negative, and the comparison is
// what decides whether a node needs upgrading — so it would fail by always
// claiming an upgrade is due.
//
// The invariant, enforced by using this at every boundary: the database, the
// wire and every comparison hold the bare form. Only the download URL puts the
// "v" back.
func NormalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// CompareVersions orders two Xray versions: negative if a < b, zero if equal,
// positive if a > b.
//
// Xray numbers releases by date (26.3.27 is 27 March 2026), so the segments are
// numeric and must be compared as numbers — string order puts 26.3.27 after
// 26.10.1, which would read as "the node is ahead of the release" and suppress
// a real upgrade.
//
// A hyphenated suffix marks a prerelease and sorts BEFORE the same base
// version, per the usual convention: 26.7.11-rc1 precedes 26.7.11. This matters
// here because the panel deliberately tracks prereleases, so both forms will be
// in play at once and "is the node behind?" has to answer correctly for a node
// sitting on the rc while the final ships.
//
// An unparsable segment compares as -1, below every real number, so garbage
// sorts low rather than winning by accident.
func CompareVersions(a, b string) int {
	aBase, aPre := splitPrerelease(NormalizeVersion(a))
	bBase, bPre := splitPrerelease(NormalizeVersion(b))

	aSeg, bSeg := strings.Split(aBase, "."), strings.Split(bBase, ".")
	for i := 0; i < len(aSeg) || i < len(bSeg); i++ {
		if c := compareInt(segAt(aSeg, i), segAt(bSeg, i)); c != 0 {
			return c
		}
	}
	switch {
	case aPre == "" && bPre == "":
		return 0
	case aPre == "":
		return 1 // a is the final release, b is a prerelease of it
	case bPre == "":
		return -1
	}
	return strings.Compare(aPre, bPre)
}

func splitPrerelease(v string) (base, pre string) {
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		return v[:i], v[i+1:]
	}
	return v, ""
}

func segAt(segs []string, i int) int {
	if i >= len(segs) {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(segs[i]))
	if err != nil {
		return -1
	}
	return n
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

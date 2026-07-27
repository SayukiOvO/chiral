package release

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// The exact bytes Xray publishes, so the parser is tested against the real
// format rather than my recollection of it. The label is "SHA2-256" — matching
// "SHA256" alone would come back empty, and an empty checksum treated as
// "nothing to compare" is how verification quietly stops happening.
const realDgst = `MD5= 7b4ea9f0e3590ab6b4a239c9531b1043
SHA1= ed009f0648de0628c20f09dfc726a54607f07918
SHA2-256= aa11c3685c71da0ffc71e511db50404609e7e963bb914b048f59a6a00af8930e
SHA2-512= 7dbde63e7e56a86fc3d52c04647c3a9050bf848d55ba55c4b996a286e33fefc83ed0463c4a5afed10d207ce15e924d39ad8f26430706bcb2bb2f6a4cf93402fe
`

func TestParseDigestReadsXraysOwnFormat(t *testing.T) {
	got := ParseDigest(realDgst)
	want := "aa11c3685c71da0ffc71e511db50404609e7e963bb914b048f59a6a00af8930e"
	if got != want {
		t.Fatalf("ParseDigest = %q, want %q", got, want)
	}
}

func TestParseDigestRejectsWhatItCannotUse(t *testing.T) {
	for _, body := range []string{
		"",
		"MD5= 7b4ea9f0e3590ab6b4a239c9531b1043\n",
		"SHA2-256= not-hex-at-all\n",
		"SHA2-256= aa11c3\n", // truncated
	} {
		if got := ParseDigest(body); got != "" {
			t.Errorf("ParseDigest(%q) = %q, want empty", body, got)
		}
	}
}

func TestAssetForKnownAndUnknownPlatforms(t *testing.T) {
	r := Release{Tag: "v26.7.11", assets: map[string]string{
		"Xray-linux-64.zip":      "https://example.invalid/a.zip",
		"Xray-linux-64.zip.dgst": "https://example.invalid/a.zip.dgst",
	}}
	archive, digest, err := r.AssetFor("linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if archive == "" || digest == "" {
		t.Fatalf("AssetFor returned %q / %q", archive, digest)
	}
	if _, _, err := r.AssetFor("plan9/amd64"); err == nil {
		t.Error("an unsupported platform was given an asset")
	}
	if _, _, err := r.AssetFor("linux/arm64"); err == nil {
		t.Error("a platform this release does not publish was accepted")
	}
}

// Refusing beats downloading 20 MB the panel cannot check before running it on
// every node it manages.
func TestAnAssetWithNoChecksumIsRefused(t *testing.T) {
	r := Release{Tag: "v26.7.11", assets: map[string]string{
		"Xray-linux-64.zip": "https://example.invalid/a.zip",
	}}
	if _, _, err := r.AssetFor("linux/amd64"); err == nil {
		t.Fatal("an asset with no published checksum was accepted")
	}
}

// GitHub returns creation order, which is usually but not always version
// order. A re-published older tag must not present itself as an upgrade to the
// whole fleet.
func TestLatestPicksByVersionNotByListOrder(t *testing.T) {
	srv := releaseServer(t, []stubRelease{
		{Tag: "v26.3.27", Prerelease: false}, // listed first, older
		{Tag: "v26.10.1", Prerelease: false},
		{Tag: "v26.9.1", Prerelease: false},
	})
	defer srv.Close()

	c := Client{HTTP: srv.Client()}
	got, err := c.listFrom(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	best := pickLatest(got, false)
	if best.Version != "26.10.1" {
		t.Fatalf("Latest = %q, want 26.10.1", best.Version)
	}
}

func TestPrereleasesAreIncludedOnlyWhenAsked(t *testing.T) {
	rels := []Release{
		{Version: "26.3.27", Prerelease: false},
		{Version: "26.10.1", Prerelease: true},
	}
	if got := pickLatest(rels, true); got.Version != "26.10.1" {
		t.Errorf("with prereleases: %q, want 26.10.1", got.Version)
	}
	if got := pickLatest(rels, false); got.Version != "26.3.27" {
		t.Errorf("without prereleases: %q, want 26.3.27", got.Version)
	}
}

func TestChecksumFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(realDgst))
	}))
	defer srv.Close()

	c := Client{HTTP: srv.Client()}
	got, err := c.Checksum(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got != "aa11c3685c71da0ffc71e511db50404609e7e963bb914b048f59a6a00af8930e" {
		t.Fatalf("Checksum = %q", got)
	}
}

// Rate limiting is the failure an operator will actually hit, and it looks like
// nothing at all unless it is named.
func TestRateLimitingIsReportedAsItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded for 203.0.113.7."}`))
	}))
	defer srv.Close()

	c := Client{HTTP: srv.Client()}
	_, err := c.listFrom(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("a rate-limited response was not an error")
	}
	if want := "rate limit"; !contains(err.Error(), want) {
		t.Fatalf("error %q does not mention %q", err, want)
	}
}

// The live check: the parsers above are only worth anything if upstream still
// looks the way they assume. Skipped without network.
func TestAgainstTheRealGitHubAPI(t *testing.T) {
	if os.Getenv("CHIRAL_NETWORK_TESTS") == "" {
		t.Skip("set CHIRAL_NETWORK_TESTS=1 to query GitHub")
	}
	c := Client{Token: os.Getenv("CHIRAL_GITHUB_TOKEN")}
	rel, err := c.Latest(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version == "" {
		t.Fatal("no version")
	}
	if rel.Version[0] == 'v' {
		t.Fatalf("Version = %q still carries the tag prefix", rel.Version)
	}
	archive, digest, err := rel.AssetFor("linux/amd64")
	if err != nil {
		t.Fatalf("the linux/amd64 asset naming has changed: %v", err)
	}
	sum, err := c.Checksum(context.Background(), digest)
	if err != nil {
		t.Fatalf("the .dgst format has changed: %v", err)
	}
	if len(sum) != 64 {
		t.Fatalf("checksum %q is not a SHA-256", sum)
	}
	t.Logf("latest is %s (%s), archive %s, sha256 %s", rel.Version, rel.Tag, archive, sum)
}

// --- helpers ---

type stubRelease struct {
	Tag        string
	Prerelease bool
}

func releaseServer(t *testing.T, rels []stubRelease) *httptest.Server {
	t.Helper()
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	}
	type rel struct {
		TagName    string  `json:"tag_name"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}
	out := make([]rel, 0, len(rels))
	for _, r := range rels {
		out = append(out, rel{TagName: r.Tag, Prerelease: r.Prerelease, Assets: []asset{
			{Name: "Xray-linux-64.zip", URL: "https://example.invalid/" + r.Tag + ".zip"},
			{Name: "Xray-linux-64.zip.dgst", URL: "https://example.invalid/" + r.Tag + ".zip.dgst"},
		}})
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(out)
	}))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

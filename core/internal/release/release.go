// Package release discovers Xray-core builds on GitHub: which version is
// newest, where its archive lives, and what it should hash to.
//
// The panel tracks prereleases deliberately (CLAUDE.md decision 9): xhttp
// up/down split, post-quantum REALITY and most of what the templates are built
// around exist only there, and stable tags are sparse enough that following
// them means following nothing for months at a time.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/template"
)

// Repo is the upstream. Not configurable: a panel that can be pointed at an
// arbitrary repository is a panel that can be told to install an arbitrary
// binary on every node it manages.
const Repo = "XTLS/Xray-core"

const apiTimeout = 30 * time.Second

// Release is one upstream build.
type Release struct {
	// Version is canonical (no leading "v"); Tag is what GitHub calls it.
	Version     string    `json:"version"`
	Tag         string    `json:"tag"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	// assets maps an asset filename to its download URL.
	assets map[string]string
}

// AssetFor returns the archive URL and its .dgst URL for a platform string of
// the form "linux/amd64".
func (r Release) AssetFor(platform string) (archive, digest string, err error) {
	name, ok := assetName(platform)
	if !ok {
		return "", "", fmt.Errorf("no Xray release asset is published for %s", platform)
	}
	archive, ok = r.assets[name]
	if !ok {
		return "", "", fmt.Errorf("release %s has no asset named %s", r.Tag, name)
	}
	digest, ok = r.assets[name+".dgst"]
	if !ok {
		// Refuse rather than fall through to an unverified download: the
		// checksum is the only thing standing between "we fetched 20 MB from
		// the internet" and "we ran it as root on every node".
		return "", "", fmt.Errorf("release %s publishes %s but no checksum for it", r.Tag, name)
	}
	return archive, digest, nil
}

// Platforms the panel can upgrade. Xray publishes far more; these are the ones
// a node running this agent could plausibly be, and an unlisted platform gets a
// clear error rather than a guessed asset name.
var assets = map[string]string{
	"linux/amd64":   "Xray-linux-64.zip",
	"linux/arm64":   "Xray-linux-arm64-v8a.zip",
	"linux/386":     "Xray-linux-32.zip",
	"linux/arm":     "Xray-linux-arm32-v7a.zip",
	"linux/riscv64": "Xray-linux-riscv64.zip",
	"linux/s390x":   "Xray-linux-s390x.zip",
	"linux/ppc64le": "Xray-linux-ppc64le.zip",
	"linux/loong64": "Xray-linux-loong64.zip",
	"darwin/amd64":  "Xray-macos-64.zip",
	"darwin/arm64":  "Xray-macos-arm64-v8a.zip",
	"freebsd/amd64": "Xray-freebsd-64.zip",
	"freebsd/arm64": "Xray-freebsd-arm64-v8a.zip",
}

func assetName(platform string) (string, bool) {
	n, ok := assets[strings.TrimSpace(platform)]
	return n, ok
}

// Client fetches release metadata.
type Client struct {
	HTTP *http.Client
	// Token is an optional GitHub token. Unauthenticated requests are limited
	// to 60/hour per IP, which a panel polling every few hours will never
	// approach — but a shared egress IP might, and then upgrades silently stop
	// being offered.
	Token string
}

func (c Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: apiTimeout}
}

// Latest returns the newest release, counting prereleases when asked.
//
// "Newest" is decided by version order, not by GitHub's list order or by
// published_at. GitHub returns creation order, which is usually but not always
// the same thing, and a re-published older tag would otherwise present itself
// as an upgrade to the whole fleet.
func (c Client) Latest(ctx context.Context, includePrerelease bool) (Release, error) {
	all, err := c.List(ctx, 10)
	if err != nil {
		return Release{}, err
	}
	best := pickLatest(all, includePrerelease)
	if best.Version == "" {
		return Release{}, fmt.Errorf("no suitable %s release found", Repo)
	}
	return best, nil
}

func pickLatest(all []Release, includePrerelease bool) Release {
	var best Release
	for _, r := range all {
		if r.Prerelease && !includePrerelease {
			continue
		}
		if best.Version == "" || template.CompareVersions(r.Version, best.Version) > 0 {
			best = r
		}
	}
	return best
}

// List returns up to n recent releases, newest first as GitHub orders them.
func (c Client) List(ctx context.Context, n int) ([]Release, error) {
	if n <= 0 || n > 100 {
		n = 10
	}
	return c.listFrom(ctx, fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d", Repo, n))
}

// listFrom is List with the URL injected, so the parsing and the error mapping
// can be exercised without reaching GitHub.
func (c Client) listFrom(ctx context.Context, url string) ([]Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("querying %s releases: %w", Repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		// Rate limiting is the failure an operator will actually hit, and it
		// looks like nothing at all unless it is named.
		if resp.StatusCode == http.StatusForbidden && strings.Contains(string(body), "rate limit") {
			return nil, fmt.Errorf("GitHub rate limit reached; set CHIRAL_GITHUB_TOKEN to raise it")
		}
		return nil, fmt.Errorf("GitHub returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var raw []struct {
		TagName     string    `json:"tag_name"`
		Prerelease  bool      `json:"prerelease"`
		Draft       bool      `json:"draft"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decoding the release list: %w", err)
	}

	out := make([]Release, 0, len(raw))
	for _, r := range raw {
		if r.Draft {
			continue
		}
		rel := Release{
			Version:     template.NormalizeVersion(r.TagName),
			Tag:         r.TagName,
			Prerelease:  r.Prerelease,
			PublishedAt: r.PublishedAt,
			assets:      make(map[string]string, len(r.Assets)),
		}
		for _, a := range r.Assets {
			rel.assets[a.Name] = a.URL
		}
		out = append(out, rel)
	}
	return out, nil
}

// Checksum fetches a .dgst file and returns the SHA-256 as lowercase hex.
//
// The format is Xray's own, verified against the real file rather than assumed:
// one "ALGO= hex" line per algorithm, and the SHA-256 line is labelled
// "SHA2-256", not "SHA256". Matching the wrong label would leave the checksum
// empty, and an empty checksum that is treated as "nothing to compare" is how
// verification quietly stops happening.
func (c Client) Checksum(ctx context.Context, dgstURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dgstURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching the checksum: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksum fetch returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return "", err
	}
	sum := ParseDigest(string(body))
	if sum == "" {
		return "", fmt.Errorf("no SHA2-256 line in %s", dgstURL)
	}
	return sum, nil
}

// ParseDigest pulls the SHA-256 out of a .dgst file's contents.
func ParseDigest(body string) string {
	for _, line := range strings.Split(body, "\n") {
		label, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(label) {
		case "SHA2-256", "SHA256":
			v := strings.ToLower(strings.TrimSpace(value))
			if len(v) == 64 && isHex(v) {
				return v
			}
		}
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

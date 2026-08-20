package ruleset

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// maxListBytes bounds one rule list. The largest ACL4SSR list is around 40 kB;
// 8 MB is far above anything legitimate and still small enough that a
// misconfigured URL pointing at a disk image cannot exhaust memory.
const maxListBytes = 8 << 20

// fetchTimeout bounds a single request. Refreshing thirty-odd lists happens in
// the background, so being patient costs nothing an operator waits on.
const fetchTimeout = 30 * time.Second

// Service fetches and caches the rule sources.
//
// Fetching is deliberately never on the subscription path. A subscriber
// pulling their config must not wait on GitHub, and must not fail when GitHub
// is unreachable — which, for the people this software exists for, is the
// normal state of the network until the proxy they are configuring works.
type Service struct {
	st     *store.Store
	http   *http.Client
	token  string
	logger *slog.Logger

	// refreshing serialises refreshes so a manual one during the scheduled
	// sweep does not double every request.
	refreshing sync.Mutex
}

func NewService(st *store.Store, githubToken string, logger *slog.Logger) *Service {
	return &Service{
		st:     st,
		http:   &http.Client{Timeout: fetchTimeout},
		token:  githubToken,
		logger: logger,
	}
}

// Refresh re-fetches one ruleset's .ini and every list it references.
//
// Partial failure is normal and is not fatal: one unreachable list leaves the
// previous copy in place and is reported, because a ruleset that renders with
// eleven of its twelve lists is far better than one that does not render.
func (s *Service) Refresh(ctx context.Context, id string) error {
	s.refreshing.Lock()
	defer s.refreshing.Unlock()

	r, err := s.st.GetRuleset(id)
	if err != nil {
		return err
	}

	ini, err := s.fetch(ctx, r.URL, "")
	if err != nil {
		// Recorded, not returned as a hard failure: the previous .ini stays
		// and the ruleset keeps rendering.
		_ = s.st.SaveRulesetFetch(id, "", err.Error())
		return fmt.Errorf("fetching %s: %w", r.URL, err)
	}
	cfg, perr := ParseINI(ini.body)
	if perr != nil {
		_ = s.st.SaveRulesetFetch(id, "", perr.Error())
		return fmt.Errorf("parsing %s: %w", r.URL, perr)
	}
	if err := s.st.SaveRulesetFetch(id, ini.body, ""); err != nil {
		return err
	}

	var failed []string
	for _, url := range cfg.ListURLs() {
		if err := s.refreshList(ctx, url); err != nil {
			failed = append(failed, url)
			s.logger.Warn("rule list refresh failed", "url", url, "err", err)
		}
	}
	if len(failed) > 0 {
		msg := fmt.Sprintf("%d 个规则列表未能更新，沿用上一次的内容", len(failed))
		_ = s.st.SaveRulesetFetch(id, ini.body, msg)
	}
	return nil
}

// RefreshAll updates every ruleset, then drops cached lists nothing references.
func (s *Service) RefreshAll(ctx context.Context) {
	sets, err := s.st.ListRulesets()
	if err != nil {
		s.logger.Error("listing rulesets failed", "err", err)
		return
	}
	keep := map[string]struct{}{}
	for _, r := range sets {
		if err := s.Refresh(ctx, r.ID); err != nil {
			s.logger.Warn("ruleset refresh failed", "ruleset", r.Name, "err", err)
		}
		// Read back: Refresh may have replaced the .ini, and the URL set that
		// matters is the one now stored.
		if cur, err := s.st.GetRuleset(r.ID); err == nil && cur.INI != "" {
			if cfg, err := ParseINI(cur.INI); err == nil {
				for _, u := range cfg.ListURLs() {
					keep[u] = struct{}{}
				}
			}
		}
	}
	if n, err := s.st.PruneRuleLists(keep); err != nil {
		s.logger.Warn("pruning rule lists failed", "err", err)
	} else if n > 0 {
		s.logger.Info("dropped unreferenced rule lists", "count", n)
	}
}

// refreshList fetches one list, conditionally when we already hold a copy.
func (s *Service) refreshList(ctx context.Context, url string) error {
	prev, err := s.st.GetRuleList(url)
	etag := ""
	if err == nil {
		etag = prev.ETag
	}
	got, err := s.fetch(ctx, url, etag)
	if err != nil {
		return err
	}
	if got.notModified {
		return nil
	}
	return s.st.PutRuleList(store.RuleList{URL: url, Body: got.body, ETag: got.etag})
}

type fetched struct {
	body        string
	etag        string
	notModified bool
}

func (s *Service) fetch(ctx context.Context, url, etag string) (fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fetched{}, err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	// Only for api.github.com; raw.githubusercontent.com serves these
	// anonymously and rate-limits by IP, which one panel never approaches.
	if s.token != "" && strings.Contains(url, "api.github.com") {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fetched{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return fetched{notModified: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return fetched{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBytes+1))
	if err != nil {
		return fetched{}, err
	}
	if len(body) > maxListBytes {
		return fetched{}, fmt.Errorf("larger than %d bytes", maxListBytes)
	}
	return fetched{body: string(body), etag: resp.Header.Get("ETag")}, nil
}

// ProviderYAML converts a cached rule list into the payload a Clash
// rule-provider expects.
//
// The .list format is one rule per line with comments; a classical provider
// wants a YAML list of the same rules. Converting here rather than storing the
// converted form keeps the cache a faithful copy of upstream, so a change in
// what Clash accepts does not require re-fetching everything.
func ProviderYAML(list string) string {
	var b strings.Builder
	b.WriteString("payload:\n")
	for _, line := range strings.Split(list, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") ||
			strings.HasPrefix(line, "//") {
			continue
		}
		// Some lists carry a trailing policy (`DOMAIN,x,DIRECT`) or the
		// no-resolve flag. A classical payload entry must not carry a policy —
		// the provider's rule supplies it — but no-resolve belongs to the rule
		// and has to survive.
		b.WriteString("  - ")
		b.WriteString(quoteYAML(line))
		b.WriteString("\n")
	}
	return b.String()
}

// quoteYAML wraps a rule so YAML cannot reinterpret it. Rules contain commas
// and colons; unquoted, `IP-CIDR,10.0.0.0/8` is a plain scalar but
// `DOMAIN,a: b` is a mapping, and one malformed entry rejects the whole
// provider file.
func quoteYAML(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

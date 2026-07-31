package external

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

// maxBodyBytes bounds one fetched subscription. A large provider list is tens
// of kilobytes; four megabytes is far past anything real and still small
// enough that a URL pointing at the wrong thing cannot exhaust memory.
const maxBodyBytes = 4 << 20

const fetchTimeout = 30 * time.Second

// Service fetches external subscriptions and keeps their proxies in step.
type Service struct {
	st     *store.Store
	http   *http.Client
	logger *slog.Logger
	mu     sync.Mutex
}

func NewService(st *store.Store, logger *slog.Logger) *Service {
	return &Service{st: st, http: &http.Client{Timeout: fetchTimeout}, logger: logger}
}

// Refresh re-fetches one source and reparses it.
//
// A source with no URL was pasted rather than linked; its body is whatever the
// operator supplied, so there is nothing to fetch and it is only reparsed.
func (s *Service) Refresh(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	sub, err := s.st.GetExternalSub(id)
	if err != nil {
		return err
	}
	body := sub.Body
	if sub.URL != "" {
		fetched, err := s.fetch(ctx, sub.URL)
		if err != nil {
			// Recorded, not fatal: the stored body keeps serving.
			_ = s.st.SaveExternalFetch(id, "", err.Error())
			return fmt.Errorf("fetching %s: %w", sub.URL, err)
		}
		body = fetched
	}

	proxies, err := Parse(body)
	if err != nil {
		_ = s.st.SaveExternalFetch(id, "", err.Error())
		return err
	}
	rows := make([]store.ExternalProxy, 0, len(proxies))
	for _, p := range proxies {
		yamlBody, err := RenderYAML(p, "")
		if err != nil {
			continue
		}
		rows = append(rows, store.ExternalProxy{
			Name: p.Name, Type: p.Type, Server: p.Server, Port: p.Port, Config: yamlBody,
		})
	}
	if len(rows) == 0 {
		msg := "解析后没有可用节点"
		_ = s.st.SaveExternalFetch(id, "", msg)
		return fmt.Errorf("%s", msg)
	}
	if err := s.st.ReplaceExternalProxies(id, rows); err != nil {
		return err
	}
	return s.st.SaveExternalFetch(id, body, "")
}

// RefreshAll updates every source that has a URL.
func (s *Service) RefreshAll(ctx context.Context) {
	subs, err := s.st.ListExternalSubs()
	if err != nil {
		s.logger.Error("listing external subscriptions failed", "err", err)
		return
	}
	for _, sub := range subs {
		if sub.URL == "" {
			continue
		}
		if err := s.Refresh(ctx, sub.ID); err != nil {
			s.logger.Warn("external subscription refresh failed", "name", sub.Name, "err", err)
		}
	}
}

func (s *Service) fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	// Providers serve a different format per client, and several serve
	// nothing useful to a bare Go user agent. Asking as clash is what gets a
	// clash document, which is the richest of the formats they offer.
	req.Header.Set("User-Agent", "clash-verge/1.7.0")
	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxBodyBytes {
		return "", fmt.Errorf("larger than %d bytes", maxBodyBytes)
	}
	return string(body), nil
}

// Fragments renders the enabled external proxies as clash proxies-list items.
//
// chainName resolves a fleet node id to the name that node appears under in
// this subscriber's own proxies — the chain has to reference a proxy the same
// document defines, and that name is the customer-facing one, not the node id.
func (s *Service) Fragments(chainName func(nodeID string) string) ([]string, error) {
	proxies, err := s.st.EnabledExternalProxies()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(proxies))
	for _, p := range proxies {
		body := p.Config
		if p.ChainNodeID != "" {
			via := chainName(p.ChainNodeID)
			if via == "" {
				// The chain node is gone or is not in this subscription.
				// Emitting the proxy without its chain would quietly send the
				// subscriber straight at the provider, which is the one thing
				// the setting exists to prevent, so it is left out instead.
				continue
			}
			body = appendYAMLKey(body, "dialer-proxy", via)
		}
		out = append(out, body)
	}
	return out, nil
}

// appendYAMLKey adds one scalar entry to a rendered mapping.
//
// Text rather than re-marshalling: the stored config is what the provider sent
// and round-tripping it through a parser for one added key risks changing
// values this panel does not understand.
func appendYAMLKey(body, key, value string) string {
	return strings.TrimRight(body, "\n") + "\n" + key + ": " + quoteYAML(value)
}

// quoteYAML wraps a value so YAML reads it as the string it is. Node names
// carry emoji, spaces, colons and the occasional digit-only label.
func quoteYAML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

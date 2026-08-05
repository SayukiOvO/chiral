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

	proxies, skipped, err := Parse(body)
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
	// Served, but short. A source that quietly yields seven nodes out of ten
	// looks exactly like a source with seven nodes, and the count on the card
	// agrees with itself either way — so the reasons go where the operator is
	// already looking for trouble with this source.
	if len(skipped) > 0 {
		s.logger.Warn("some nodes in an external source could not be read",
			"source", sub.Name, "skipped", len(skipped), "kept", len(rows))
		return s.st.SaveExternalFetch(id, body,
			fmt.Sprintf("%d 个节点无法解析，已跳过：%s", len(skipped), strings.Join(skipped, "；")))
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

// Fragment is one rendered proxy with the place the operator gave it.
//
// The order travels with the body because the assembler interleaves these with
// the fleet's own, and it cannot recover a proxy's position from the YAML.
type Fragment struct {
	Body string
	// Order is the operator's position, 0 for never placed.
	Order int
	// Tie keeps the unplaced tail stable and reproducible.
	Tie string
}

// Fragments renders the enabled external proxies as clash proxies-list items.
//
// denied names the proxies this subscriber may not use. chainName resolves a
// fleet node id to the name that node appears under in this subscriber's own
// proxies — the chain has to reference a proxy the same
// document defines, and that name is the customer-facing one, not the node id.
func (s *Service) Fragments(denied map[string]struct{}, chainName func(nodeID string) string) ([]Fragment, error) {
	proxies, carried, _, err := s.carried(denied, chainName)
	if err != nil {
		return nil, err
	}
	rank := make(map[string]int, len(proxies))
	for i, p := range proxies {
		rank[p.ID] = i
	}
	out := make([]Fragment, 0, len(carried))
	for _, p := range proxies {
		if _, ok := carried[p.ID]; !ok {
			continue
		}
		body := p.Config
		if label := p.Label(); label != p.Name {
			body = withName(body, label)
		}
		if target, external, chained := p.ChainTarget(); chained {
			via := chainName(target)
			if external {
				// The label, not the provider's name: a dialer-proxy naming
				// something the document does not define makes the whole
				// configuration unloadable, and the document defines labels.
				via = carried[target].Label()
			}
			body = appendYAMLKey(body, "dialer-proxy", via)
		}
		// Prefixed "1" so every external node follows every fleet one while
		// nothing has been placed, and ranked by the same query the console
		// lists them from, so the two agree row for row.
		out = append(out, Fragment{Body: body, Order: p.SortOrder, Tie: fmt.Sprintf("1:%08d", rank[p.ID])})
	}
	return out, nil
}

// ChainRef names what a proxy is dialled through.
type ChainRef struct {
	ID       string
	External bool
}

// Blocked reports the external proxies this subscriber would otherwise get but
// cannot, each mapped to the chain target it is missing. The console uses it to
// show a node as unreachable instead of leaving its toggle looking on; the
// subscription itself takes the same answer from the same code, so the two
// cannot drift.
func (s *Service) Blocked(denied map[string]struct{}, chainName func(nodeID string) string) (map[string]ChainRef, error) {
	_, _, blocked, err := s.carried(denied, chainName)
	return blocked, err
}

// carried resolves which proxies survive their chains.
//
// Carried starts as everything this subscriber is allowed, then loses whatever
// cannot resolve its chain — including anything chained through something that
// just dropped out. One pass is not enough now that a proxy can be dialled
// through another proxy: A through B through a fleet node the subscriber does
// not have means A must go too, and A may be visited before B. Iterating to a
// fixpoint settles that regardless of order, and terminates because each round
// either removes a proxy or stops.
func (s *Service) carried(denied map[string]struct{}, chainName func(nodeID string) string) (
	all []store.ExternalProxy, carried map[string]store.ExternalProxy, blocked map[string]ChainRef, err error) {
	all, err = s.st.EnabledExternalProxies()
	if err != nil {
		return nil, nil, nil, err
	}
	carried = make(map[string]store.ExternalProxy, len(all))
	for _, p := range all {
		if _, no := denied[p.ID]; no {
			continue
		}
		// A relayed proxy is not a line in anybody's subscription: a node of
		// this fleet carries it, and the subscriber reaches it by connecting
		// to that node. Handing them the provider's address as well would
		// give back everything relaying was for.
		if p.Relayed() && !p.RelayExposed {
			continue
		}
		carried[p.ID] = p
	}
	blocked = map[string]ChainRef{}
	for changed := true; changed; {
		changed = false
		for id, p := range carried {
			target, external, chained := p.ChainTarget()
			if !chained {
				continue
			}
			ok := false
			if external {
				_, ok = carried[target]
			} else {
				ok = chainName(target) != ""
			}
			if !ok {
				// The chain target is gone, disabled, denied to this
				// subscriber, or dropped for the same reason one step further
				// along. Emitting the proxy without its chain would quietly
				// send them straight at the provider, which is the one thing
				// the setting exists to prevent, so it is left out instead.
				delete(carried, id)
				blocked[id] = ChainRef{ID: target, External: external}
				changed = true
			}
		}
	}
	return all, carried, blocked, nil
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

// withName swaps the proxy's name for the one the operator chose.
//
// A replacement rather than an appended key: the stored body already carries a
// `name`, and a second one is a duplicate mapping key — which yaml.v3 rejects
// outright and other readers resolve by picking one, so the document would
// either fail to load or load under a name nothing else in it refers to.
//
// Only column zero is touched. `name` also appears nested inside transport
// options, and those belong to the provider.
func withName(body, label string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "name:") {
			lines[i] = "name: " + quoteYAML(label)
			return strings.Join(lines, "\n")
		}
	}
	return body
}

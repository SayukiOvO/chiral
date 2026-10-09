// Package equivalence renders private, offline migration evidence. It never
// starts Core, refreshes external sources, repairs data, or contacts an Agent.
package equivalence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/api"
	"github.com/SayukiOvO/chiral/core/internal/auth"
	"github.com/SayukiOvO/chiral/core/internal/external"
	"github.com/SayukiOvO/chiral/core/internal/profile"
	"github.com/SayukiOvO/chiral/core/internal/ruleset"
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/subscription"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

type Options struct {
	At        time.Time
	PublicURL string
	Revision  string
}

// Bundle contains plaintext credentials. It is not a public report. Database
// identity and renderer revision are provenance, not fields that must match
// across a migration; record bodies and successful response headers must match.
type Bundle struct {
	Version        int      `json:"version"`
	At             int64    `json:"at"`
	PublicURL      string   `json:"public_url"`
	Revision       string   `json:"revision"`
	DatabaseSHA256 string   `json:"database_sha256"`
	Users          int      `json:"users"`
	Nodes          int      `json:"nodes"`
	Records        []Record `json:"records"`
}

type Record struct {
	Kind        string              `json:"kind"`
	ID          string              `json:"id"`
	Client      string              `json:"client,omitempty"`
	TokenSHA256 string              `json:"token_sha256,omitempty"`
	Body        []byte              `json:"body"`
	Headers     map[string][]string `json:"headers,omitempty"`
	Fragments   int                 `json:"fragments,omitempty"`
	Issues      []string            `json:"issues,omitempty"`
	Version     int64               `json:"version,omitempty"`
}

// Export requires a sealed SQLite backup opened by store.OpenSnapshot. All
// diagnostics are fixed codes; raw template/SQL errors can contain secrets.
func Export(st *store.Store, opts Options) (Bundle, error) {
	identity, ok := st.SnapshotIdentity()
	if !ok {
		return Bundle{}, errors.New("a read-only snapshot is required")
	}
	u, err := url.Parse(opts.PublicURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || opts.At.Unix() <= 0 || strings.TrimSpace(opts.Revision) == "" {
		return Bundle{}, errors.New("explicit public URL, positive observation time and renderer revision are required")
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	usersService, err := user.NewSnapshotService(st, logger, opts.At)
	if err != nil {
		return Bundle{}, errors.New("cannot initialize snapshot rendering")
	}
	profiles := profile.NewService(st, nil, nil, nil, usersService, logger)
	routing := &checkedRouting{service: ruleset.NewService(st, "", logger)}
	externals := &checkedExternals{service: external.NewService(st, logger)}
	subs := subscription.NewService(st, profiles)
	subs.EnableRouting(routing, opts.PublicURL)
	subs.EnableExternals(externals)
	users, err := st.ListUsers()
	if err != nil {
		return Bundle{}, errors.New("cannot enumerate snapshot users")
	}
	nodes, err := st.ListNodes()
	if err != nil {
		return Bundle{}, errors.New("cannot enumerate snapshot nodes")
	}
	// These queries are otherwise treated as optional ordering hints by Render.
	if _, err := st.EnabledExternalProxies(); err != nil {
		return Bundle{}, errors.New("cannot enumerate snapshot external proxies")
	}
	if _, err := st.ListNodeRelays(); err != nil {
		return Bundle{}, errors.New("cannot enumerate snapshot relays")
	}
	if _, err := st.Settings(); err != nil {
		return Bundle{}, errors.New("cannot read snapshot settings")
	}
	b := Bundle{Version: 1, At: opts.At.Unix(), PublicURL: opts.PublicURL, Revision: opts.Revision,
		DatabaseSHA256: identity.SHA256, Users: len(users), Nodes: len(nodes)}
	for _, n := range nodes {
		r := Record{Kind: "node_rendered", ID: n.ID}
		body, err := profiles.AssembleNode(n.ID)
		if err != nil {
			r.Issues = []string{"node_render_failed"}
		} else {
			r.Body = body
		}
		b.Records = append(b.Records, r)
		r = Record{Kind: "node_stored", ID: n.ID}
		c, err := st.LatestPushableConfig(n.ID)
		if err != nil {
			r.Issues = []string{"stored_config_unavailable"}
		} else {
			r.Body, r.Version = []byte(c.Config), c.Version
		}
		// ProbeOutbound is deliberately out of scope: the normal store reader
		// can omit an undecryptable probe, which must not become false evidence.
		b.Records = append(b.Records, r)
	}
	for _, u := range users {
		token, tokenErr := st.SubToken(u.ID)
		if tokenErr == nil && token != "" {
			found, err := st.FindUserBySubTokenHash(auth.HashSecret(token))
			if err != nil || found.ID != u.ID {
				tokenErr = errors.New("token identity mismatch")
			}
		}
		for _, kind := range subscription.Kinds() {
			r := Record{Kind: "subscription", ID: u.ID, Client: kind}
			if tokenErr != nil || token == "" {
				r.Issues = []string{"subscription_token_unavailable"}
				b.Records = append(b.Records, r)
				continue
			}
			r.TokenSHA256 = auth.HashSecret(token)
			if err := checkCredentials(st, u.ID, kind); err != nil {
				r.Issues = append(r.Issues, "credential_inventory_incomplete")
			}
			routing.failed, externals.failed = false, false
			result, err := subs.Render(u, kind, token)
			if err != nil {
				r.Issues = append(r.Issues, "subscription_render_failed")
			} else {
				r.Body, r.Fragments = []byte(result.Body), result.Fragments
				if result.Fragments == 0 {
					r.Issues = append(r.Issues, "empty_subscription")
				} else {
					r.Headers = api.SubscriptionSuccessHeaders(u, result, opts.At)
				}
				if len(result.Skipped) > 0 {
					r.Issues = append(r.Issues, "subscription_fragments_skipped")
				}
			}
			if routing.failed {
				r.Issues = append(r.Issues, "routing_render_failed")
			}
			if externals.failed {
				r.Issues = append(r.Issues, "external_render_failed")
			}
			b.Records = append(b.Records, r)
		}
		b.Records = append(b.Records, rulePayloads(st, u)...)
	}
	sortRecords(b.Records)
	if err := st.VerifySnapshot(); err != nil {
		return Bundle{}, errors.New("snapshot changed during export")
	}
	return b, nil
}

// Check the same served and denied sets before Render can skip absent
// credentials. No entitlement is created, inferred or restored here.
func checkCredentials(st *store.Store, uid, kind string) error {
	pids, err := st.UserProfileIDs(uid)
	if err != nil {
		return err
	}
	denied, err := st.UserNodeDenies(uid)
	if err != nil {
		return err
	}
	deniedProxies, err := st.UserExternalDenies(uid)
	if err != nil {
		return err
	}
	deniedRelays, err := st.UserRelayDenies(uid)
	if err != nil {
		return err
	}
	exits, err := st.EnabledExternalProxies()
	if err != nil {
		return err
	}
	for _, pid := range pids {
		templates, err := st.ServedClientTemplates(pid)
		if err != nil {
			return err
		}
		if templates[kind] == "" {
			continue
		} // Deliberately hidden/not offered.
		nids, err := st.ProfileNodeIDs(pid)
		if err != nil {
			return err
		}
		for _, nid := range nids {
			if _, no := denied[nid]; no {
				continue
			}
			if _, err := st.FindCredential(uid, pid, nid, ""); err != nil {
				return err
			}
			for _, ex := range exits {
				if ex.ChainNodeID != nid {
					continue
				}
				if _, no := deniedProxies[ex.ID]; no {
					continue
				}
				if _, err := st.FindCredential(uid, pid, nid, ex.ID); err != nil {
					return err
				}
			}
			relays, err := st.RelaysFromEntry(nid)
			if err != nil {
				return err
			}
			for _, rl := range relays {
				if _, no := deniedRelays[rl.ID]; no {
					continue
				}
				if _, err := st.FindCredentialForExit(uid, pid, nid, "", rl.ID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func rulePayloads(st *store.Store, u store.User) []Record {
	if u.EffectiveRulesetID == "" {
		return nil
	}
	failure := []Record{{Kind: "rule_payload", ID: u.ID, Issues: []string{"rule_cache_incomplete"}}}
	r, err := st.GetRuleset(u.EffectiveRulesetID)
	if err != nil || r.INI == "" {
		return failure
	}
	cfg, err := ruleset.ParseINI(r.INI)
	if err != nil {
		return failure
	}
	var out []Record
	for _, address := range cfg.ListURLs() {
		sum := sha256.Sum256([]byte(address))
		record := Record{Kind: "rule_payload", ID: u.ID, Client: hex.EncodeToString(sum[:])}
		list, err := st.GetRuleList(address)
		if err != nil {
			record.Issues = []string{"rule_cache_incomplete"}
		} else {
			record.Body = []byte(ruleset.ProviderYAML(list.Body))
		}
		out = append(out, record)
	}
	return out
}

type checkedRouting struct {
	service *ruleset.Service
	failed  bool
}

func (r *checkedRouting) For(u store.User, names []string, base string) (string, string, string, error) {
	g, p, rules, err := r.service.For(u, names, base)
	r.failed = r.failed || err != nil
	return g, p, rules, err
}

type checkedExternals struct {
	service *external.Service
	failed  bool
}

func (e *checkedExternals) Fragments(denied map[string]struct{}, chain func(string) string) ([]external.Fragment, error) {
	f, err := e.service.Fragments(denied, chain)
	e.failed = e.failed || err != nil
	return f, err
}

func recordKey(r Record) string { return r.Kind + "\x00" + r.ID + "\x00" + r.Client }
func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool { return recordKey(records[i]) < recordKey(records[j]) })
}

// Complete rejects empty evidence and duplicate/missing inventory independently
// of equality. Identical broken exports are not a passing migration check.
func (b Bundle) Complete() bool {
	if b.Version != 1 || b.Users <= 0 || b.Nodes <= 0 || b.At <= 0 || b.PublicURL == "" || b.Revision == "" || !validSHA256(b.DatabaseSHA256) {
		return false
	}
	seen := map[string]bool{}
	users := map[string]map[string]bool{}
	nodes := map[string]map[string]bool{}
	for _, r := range b.Records {
		key := recordKey(r)
		if seen[key] || r.ID == "" || len(r.Issues) != 0 || len(r.Body) == 0 {
			return false
		}
		seen[key] = true
		switch r.Kind {
		case "subscription":
			if r.Fragments <= 0 || len(r.Headers) == 0 || !validSHA256(r.TokenSHA256) {
				return false
			}
			if users[r.ID] == nil {
				users[r.ID] = map[string]bool{}
			}
			users[r.ID][r.Client] = true
		case "node_rendered", "node_stored":
			if r.Client != "" || (r.Kind == "node_stored" && r.Version <= 0) {
				return false
			}
			if nodes[r.ID] == nil {
				nodes[r.ID] = map[string]bool{}
			}
			nodes[r.ID][r.Kind] = true
		case "rule_payload":
			if len(r.Client) != 64 {
				return false
			}
		default:
			return false
		}
	}
	if len(users) != b.Users || len(nodes) != b.Nodes {
		return false
	}
	for _, kinds := range users {
		if len(kinds) != len(subscription.Kinds()) {
			return false
		}
		for _, kind := range subscription.Kinds() {
			if !kinds[kind] {
				return false
			}
		}
	}
	for _, kinds := range nodes {
		if len(kinds) != 2 {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// Comparison exposes counts only, never credentials, tokens, addresses or
// template errors. Equal compares exact bytes, not normalized JSON or YAML.
type Comparison struct {
	Equal       bool `json:"equal"`
	Complete    bool `json:"complete"`
	Passed      bool `json:"passed"`
	Differences int  `json:"differences"`
}

func Compare(a, b Bundle) Comparison {
	c := Comparison{Complete: a.Complete() && b.Complete()}
	if a.Version != b.Version || a.At != b.At || a.PublicURL != b.PublicURL || a.Users != b.Users || a.Nodes != b.Nodes {
		c.Differences++
	}
	left, right := map[string]Record{}, map[string]Record{}
	for _, r := range a.Records {
		left[recordKey(r)] = r
	}
	for _, r := range b.Records {
		right[recordKey(r)] = r
	}
	if len(left) != len(a.Records) || len(right) != len(b.Records) {
		c.Differences++
	}
	for k, r := range left {
		if other, ok := right[k]; !ok || !reflect.DeepEqual(r, other) {
			c.Differences++
		}
	}
	for k := range right {
		if _, ok := left[k]; !ok {
			c.Differences++
		}
	}
	c.Equal = c.Differences == 0
	c.Passed = c.Equal && c.Complete
	return c
}

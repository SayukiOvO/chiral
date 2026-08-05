package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ExternalSub is a subscription belonging to someone else.
type ExternalSub struct {
	ID   string
	Name string
	// URL is empty when the operator pasted the body instead of pointing at a
	// link — the case for a single node sent over chat.
	URL       string
	Body      string
	Enabled   bool
	FetchedAt sql.NullInt64
	LastError string
	CreatedAt int64
	UpdatedAt int64
}

// ExternalProxy is one node from such a subscription.
type ExternalProxy struct {
	ID     string
	SubID  string
	Name   string
	Type   string
	Server string
	Port   int
	Config string
	// DisplayName is the operator's own label, empty when the provider's is
	// used as-is. Name stays the provider's whatever this says: it is what a
	// refresh matches on.
	DisplayName string
	// ChainNodeID is the fleet node this one is reached through, empty for a
	// direct dial. ChainProxyID is the same for another external node. At most
	// one of the two is set.
	ChainNodeID  string
	ChainProxyID string
	Enabled      bool
	// Ord is the provider's own position in the source it came from.
	Ord int
	// SortOrder is this proxy's place in the operator's single list, shared
	// with the fleet nodes. 0 means never placed; see migration 0022.
	SortOrder int
}

// Label is the name this proxy goes out under — into the subscription, and
// into any dialer-proxy that names it. One function, because a chain that
// referred to a node by a different name than the document defines makes the
// whole configuration unloadable.
func (p ExternalProxy) Label() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Name
}

// ChainTarget reports what this proxy is dialled through, if anything.
func (p ExternalProxy) ChainTarget() (id string, external bool, chained bool) {
	switch {
	case p.ChainProxyID != "":
		return p.ChainProxyID, true, true
	case p.ChainNodeID != "":
		return p.ChainNodeID, false, true
	}
	return "", false, false
}

const externalSubCols = `id, name, url, body, enabled, fetched_at, last_error, created_at, updated_at`

func scanExternalSub(row interface{ Scan(...any) error }) (ExternalSub, error) {
	var s ExternalSub
	err := row.Scan(&s.ID, &s.Name, &s.URL, &s.Body, &s.Enabled, &s.FetchedAt,
		&s.LastError, &s.CreatedAt, &s.UpdatedAt)
	return s, err
}

func (s *Store) CreateExternalSub(name, url, body string) (ExternalSub, error) {
	now := time.Now().Unix()
	e := ExternalSub{ID: NewID(), Name: name, URL: url, Body: body, Enabled: true,
		CreatedAt: now, UpdatedAt: now}
	_, err := s.db.Exec(`INSERT INTO external_subs (id, name, url, body, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?)`, e.ID, e.Name, e.URL, e.Body, e.CreatedAt, e.UpdatedAt)
	return e, err
}

func (s *Store) GetExternalSub(id string) (ExternalSub, error) {
	return scanExternalSub(s.db.QueryRow(`SELECT `+externalSubCols+` FROM external_subs WHERE id = ?`, id))
}

func (s *Store) ListExternalSubs() ([]ExternalSub, error) {
	rows, err := s.db.Query(`SELECT ` + externalSubCols + ` FROM external_subs ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExternalSub
	for rows.Next() {
		e, err := scanExternalSub(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) UpdateExternalSub(id, name, url string, enabled bool) error {
	res, err := s.db.Exec(`UPDATE external_subs SET name = ?, url = ?, enabled = ?, updated_at = ?
		WHERE id = ?`, name, url, enabled, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteExternalSub(id string) error {
	res, err := s.db.Exec(`DELETE FROM external_subs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SaveExternalFetch records a refresh outcome, keeping the previous body on
// failure so an unreachable provider degrades to yesterday's nodes rather than
// to none.
func (s *Store) SaveExternalFetch(id, body, failure string) error {
	now := time.Now().Unix()
	if failure != "" {
		_, err := s.db.Exec(`UPDATE external_subs SET last_error = ?, updated_at = ? WHERE id = ?`,
			failure, now, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE external_subs SET body = ?, fetched_at = ?, last_error = '', updated_at = ?
		WHERE id = ?`, body, now, now, id)
	return err
}

const externalProxyCols = `id, sub_id, name, COALESCE(display_name, ''), type, server, port, config, COALESCE(chain_node_id, ''), COALESCE(chain_proxy_id, ''), enabled, ord, sort_order`

func scanExternalProxy(row interface{ Scan(...any) error }) (ExternalProxy, error) {
	var p ExternalProxy
	err := row.Scan(&p.ID, &p.SubID, &p.Name, &p.DisplayName, &p.Type, &p.Server, &p.Port, &p.Config,
		&p.ChainNodeID, &p.ChainProxyID, &p.Enabled, &p.Ord, &p.SortOrder)
	return p, err
}

func (s *Store) ExternalProxies(subID string) ([]ExternalProxy, error) {
	rows, err := s.db.Query(`SELECT `+externalProxyCols+
		` FROM external_proxies WHERE sub_id = ? ORDER BY ord`, subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExternalProxy
	for rows.Next() {
		p, err := scanExternalProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// EnabledExternalProxies lists every proxy a subscription should carry, across
// all enabled sources, in a stable order.
func (s *Store) EnabledExternalProxies() ([]ExternalProxy, error) {
	rows, err := s.db.Query(`SELECT ` + externalProxyCols + ` FROM external_proxies p
		WHERE p.enabled = 1 AND EXISTS (
			SELECT 1 FROM external_subs e WHERE e.id = p.sub_id AND e.enabled = 1)
		ORDER BY p.sort_order = 0, p.sort_order, p.sub_id, p.ord`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExternalProxy
	for rows.Next() {
		p, err := scanExternalProxy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReplaceExternalProxies swaps in a freshly parsed set, carrying the
// operator's per-proxy settings across and keeping each row's identity.
//
// A refreshed proxy is matched to its previous row by name, and failing that
// by endpoint. Providers rename constantly — remaining traffic and expiry
// dates go in the label — so matching on name alone would detach every setting
// each time they edited a string, which is the one thing an operator would
// never think to check.
//
// Matched rows are UPDATED rather than replaced, so the id survives. Anything
// referencing a proxy — a per-user denial, and whatever comes later — hangs off
// that id with a cascading foreign key, and delete-then-insert would take those
// rows with it on every refresh.
func (s *Store) ReplaceExternalProxies(subID string, fresh []ExternalProxy) error {
	old, err := s.ExternalProxies(subID)
	if err != nil {
		return err
	}
	byName := make(map[string]ExternalProxy, len(old))
	byEndpoint := make(map[string]ExternalProxy, len(old))
	for _, p := range old {
		byName[p.Name] = p
		byEndpoint[endpointKey(p.Server, p.Port, p.Type)] = p
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	kept := make(map[string]struct{}, len(fresh))
	for i, p := range fresh {
		prev, ok := byName[p.Name]
		if !ok {
			prev, ok = byEndpoint[endpointKey(p.Server, p.Port, p.Type)]
		}
		if ok {
			if _, dup := kept[prev.ID]; dup {
				// Two fresh proxies matched the same previous row — possible
				// when a provider duplicates an endpoint under two names. The
				// second one is new rather than a second claim on that row.
				ok = false
			}
		}
		if ok {
			if _, err := tx.Exec(`UPDATE external_proxies
				SET name = ?, type = ?, server = ?, port = ?, config = ?, ord = ?
				WHERE id = ?`, p.Name, p.Type, p.Server, p.Port, p.Config, i, prev.ID); err != nil {
				return err
			}
			kept[prev.ID] = struct{}{}
			continue
		}
		id := NewID()
		if _, err := tx.Exec(`INSERT INTO external_proxies
			(id, sub_id, name, type, server, port, config, chain_node_id, chain_proxy_id, enabled, ord)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULL, NULL, 1, ?)`,
			id, subID, p.Name, p.Type, p.Server, p.Port, p.Config, i); err != nil {
			return err
		}
		// Denied to everyone who already subscribes, for the same reason a new
		// fleet node is: a provider adding a node to their list is not the
		// operator deciding to hand it out. This runs on every refresh, so a
		// node the provider adds next month is inert too — only the rows that
		// are genuinely new get denials, because matched rows take the UPDATE
		// path above and never reach here.
		if _, err := tx.Exec(`INSERT INTO user_external_denies (user_id, proxy_id)
			SELECT id, ? FROM users`, id); err != nil {
			return err
		}
		kept[id] = struct{}{}
	}

	for _, p := range old {
		if _, ok := kept[p.ID]; ok {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM external_proxies WHERE id = ?`, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func endpointKey(server string, port int, typ string) string {
	return fmt.Sprintf("%s|%d|%s", server, port, typ)
}

// ErrChainCycle reports a chain that would come back round to itself.
var ErrChainCycle = errors.New("that would make the chain loop back on itself")

// SetExternalProxy records the operator's settings for one proxy.
//
// chainNodeID and chainProxyID are mutually exclusive: a proxy is dialled
// directly, through a fleet node, or through another external node. The last of
// those can loop — A through B through A — which is rejected here rather than
// left for the renderer, because a setting that silently drops both proxies from
// every subscription is worse than one that refuses to be saved.
func (s *Store) SetExternalProxy(id, chainNodeID, chainProxyID string, enabled bool) error {
	if chainNodeID != "" && chainProxyID != "" {
		return errors.New("a proxy is chained through one thing, not two")
	}
	if chainProxyID != "" {
		if err := s.checkExternalChain(id, chainProxyID); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(`UPDATE external_proxies
		SET chain_node_id = ?, chain_proxy_id = ?, enabled = ? WHERE id = ?`,
		nullIfEmpty(chainNodeID), nullIfEmpty(chainProxyID), enabled, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// checkExternalChain walks from the proposed target and fails if it arrives
// back at id. The walk is bounded by the number of rows, so a cycle that is
// already in the table — put there by a hand-edited database — terminates too.
func (s *Store) checkExternalChain(id, target string) error {
	if id == target {
		return ErrChainCycle
	}
	next := map[string]string{}
	rows, err := s.db.Query(`SELECT id, COALESCE(chain_proxy_id, '') FROM external_proxies`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var from, to string
		if err := rows.Scan(&from, &to); err != nil {
			return err
		}
		if to != "" {
			next[from] = to
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for at, steps := target, 0; at != "" && steps <= len(next); at, steps = next[at], steps+1 {
		if at == id {
			return ErrChainCycle
		}
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UserNodeDenies lists the fleet nodes this subscriber may not use.
//
// Denials rather than grants: a node nobody has been asked about is usable,
// which is what an operator expects when they bind a new one, and it keeps
// these tables empty for a fleet that does not need per-user control.
func (s *Store) UserNodeDenies(userID string) (map[string]struct{}, error) {
	return s.denySet(`SELECT node_id FROM user_node_denies WHERE user_id = ?`, userID)
}

// UserExternalDenies lists the external proxies this subscriber may not use.
func (s *Store) UserExternalDenies(userID string) (map[string]struct{}, error) {
	return s.denySet(`SELECT proxy_id FROM user_external_denies WHERE user_id = ?`, userID)
}

func (s *Store) denySet(query, userID string) (map[string]struct{}, error) {
	rows, err := s.db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = struct{}{}
	}
	return out, rows.Err()
}

// SetUserNodeAccess replaces this subscriber's denials in one go.
//
// Whole-set rather than per-node toggles, because the console shows the whole
// list and an operator ticking boxes is describing an end state. Two calls that
// each toggled one node could interleave into a state neither of them asked
// for.
func (s *Store) SetUserNodeAccess(userID string, deniedNodes, deniedProxies []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM user_node_denies WHERE user_id = ?`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM user_external_denies WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, id := range deniedNodes {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_node_denies (user_id, node_id) VALUES (?, ?)`,
			userID, id); err != nil {
			return err
		}
	}
	for _, id := range deniedProxies {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO user_external_denies (user_id, proxy_id) VALUES (?, ?)`,
			userID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetExternalProxy loads one proxy by id, across sources.
func (s *Store) GetExternalProxy(id string) (ExternalProxy, error) {
	return scanExternalProxy(s.db.QueryRow(
		`SELECT `+externalProxyCols+` FROM external_proxies WHERE id = ?`, id))
}

// ExternalProxyDenies lists the subscribers who may not use one external node.
//
// The mirror of UserExternalDenies. The relation is the same one; which end you
// read it from is a question about what the operator is doing — going through a
// person's entitlements, or deciding who a newly added node is for.
func (s *Store) ExternalProxyDenies(proxyID string) (map[string]struct{}, error) {
	return s.denySet(`SELECT user_id FROM user_external_denies WHERE proxy_id = ?`, proxyID)
}

// SetExternalProxyAccess replaces the set of subscribers denied one external
// node, leaving every other node's denials alone.
//
// Whole-set for this proxy, per the same reasoning as SetUserNodeAccess: the
// console shows the whole list of subscribers and the operator is describing an
// end state, not a sequence of toggles.
func (s *Store) SetExternalProxyAccess(proxyID string, deniedUsers []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM user_external_denies WHERE proxy_id = ?`, proxyID); err != nil {
		return err
	}
	for _, id := range deniedUsers {
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO user_external_denies (user_id, proxy_id) VALUES (?, ?)`,
			id, proxyID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RenameExternalProxy sets or clears the operator's own label for one proxy.
// An empty name restores the provider's.
func (s *Store) RenameExternalProxy(id, displayName string) error {
	res, err := s.db.Exec(`UPDATE external_proxies SET display_name = ? WHERE id = ?`, displayName, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

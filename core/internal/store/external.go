package store

import (
	"database/sql"
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
	// ChainNodeID is the fleet node this one is reached through, empty for a
	// direct dial.
	ChainNodeID string
	Enabled     bool
	Ord         int
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

const externalProxyCols = `id, sub_id, name, type, server, port, config, COALESCE(chain_node_id, ''), enabled, ord`

func scanExternalProxy(row interface{ Scan(...any) error }) (ExternalProxy, error) {
	var p ExternalProxy
	err := row.Scan(&p.ID, &p.SubID, &p.Name, &p.Type, &p.Server, &p.Port, &p.Config,
		&p.ChainNodeID, &p.Enabled, &p.Ord)
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
		ORDER BY p.sub_id, p.ord`)
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
			(id, sub_id, name, type, server, port, config, chain_node_id, enabled, ord)
			VALUES (?, ?, ?, ?, ?, ?, ?, NULL, 1, ?)`,
			id, subID, p.Name, p.Type, p.Server, p.Port, p.Config, i); err != nil {
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

// SetExternalProxy records the operator's settings for one proxy.
func (s *Store) SetExternalProxy(id, chainNodeID string, enabled bool) error {
	var chain any
	if chainNodeID != "" {
		chain = chainNodeID
	}
	res, err := s.db.Exec(`UPDATE external_proxies SET chain_node_id = ?, enabled = ? WHERE id = ?`,
		chain, enabled, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
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

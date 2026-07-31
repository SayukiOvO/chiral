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
// operator's per-proxy settings across.
//
// A refreshed proxy is matched to its previous row by name, and failing that
// by endpoint. Providers rename constantly — remaining traffic and expiry
// dates go in the label — so matching on name alone would detach the chain
// setting every time the provider edited a string, which is the one thing an
// operator would never think to check.
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
	if _, err := tx.Exec(`DELETE FROM external_proxies WHERE sub_id = ?`, subID); err != nil {
		return err
	}
	for i, p := range fresh {
		prev, ok := byName[p.Name]
		if !ok {
			prev, ok = byEndpoint[endpointKey(p.Server, p.Port, p.Type)]
		}
		chain := ""
		enabled := true
		if ok {
			chain, enabled = prev.ChainNodeID, prev.Enabled
		}
		var chainVal any
		if chain != "" {
			chainVal = chain
		}
		if _, err := tx.Exec(`INSERT INTO external_proxies
			(id, sub_id, name, type, server, port, config, chain_node_id, enabled, ord)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			NewID(), subID, p.Name, p.Type, p.Server, p.Port, p.Config, chainVal, enabled, i); err != nil {
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

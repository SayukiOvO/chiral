package store

import (
	"database/sql"
	"time"
)

// Ruleset is a routing configuration a subscription can be rendered against:
// a built-in ACL4SSR preset, or an .ini the operator pointed at.
type Ruleset struct {
	ID   string
	Name string
	// Preset is the built-in key, empty for a custom source. Kept so the
	// console can show which preset a ruleset came from and refuse to rename
	// it into something it is not.
	Preset string
	URL    string
	// INI is the last successfully fetched source. Rendering uses this, never
	// the network: a subscription must not fail because GitHub is unreachable,
	// which for this software's users is the ordinary condition.
	INI       string
	FetchedAt sql.NullInt64
	LastError string
	CreatedAt int64
	UpdatedAt int64
}

const rulesetCols = `id, name, preset, url, ini, fetched_at, last_error, created_at, updated_at`

func scanRuleset(row interface{ Scan(...any) error }) (Ruleset, error) {
	var r Ruleset
	err := row.Scan(&r.ID, &r.Name, &r.Preset, &r.URL, &r.INI, &r.FetchedAt,
		&r.LastError, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) CreateRuleset(name, preset, url string) (Ruleset, error) {
	now := time.Now().Unix()
	r := Ruleset{ID: NewID(), Name: name, Preset: preset, URL: url, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.Exec(`INSERT INTO rulesets (id, name, preset, url, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, r.ID, r.Name, r.Preset, r.URL, r.CreatedAt, r.UpdatedAt)
	return r, err
}

func (s *Store) GetRuleset(id string) (Ruleset, error) {
	return scanRuleset(s.db.QueryRow(`SELECT `+rulesetCols+` FROM rulesets WHERE id = ?`, id))
}

func (s *Store) ListRulesets() ([]Ruleset, error) {
	rows, err := s.db.Query(`SELECT ` + rulesetCols + ` FROM rulesets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ruleset
	for rows.Next() {
		r, err := scanRuleset(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRuleset changes the name and, for a custom ruleset, its source.
func (s *Store) UpdateRuleset(id, name, url string) error {
	res, err := s.db.Exec(`UPDATE rulesets SET name = ?, url = ?, updated_at = ? WHERE id = ?`,
		name, url, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteRuleset(id string) error {
	res, err := s.db.Exec(`DELETE FROM rulesets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SaveRulesetFetch records the outcome of a refresh.
//
// A failure keeps the previous .ini rather than clearing it: an unreachable
// upstream must degrade to yesterday's rules, not to none. The error is
// stored alongside so the console can say the rules are stale and why, which
// is the difference between a ruleset that quietly stopped updating and one
// that visibly did.
func (s *Store) SaveRulesetFetch(id, ini, failure string) error {
	now := time.Now().Unix()
	if failure != "" {
		_, err := s.db.Exec(`UPDATE rulesets SET last_error = ?, updated_at = ? WHERE id = ?`,
			failure, now, id)
		return err
	}
	_, err := s.db.Exec(`UPDATE rulesets SET ini = ?, fetched_at = ?, last_error = '', updated_at = ?
		WHERE id = ?`, ini, now, now, id)
	return err
}

// RuleList is one cached rule list, shared by every ruleset referencing it.
type RuleList struct {
	URL       string
	Body      string
	ETag      string
	FetchedAt int64
}

func (s *Store) GetRuleList(url string) (RuleList, error) {
	var l RuleList
	err := s.db.QueryRow(`SELECT url, body, etag, fetched_at FROM rule_lists WHERE url = ?`, url).
		Scan(&l.URL, &l.Body, &l.ETag, &l.FetchedAt)
	return l, err
}

func (s *Store) PutRuleList(l RuleList) error {
	_, err := s.db.Exec(`INSERT INTO rule_lists (url, body, etag, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (url) DO UPDATE SET body = excluded.body, etag = excluded.etag,
		fetched_at = excluded.fetched_at`, l.URL, l.Body, l.ETag, time.Now().Unix())
	return err
}

// PruneRuleLists drops cached lists no live ruleset references any more.
//
// Called after a refresh rather than on a timer: the set of referenced URLs
// only changes when a ruleset's .ini does, and a list nobody references is
// dead weight that would otherwise be refreshed forever.
func (s *Store) PruneRuleLists(keep map[string]struct{}) (int, error) {
	rows, err := s.db.Query(`SELECT url FROM rule_lists`)
	if err != nil {
		return 0, err
	}
	var stale []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			rows.Close()
			return 0, err
		}
		if _, ok := keep[u]; !ok {
			stale = append(stale, u)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, u := range stale {
		if _, err := s.db.Exec(`DELETE FROM rule_lists WHERE url = ?`, u); err != nil {
			return 0, err
		}
	}
	return len(stale), nil
}

// SetUserRuleset points a subscriber at a ruleset, or at none when id is empty.
func (s *Store) SetUserRuleset(userID, rulesetID string) error {
	var v any
	if rulesetID != "" {
		v = rulesetID
	}
	res, err := s.db.Exec(`UPDATE users SET ruleset_id = ? WHERE id = ?`, v, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

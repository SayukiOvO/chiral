package store

import "database/sql"

// Setting keys. Constants rather than string literals at the call sites, so a
// typo is a compile error instead of a setting that silently stays default.
const (
	// SettingSubscriptionName is what a subscription is called when it lands
	// in a client. Clash-family clients take it from the download filename and
	// show it as the profile's name, so leaving it as the software's own name
	// puts "chiral" on every subscriber's screen.
	SettingSubscriptionName = "subscription_name"
)

// Setting reads one value, returning the fallback when it has never been set.
func (s *Store) Setting(key, fallback string) string {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil || v == "" {
		return fallback
	}
	return v
}

// SetSetting writes one value. An empty value removes the row, so "unset"
// and "set to empty" cannot drift apart — the fallback applies in both cases
// and there is only one way to express it.
func (s *Store) SetSetting(key, value string) error {
	if value == "" {
		_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Settings returns every stored value, for the console's settings form.
func (s *Store) Settings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

var _ = sql.ErrNoRows

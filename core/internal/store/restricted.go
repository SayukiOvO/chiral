package store

import (
	"database/sql"
	"fmt"
	"net"
	"strings"
	"time"
)

// RestrictedDestination is a network some nodes can reach that most
// subscribers must not — DN42 being the motivating case. See migration 0027
// for why its permission table stores allows where everything else stores
// denies.
type RestrictedDestination struct {
	ID   string
	Name string
	// CIDRs and Domains are what the routing rule matches: destination IP
	// ranges and domain suffixes, one per line.
	CIDRs   []string
	Domains []string
	// NodeIDs are where the network exists and the rules are enforced.
	NodeIDs []string
	// AllowedUserIDs may go there. Everyone else is blackholed.
	AllowedUserIDs []string
	CreatedAt      int64
}

// ParseCIDRLines validates operator-entered CIDRs, one per line. A bare IP is
// accepted and returned as its /32 (or /128): "the one machine" is a natural
// thing to type, and rejecting it would only teach the operator to append the
// suffix by hand.
func ParseCIDRLines(text string) ([]string, error) {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.Contains(line, "/") {
			ip := net.ParseIP(line)
			if ip == nil {
				return nil, fmt.Errorf("%q 不是 IP 也不是 CIDR", line)
			}
			// Canonicalised, so an IPv4-mapped form ("::ffff:1.2.3.4") becomes
			// the plain v4 it means rather than a /32 on the wrong family.
			if v4 := ip.To4(); v4 != nil {
				line = v4.String() + "/32"
			} else {
				line += "/128"
			}
		}
		if _, _, err := net.ParseCIDR(line); err != nil {
			return nil, fmt.Errorf("%q 不是合法的 CIDR", line)
		}
		out = append(out, line)
	}
	return out, nil
}

// ParseDomainLines validates domain suffixes, one per line. Leading "*." and
// "." are stripped — "*.dn42", ".dn42" and "dn42" all mean the same suffix to
// the routing rule.
//
// Everything else non-domain-shaped is REFUSED, not passed through. This is
// an allowlist security feature: a pattern Xray accepts and never matches —
// a literal "*", a colon, a trailing dot, an IP typed into the wrong box —
// is a restriction that silently is not one, which is the exact failure the
// whole table exists to prevent.
func ParseDomainLines(text string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.TrimPrefix(line, "*.")
		line = strings.TrimPrefix(line, ".")
		if line == "" {
			continue
		}
		line = strings.ToLower(line)
		if net.ParseIP(line) != nil {
			return nil, fmt.Errorf("%q 是 IP——请写进上面的 IP 段一栏", line)
		}
		if !domainSuffixShaped(line) {
			return nil, fmt.Errorf("%q 不是合法的域名后缀", line)
		}
		out = append(out, line)
	}
	return out, nil
}

// domainSuffixShaped reports whether every label is hostname-shaped: letters,
// digits and hyphens, dot-separated, no empty labels.
func domainSuffixShaped(s string) bool {
	for _, label := range strings.Split(s, ".") {
		if label == "" {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return false
			}
		}
	}
	return true
}

func joinLines(v []string) string { return strings.Join(v, "\n") }

func splitLines(v string) []string {
	var out []string
	for _, l := range strings.Split(v, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// CreateRestrictedDestination stores a destination with nobody allowed and no
// node enforcing it: naming the network, deciding where it exists, and
// deciding who may enter are three separate acts.
func (s *Store) CreateRestrictedDestination(name string, cidrs, domains []string) (RestrictedDestination, error) {
	d := RestrictedDestination{
		ID: NewID(), Name: name, CIDRs: cidrs, Domains: domains,
		CreatedAt: time.Now().Unix(),
	}
	_, err := s.db.Exec(`INSERT INTO restricted_destinations (id, name, cidrs, domains, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		d.ID, d.Name, joinLines(d.CIDRs), joinLines(d.Domains), d.CreatedAt)
	return d, err
}

func (s *Store) UpdateRestrictedDestination(id, name string, cidrs, domains []string) error {
	res, err := s.db.Exec(`UPDATE restricted_destinations SET name = ?, cidrs = ?, domains = ? WHERE id = ?`,
		name, joinLines(cidrs), joinLines(domains), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteRestrictedDestination(id string) error {
	res, err := s.db.Exec(`DELETE FROM restricted_destinations WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetRestrictedDestinationNodes replaces where one destination is enforced.
func (s *Store) SetRestrictedDestinationNodes(destID string, nodeIDs []string) error {
	return s.replaceSet(`DELETE FROM restricted_destination_nodes WHERE dest_id = ?`,
		`INSERT OR IGNORE INTO restricted_destination_nodes (dest_id, node_id) VALUES (?, ?)`,
		destID, nodeIDs)
}

// SetRestrictedDestinationAllows replaces who may enter one destination.
func (s *Store) SetRestrictedDestinationAllows(destID string, userIDs []string) error {
	return s.replaceSet(`DELETE FROM restricted_destination_allows WHERE dest_id = ?`,
		`INSERT OR IGNORE INTO restricted_destination_allows (dest_id, user_id) VALUES (?, ?)`,
		destID, userIDs)
}

func (s *Store) replaceSet(deleteQ, insertQ, key string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(deleteQ, key); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(insertQ, key, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListRestrictedDestinations returns every destination with its scope and
// allow set attached — the console shows all three together, and they are
// small.
func (s *Store) ListRestrictedDestinations() ([]RestrictedDestination, error) {
	rows, err := s.db.Query(`SELECT id, name, cidrs, domains, created_at
		FROM restricted_destinations ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RestrictedDestination
	for rows.Next() {
		var d RestrictedDestination
		var cidrs, domains string
		if err := rows.Scan(&d.ID, &d.Name, &cidrs, &domains, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.CIDRs, d.Domains = splitLines(cidrs), splitLines(domains)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].NodeIDs, err = s.stringColumn(
			`SELECT node_id FROM restricted_destination_nodes WHERE dest_id = ?`, out[i].ID); err != nil {
			return nil, err
		}
		if out[i].AllowedUserIDs, err = s.stringColumn(
			`SELECT user_id FROM restricted_destination_allows WHERE dest_id = ?`, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetRestrictedDestination loads one, with scope and allows.
func (s *Store) GetRestrictedDestination(id string) (RestrictedDestination, error) {
	all, err := s.ListRestrictedDestinations()
	if err != nil {
		return RestrictedDestination{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return RestrictedDestination{}, sql.ErrNoRows
}

func (s *Store) stringColumn(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

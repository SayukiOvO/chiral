package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Target kinds for an egress rule.
const (
	EgressDirect   = "direct"
	EgressExternal = "external"
	EgressNode     = "node"
)

// EgressRule sends particular traffic off a node somewhere other than the
// node's own egress. See migration 0028 for why it is its own table rather
// than an extension of restricted destinations.
type EgressRule struct {
	ID     string
	NodeID string
	Label  string
	// Domains and IPs are the match. Both may be set and they render as two
	// Xray rules sharing one outbound, because conditions within one rule are
	// AND-ed. Entries are geosite:/geoip: categories, bare domain suffixes,
	// or CIDRs.
	Domains []string
	IPs     []string

	TargetKind string
	// TargetProxyID for an external landing; TargetNodeID plus
	// TargetProfileID and Secret for a fleet one.
	TargetProxyID   string
	TargetNodeID    string
	TargetProfileID string
	Secret          string

	Enabled   bool
	SortOrder int
	CreatedAt int64
}

// Matcher reports whether a value is a geo category rather than a literal.
func isGeoCategory(v string) (kind string, ok bool) {
	switch {
	case strings.HasPrefix(v, "geosite:"):
		return "geosite", true
	case strings.HasPrefix(v, "geoip:"):
		return "geoip", true
	case strings.HasPrefix(v, "ext:"):
		// ext:file.dat:tag — an external geodata file, same shape either side.
		return "ext", true
	}
	return "", false
}

// validGeoTag accepts what a geodata category name may contain. Xray takes
// the text after the colon verbatim, so a category with a stray space or
// quote loads as a category that does not exist — accepted by `xray -test`,
// matching nothing, forever.
func validGeoTag(v string) bool {
	tag := v[strings.Index(v, ":")+1:]
	if tag == "" {
		return false
	}
	for _, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.', r == ':', r == '@':
		default:
			return false
		}
	}
	return true
}

// ParseEgressDomains validates the domain side of a match: geosite categories
// and bare suffixes, one per line.
func ParseEgressDomains(text string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if kind, ok := isGeoCategory(line); ok {
			if kind == "geoip" {
				return nil, fmt.Errorf("%q 属于 IP 类别，请填入下方的「IP 段 / geoip 类别」一栏", line)
			}
			if !validGeoTag(line) {
				return nil, fmt.Errorf("%q 不是合法的 geosite 类别", line)
			}
			out = append(out, line)
			continue
		}
		suffix, err := ParseDomainLines(line)
		if err != nil {
			return nil, err
		}
		// Rendered with the domain: prefix so it is a suffix match, matching
		// how restricted destinations spell the same thing.
		for _, s := range suffix {
			out = append(out, "domain:"+s)
		}
	}
	return out, nil
}

// ParseEgressIPs validates the IP side: geoip categories and CIDRs.
func ParseEgressIPs(text string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if kind, ok := isGeoCategory(line); ok {
			if kind == "geosite" {
				return nil, fmt.Errorf("%q 属于域名类别，请填入上方的「域名 / geosite 类别」一栏", line)
			}
			// A leading "!" negates: geoip:!cn is "everything except China",
			// which is the most common rule anyone writes. Xray takes it on
			// ip conditions; geosite has no such form.
			if !validGeoTag(strings.Replace(line, ":!", ":", 1)) {
				return nil, fmt.Errorf("%q 不是合法的 geoip 类别", line)
			}
			out = append(out, line)
			continue
		}
		cidrs, err := ParseCIDRLines(line)
		if err != nil {
			return nil, err
		}
		out = append(out, cidrs...)
	}
	return out, nil
}

func egressAAD(id string) string { return "egress-rule:" + id }

const egressCols = `id, node_id, label, domains, ips, target_kind,
	COALESCE(target_proxy_id, ''), COALESCE(target_node_id, ''),
	COALESCE(target_profile_id, ''), secret, enabled, sort_order, created_at`

func (s *Store) scanEgress(row interface{ Scan(...any) error }) (EgressRule, error) {
	var r EgressRule
	var domains, ips string
	if err := row.Scan(&r.ID, &r.NodeID, &r.Label, &domains, &ips, &r.TargetKind,
		&r.TargetProxyID, &r.TargetNodeID, &r.TargetProfileID, &r.Secret,
		&r.Enabled, &r.SortOrder, &r.CreatedAt); err != nil {
		return r, err
	}
	r.Domains, r.IPs = splitLines(domains), splitLines(ips)
	if r.Secret != "" {
		plain, err := s.box.Open(egressAAD(r.ID), r.Secret)
		if err != nil {
			return r, err
		}
		r.Secret = plain
	}
	return r, nil
}

// CreateEgressRule stores one rule, sealing the dial credential when the
// landing is another of our nodes.
func (s *Store) CreateEgressRule(r EgressRule) (EgressRule, error) {
	r.ID = NewID()
	r.CreatedAt = time.Now().Unix()
	sealed := ""
	if r.Secret != "" {
		var err error
		if sealed, err = s.box.Seal(egressAAD(r.ID), r.Secret); err != nil {
			return EgressRule{}, err
		}
	}
	// Placed last, not first. Order is priority, and a rule that silently
	// outranked everything the operator had already arranged would change
	// what the node does the moment it was created.
	_, err := s.db.Exec(`INSERT INTO node_egress_rules
		(id, node_id, label, domains, ips, target_kind, target_proxy_id, target_node_id,
		 target_profile_id, secret, enabled, sort_order, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM node_egress_rules WHERE node_id = ?), ?)`,
		r.ID, r.NodeID, r.Label, joinLines(r.Domains), joinLines(r.IPs), r.TargetKind,
		nullIfEmpty(r.TargetProxyID), nullIfEmpty(r.TargetNodeID),
		nullIfEmpty(r.TargetProfileID), sealed, r.Enabled, r.NodeID, r.CreatedAt)
	return r, err
}

// UpdateEgressRule changes what an operator may change. The dial credential
// is not among them: rotating it is a separate act with its own consequences.
func (s *Store) UpdateEgressRule(id, label string, domains, ips []string, enabled bool) error {
	res, err := s.db.Exec(`UPDATE node_egress_rules
		SET label = ?, domains = ?, ips = ?, enabled = ? WHERE id = ?`,
		label, joinLines(domains), joinLines(ips), enabled, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteEgressRule(id string) error {
	res, err := s.db.Exec(`DELETE FROM node_egress_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetEgressRule(id string) (EgressRule, error) {
	return s.scanEgress(s.db.QueryRow(`SELECT `+egressCols+` FROM node_egress_rules WHERE id = ?`, id))
}

// EgressRulesOn lists a node's rules in the operator's order — which is the
// priority order, since routing is first-match.
func (s *Store) EgressRulesOn(nodeID string) ([]EgressRule, error) {
	return s.queryEgress(`SELECT `+egressCols+` FROM node_egress_rules
		WHERE node_id = ? ORDER BY sort_order, created_at`, nodeID)
}

// EgressRulesLandingOn lists the enabled rules that dial a given node, which
// is what tells that node's config which machine credentials to accept.
func (s *Store) EgressRulesLandingOn(nodeID string) ([]EgressRule, error) {
	return s.queryEgress(`SELECT `+egressCols+` FROM node_egress_rules
		WHERE target_node_id = ? AND target_kind = 'node' AND enabled = 1
		ORDER BY sort_order, created_at`, nodeID)
}

func (s *Store) queryEgress(query string, args ...any) ([]EgressRule, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EgressRule{}
	for rows.Next() {
		r, err := s.scanEgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReorderEgressRules writes the whole order for one node at once: the console
// shows the list and the operator is describing an end state.
func (s *Store) ReorderEgressRules(nodeID string, ids []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE node_egress_rules SET sort_order = ? WHERE id = ? AND node_id = ?`,
			i+1, id, nodeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EgressSourceNodesFor lists the nodes that send traffic to any of the given
// nodes through an enabled fleet landing.
//
// The mirror of EgressRulesLandingOn, needed because a restricted destination
// scoped to a landing node has to be enforced on the SOURCE node too — that
// is the last place the user is still identifiable, exactly as with a relay
// line's entry.
func (s *Store) EgressSourceNodesFor(nodeIDs []string) ([]string, error) {
	if len(nodeIDs) == 0 {
		return nil, nil
	}
	q := `SELECT DISTINCT node_id FROM node_egress_rules
		WHERE target_kind = 'node' AND enabled = 1 AND target_node_id IN (?` +
		strings.Repeat(",?", len(nodeIDs)-1) + `)`
	args := make([]any, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		args = append(args, id)
	}
	return s.stringColumn(q, args...)
}

// SetEgressTarget moves a rule's landing, sealing the dial credential when
// the new landing is another node of this fleet.
//
// Separate from UpdateEgressRule because a landing change has consequences a
// label change does not: a new credential, and a node elsewhere that must be
// re-assembled to stop accepting the old one.
func (s *Store) SetEgressTarget(id, kind, proxyID, nodeID, profileID, secret string) error {
	sealed := ""
	if secret != "" {
		var err error
		if sealed, err = s.box.Seal(egressAAD(id), secret); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(`UPDATE node_egress_rules
		SET target_kind = ?, target_proxy_id = ?, target_node_id = ?,
		    target_profile_id = ?, secret = ?
		WHERE id = ?`,
		kind, nullIfEmpty(proxyID), nullIfEmpty(nodeID), nullIfEmpty(profileID), sealed, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

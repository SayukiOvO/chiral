package store

import (
	"database/sql"
	"time"
)

// NodeRelay is one node of this fleet leaving through another: subscribers
// connect to the entry, their traffic leaves at the exit.
//
// Unlike an external relay, both ends are ours, so this is not about hiding a
// landing — it is about reaching one. A subscriber who cannot get a usable
// connection straight to a distant exit can often reach a nearby entry.
type NodeRelay struct {
	ID          string
	EntryNodeID string
	ExitNodeID  string
	// ProfileID is which of the exit's access points the entry dials. The
	// entry needs a client config for the exit, and a profile is that.
	ProfileID string
	// Label is what subscribers see this line called. It needs its own,
	// because the entry's name belongs to its direct line and the exit's to
	// its own — two proxies alike would make the document ambiguous.
	Label string
	// Secret is the link's own credential. One per link, not per subscriber:
	// an Xray outbound is static, so what it presents cannot depend on whose
	// connection it is carrying.
	Secret      string
	Enabled     bool
	SortOrder   int
	TrafficRate float64
	CreatedAt   int64
}

func relayAAD(id string) string { return "node-relay:" + id }

const relayCols = `id, entry_node_id, exit_node_id, profile_id, label, secret, enabled, sort_order, traffic_rate, created_at`

func (s *Store) scanRelay(row interface{ Scan(...any) error }) (NodeRelay, error) {
	var r NodeRelay
	if err := row.Scan(&r.ID, &r.EntryNodeID, &r.ExitNodeID, &r.ProfileID, &r.Label,
		&r.Secret, &r.Enabled, &r.SortOrder, &r.TrafficRate, &r.CreatedAt); err != nil {
		return r, err
	}
	plain, err := s.box.Open(relayAAD(r.ID), r.Secret)
	if err != nil {
		return r, err
	}
	r.Secret = plain
	return r, nil
}

// CreateNodeRelay records a link and the credential it will present.
//
// A new line starts denied to everyone, like a new node and a new external
// source: an operator adding one is describing a route, not yet deciding who
// gets it, and those two are worth keeping apart.
func (s *Store) CreateNodeRelay(r NodeRelay) (NodeRelay, error) {
	r.ID = NewID()
	r.CreatedAt = time.Now().Unix()
	if r.TrafficRate == 0 {
		r.TrafficRate = 1
	}
	sealed, err := s.box.Seal(relayAAD(r.ID), r.Secret)
	if err != nil {
		return NodeRelay{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return NodeRelay{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO node_relays
		(id, entry_node_id, exit_node_id, profile_id, label, secret, enabled, sort_order, traffic_rate, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		r.ID, r.EntryNodeID, r.ExitNodeID, r.ProfileID, r.Label, sealed, r.Enabled,
		r.TrafficRate, r.CreatedAt); err != nil {
		return NodeRelay{}, err
	}
	if _, err := tx.Exec(`INSERT INTO user_relay_denies (user_id, relay_id)
		SELECT id, ? FROM users`, r.ID); err != nil {
		return NodeRelay{}, err
	}
	if _, err := tx.Exec(`INSERT INTO group_relay_denies (group_id, relay_id)
		SELECT id, ? FROM subscriber_groups`, r.ID); err != nil {
		return NodeRelay{}, err
	}
	return r, tx.Commit()
}

// UserRelayDenies lists the relay lines this subscriber may not take.
func (s *Store) UserRelayDenies(userID string) (map[string]struct{}, error) {
	return s.effectiveDenies(userID, "relay_id", "user_relay_denies", "user_relay_allows", "group_relay_denies")
}

// NodeRelayDenies lists the subscribers who may not take one line — the same
// relation read from the other end.
func (s *Store) NodeRelayDenies(relayID string) (map[string]struct{}, error) {
	return s.objectDenies("relay", relayID)
}

func (s *Store) SetNodeRelayAccess(relayID string, deniedUsers []string) error {
	return s.setObjectAccess("relay", relayID, deniedUsers)
}

func (s *Store) GetNodeRelay(id string) (NodeRelay, error) {
	return s.scanRelay(s.db.QueryRow(`SELECT `+relayCols+` FROM node_relays WHERE id = ?`, id))
}

// ListNodeRelays returns every link, ordered like everything else the operator
// arranges.
func (s *Store) ListNodeRelays() ([]NodeRelay, error) {
	return s.queryRelays(`SELECT ` + relayCols + ` FROM node_relays
		ORDER BY sort_order = 0, sort_order, created_at`)
}

// RelaysFromEntry lists the links a given node is the entry for: the exits it
// must be able to dial.
func (s *Store) RelaysFromEntry(nodeID string) ([]NodeRelay, error) {
	return s.queryRelays(`SELECT `+relayCols+` FROM node_relays
		WHERE entry_node_id = ? AND enabled = 1
		ORDER BY sort_order = 0, sort_order, created_at`, nodeID)
}

// RelaysToExit lists the links a given node is the exit for: the credentials
// it must accept.
func (s *Store) RelaysToExit(nodeID string) ([]NodeRelay, error) {
	return s.queryRelays(`SELECT `+relayCols+` FROM node_relays
		WHERE exit_node_id = ? AND enabled = 1
		ORDER BY sort_order = 0, sort_order, created_at`, nodeID)
}

func (s *Store) queryRelays(query string, args ...any) ([]NodeRelay, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeRelay{}
	for rows.Next() {
		r, err := s.scanRelay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateNodeRelay changes what the operator can change. The secret is not
// among them: rotating it is a separate act with its own consequences.
func (s *Store) UpdateNodeRelay(id, label string, enabled bool, rate float64) error {
	res, err := s.db.Exec(`UPDATE node_relays SET label = ?, enabled = ?, traffic_rate = ? WHERE id = ?`,
		label, enabled, rate, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteNodeRelay(id string) error {
	res, err := s.db.Exec(`DELETE FROM node_relays WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

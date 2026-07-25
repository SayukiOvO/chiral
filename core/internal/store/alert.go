package store

import (
	"database/sql"
	"fmt"
	"time"
)

// AlertTarget is somewhere notifications are delivered.
type AlertTarget struct {
	ID   string
	Kind string
	Name string
	// Config is a bot token + chat id, or a webhook URL. Encrypted at rest:
	// a bot token is a credential like any other.
	Config     string
	Enabled    bool
	CreatedAt  int64
	LastError  string
	LastSentAt int64
}

const (
	AlertTelegram = "telegram"
	AlertWebhook  = "webhook"
)

func alertAAD(id string) string { return "alert-target:" + id }

const alertCols = `id, kind, name, config, enabled, created_at, last_error, last_sent_at`

func (s *Store) scanAlertTarget(row interface{ Scan(...any) error }) (AlertTarget, error) {
	var t AlertTarget
	if err := row.Scan(&t.ID, &t.Kind, &t.Name, &t.Config, &t.Enabled,
		&t.CreatedAt, &t.LastError, &t.LastSentAt); err != nil {
		return t, err
	}
	plain, err := s.box.Open(alertAAD(t.ID), t.Config)
	if err != nil {
		return t, fmt.Errorf("opening alert target %s: %w", t.Name, err)
	}
	t.Config = plain
	return t, nil
}

func (s *Store) CreateAlertTarget(kind, name, config string) (AlertTarget, error) {
	t := AlertTarget{
		ID: NewID(), Kind: kind, Name: name, Config: config,
		Enabled: true, CreatedAt: time.Now().Unix(),
	}
	sealed, err := s.box.Seal(alertAAD(t.ID), config)
	if err != nil {
		return AlertTarget{}, err
	}
	_, err = s.db.Exec(`
		INSERT INTO alert_targets (id, kind, name, config, enabled, created_at)
		VALUES (?, ?, ?, ?, 1, ?)`,
		t.ID, t.Kind, t.Name, sealed, t.CreatedAt)
	return t, err
}

func (s *Store) ListAlertTargets() ([]AlertTarget, error) {
	rows, err := s.db.Query(`SELECT ` + alertCols + ` FROM alert_targets ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertTarget{}
	for rows.Next() {
		t, err := s.scanAlertTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetAlertTarget(id string) (AlertTarget, error) {
	return s.scanAlertTarget(s.db.QueryRow(`SELECT `+alertCols+` FROM alert_targets WHERE id = ?`, id))
}

func (s *Store) SetAlertTargetEnabled(id string, enabled bool) error {
	res, err := s.db.Exec(`UPDATE alert_targets SET enabled = ? WHERE id = ?`, enabled, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteAlertTarget(id string) error {
	res, err := s.db.Exec(`DELETE FROM alert_targets WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordAlertResult stores the outcome of a delivery so a misconfigured target
// is visible in the UI instead of only in the logs.
func (s *Store) RecordAlertResult(id string, errMsg string) error {
	_, err := s.db.Exec(`UPDATE alert_targets SET last_error = ?, last_sent_at = ? WHERE id = ?`,
		errMsg, time.Now().Unix(), id)
	return err
}

// --- per-node alerting state ---

// NodeAlertState is what has been announced about a node, and what is
// currently observed. The two differ while a change is being ridden out.
type NodeAlertState struct {
	NodeID          string
	AnnouncedOnline bool
	ObservedOnline  bool
	ChangedAt       int64
	AnnouncedAt     int64
}

func (s *Store) NodeAlertStates() (map[string]NodeAlertState, error) {
	rows, err := s.db.Query(
		`SELECT node_id, announced_online, observed_online, changed_at, announced_at FROM node_alert_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]NodeAlertState{}
	for rows.Next() {
		var st NodeAlertState
		if err := rows.Scan(&st.NodeID, &st.AnnouncedOnline, &st.ObservedOnline,
			&st.ChangedAt, &st.AnnouncedAt); err != nil {
			return nil, err
		}
		out[st.NodeID] = st
	}
	return out, rows.Err()
}

// ObserveNode records what a node currently looks like, moving changed_at only
// when the observation actually differs. A first sighting is recorded as
// already-announced, so adopting an existing fleet does not fire an alert per
// node the moment alerting is switched on.
func (s *Store) ObserveNode(nodeID string, online bool, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO node_alert_state (node_id, announced_online, observed_online, changed_at, announced_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (node_id) DO UPDATE SET
			changed_at = CASE WHEN node_alert_state.observed_online = excluded.observed_online
			                  THEN node_alert_state.changed_at ELSE excluded.changed_at END,
			observed_online = excluded.observed_online`,
		nodeID, online, online, now.Unix(), now.Unix())
	return err
}

// MarkAnnounced records that the observed state has now been told to someone.
func (s *Store) MarkAnnounced(nodeID string, online bool, now time.Time) error {
	_, err := s.db.Exec(
		`UPDATE node_alert_state SET announced_online = ?, announced_at = ? WHERE node_id = ?`,
		online, now.Unix(), nodeID)
	return err
}

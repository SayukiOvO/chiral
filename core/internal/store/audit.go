package store

import (
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
)

// AuditRetention bounds the trail. Long enough to investigate something that
// happened last quarter, short enough that the table does not grow forever.
const AuditRetention = 180 * 24 * time.Hour

// AuditEntry is one recorded action.
type AuditEntry struct {
	ID         int64  `json:"id"`
	At         int64  `json:"at"`
	ActorID    string `json:"actor_id"`
	ActorName  string `json:"actor_name"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	Detail     string `json:"detail"`
}

// Audit records an action. The actor's name is copied in rather than
// referenced: an audit trail that stops naming someone once their account is
// deleted is not much of an audit trail.
func (s *Store) Audit(actor auth.Identity, action, targetType, targetID, targetName, detail string) error {
	_, err := s.db.Exec(`
		INSERT INTO audit_log (at, actor_id, actor_name, action, target_type, target_id, target_name, detail)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor.ID, actor.Name, action, targetType, targetID, targetName, detail)
	return err
}

// AuditPage returns entries newest first. `before` pages backwards through
// ids; 0 starts at the newest.
func (s *Store) AuditPage(before int64, limit int, actorID, action string) ([]AuditEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id, at, actor_id, actor_name, action, target_type, target_id, target_name, detail
	          FROM audit_log WHERE 1 = 1`
	args := []any{}
	if before > 0 {
		query += ` AND id < ?`
		args = append(args, before)
	}
	if actorID != "" {
		query += ` AND actor_id = ?`
		args = append(args, actorID)
	}
	if action != "" {
		query += ` AND action = ?`
		args = append(args, action)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorName, &e.Action,
			&e.TargetType, &e.TargetID, &e.TargetName, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneAudit drops entries past the retention window.
func (s *Store) PruneAudit(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM audit_log WHERE at < ?`, now.Add(-AuditRetention).Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

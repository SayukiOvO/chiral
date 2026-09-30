package store

import (
	"database/sql"
	"fmt"
)

// recordRetiredCredentialDelta is reachable only from the transactional,
// acknowledged stats path. These rows cannot be used to assemble config,
// render subscriptions, restore access, or attribute online observations.
// A missing identity remains unknown; it must never be inferred from an email.
func recordRetiredCredentialDelta(tx *sql.Tx, nodeID string, entry TrafficDelta) (string, error) {
	var userID sql.NullString
	var owner string
	var oldUp, oldDown, oldUsage int64
	var retiredAt sql.NullInt64
	var rate float64
	err := tx.QueryRow(`SELECT u.id, a.node_id, a.up_bytes, a.down_bytes,
		COALESCE(u.used_bytes, 0), a.traffic_rate, a.retired_at
		FROM credential_traffic_owners a LEFT JOIN users u ON u.id = a.user_id
		WHERE a.email = ?`, entry.Name).Scan(&userID, &owner, &oldUp, &oldDown, &oldUsage, &rate, &retiredAt)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if owner != nodeID {
		return "", fmt.Errorf("traffic identity does not belong to the reporting node")
	}
	if !retiredAt.Valid {
		return "", fmt.Errorf("traffic identity is missing its serving credential")
	}
	up, err := checkedTrafficSum(oldUp, entry.Up)
	if err != nil {
		return "", err
	}
	down, err := checkedTrafficSum(oldDown, entry.Down)
	if err != nil {
		return "", err
	}
	if userID.Valid {
		billed, err := billedTrafficBytes(entry.Up, entry.Down, rate)
		if err != nil {
			return "", err
		}
		usage, err := checkedTrafficSum(oldUsage, billed)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec(`UPDATE users SET used_bytes = ? WHERE id = ?`, usage, userID.String); err != nil {
			return "", err
		}
	}
	if _, err := tx.Exec(`UPDATE credential_traffic_owners SET up_bytes = ?, down_bytes = ?
		WHERE email = ? AND retired_at IS NOT NULL`, up, down, entry.Name); err != nil {
		return "", err
	}
	// Deleted users are not recreated or charged. Their late bytes still
	// contribute to the node's unattributed raw history, as unknown bytes do.
	return userID.String, nil
}

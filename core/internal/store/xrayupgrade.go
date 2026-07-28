package store

import (
	"fmt"
	"strings"
	"time"
)

// Upgrade states. Stored as these exact strings; migration 0012's uniqueness
// index depends on the in-flight set, so adding a state means revisiting it.
const (
	// UpgradeCanary: the canary node is fetching and switching.
	UpgradeCanary = "canary"
	// UpgradeAwaitingPromote: the canary came up; nobody has said to continue.
	UpgradeAwaitingPromote = "awaiting_promote"
	// UpgradePromoting: the rest of the fleet is being told to install.
	UpgradePromoting = "promoting"
	// UpgradeDone: every target reached a terminal phase successfully.
	UpgradeDone = "done"
	// UpgradeBlocked: something rolled back; the fleet stays where it is, and
	// this is the state an operator retries from after fixing the cause.
	UpgradeBlocked = "blocked"
)

// Upgrade is one fleet-wide kernel upgrade.
type Upgrade struct {
	ID           string `json:"id"`
	Version      string `json:"version"`
	State        string `json:"state"`
	CanaryNodeID string `json:"canary_node_id"`
	Message      string `json:"message"`
	StartedAt    int64  `json:"started_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

// InFlight reports whether this upgrade still needs attention.
func (u Upgrade) InFlight() bool {
	switch u.State {
	case UpgradeCanary, UpgradeAwaitingPromote, UpgradePromoting, UpgradeBlocked:
		return true
	}
	return false
}

const upgradeCols = `id, version, state, COALESCE(canary_node_id, ''), message, started_at, updated_at`

func scanUpgrade(row interface{ Scan(...any) error }) (Upgrade, error) {
	var u Upgrade
	err := row.Scan(&u.ID, &u.Version, &u.State, &u.CanaryNodeID, &u.Message, &u.StartedAt, &u.UpdatedAt)
	return u, err
}

// StartUpgrade opens a fleet upgrade with a canary.
//
// Fails while another is in flight, and that failure comes from the database's
// own uniqueness constraint rather than from a check here — a check would have
// a window between reading and writing, and this is exactly the operation two
// admins press at the same time.
func (s *Store) StartUpgrade(version, canaryNodeID string, at time.Time) (Upgrade, error) {
	u := Upgrade{
		ID:           NewID(),
		Version:      version,
		State:        UpgradeCanary,
		CanaryNodeID: canaryNodeID,
		StartedAt:    at.Unix(),
		UpdatedAt:    at.Unix(),
	}
	_, err := s.db.Exec(`
		INSERT INTO xray_upgrades (id, version, state, canary_node_id, message, started_at, updated_at)
		VALUES (?, ?, ?, ?, '', ?, ?)`,
		u.ID, u.Version, u.State, nullable(canaryNodeID), u.StartedAt, u.UpdatedAt)
	if err != nil {
		if IsConstraint(err) {
			// Two different constraints reach here and they mean opposite
			// things: the partial unique index on in-flight upgrades, and the
			// foreign key on canary_node_id. Reporting a bad node id as "an
			// upgrade is already in progress" sent an operator looking for an
			// upgrade that does not exist. Ask which it was.
			if _, ferr := s.ActiveUpgrade(); ferr != nil {
				return Upgrade{}, fmt.Errorf("cannot start an upgrade on node %q: no such node", canaryNodeID)
			}
			return Upgrade{}, fmt.Errorf("another kernel upgrade is already in progress")
		}
		return Upgrade{}, err
	}
	return u, nil
}

// ActiveUpgrade returns the in-flight upgrade, or sql.ErrNoRows.
func (s *Store) ActiveUpgrade() (Upgrade, error) {
	return scanUpgrade(s.db.QueryRow(
		`SELECT ` + upgradeCols + ` FROM xray_upgrades WHERE active_marker = 'active'`))
}

// SetUpgradeState moves an upgrade, but only from the state the caller thinks
// it is in.
//
// The `from` guard is what keeps two concurrent transitions from both
// succeeding: a canary status arriving at the same moment an operator presses
// promote would otherwise leave the upgrade in whichever state lost the race,
// with the other transition's side effects already carried out.
func (s *Store) SetUpgradeState(id, from, to, message string, at time.Time) (bool, error) {
	res, err := s.db.Exec(`
		UPDATE xray_upgrades SET state = ?, message = ?, updated_at = ?
		WHERE id = ? AND state = ?`, to, message, at.Unix(), id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpgradeHistory lists upgrades, newest first.
func (s *Store) UpgradeHistory(limit int) ([]Upgrade, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.Query(
		`SELECT `+upgradeCols+` FROM xray_upgrades ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upgrade
	for rows.Next() {
		u, err := scanUpgrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// IsConstraint reports whether err is a uniqueness/constraint violation. Used
// to turn "another upgrade is in flight" from a database error into the
// sentence an operator needs.
func IsConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "constraint failed")
}

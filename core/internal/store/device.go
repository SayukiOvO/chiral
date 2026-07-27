package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// Persistence for observed source addresses.
//
// "Who is online right now" is answered from memory (core/internal/online) and
// never touches this table. What lands here is the durable record: which
// addresses a user has connected from, and when they were last seen. That is a
// different question with a different lifetime, and conflating them would put
// a write on every polling round of every node.

const (
	// DeviceRetention bounds how long an observation is kept. Shorter than
	// TrafficRetention's 90 days on purpose: this is personal data, and a
	// month-old address answers no operational question. The two windows
	// differing is a decision, not an oversight.
	DeviceRetention = 30 * 24 * time.Hour
	// DeviceRefreshInterval is how stale last_seen must be before a repeat
	// sighting is written. Without it, a 30-second poll would write a row per
	// user per node per round, forever, against a handle pinned to one
	// connection.
	DeviceRefreshInterval = 15 * time.Minute
)

// Device is one observed source address.
type Device struct {
	IP        string `json:"ip"`
	NodeID    string `json:"node_id"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
}

// deviceAAD binds a sealed address to the exact row that holds it, so a
// ciphertext moved to another user's row fails to open rather than decoding as
// that user's address.
func deviceAAD(userID, ipHash string) string {
	return "user-device:" + userID + ":" + ipHash
}

// hashIP is the stable key for an address. Not a security boundary — the
// address space is small enough to enumerate — purely a primary key that
// survives the randomised sealing.
func hashIP(ip string) string {
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}

// RecordDevice notes that a user was seen from an address, refreshing an
// existing row at most once per DeviceRefreshInterval.
//
// The conditional in the upsert is what keeps this cheap: the WHERE clause
// makes a repeat sighting inside the interval a no-op at the database, rather
// than a read followed by a decision followed by a write.
func (s *Store) RecordDevice(userID, ip, nodeID string, at time.Time) error {
	if userID == "" || ip == "" {
		return fmt.Errorf("recording a device needs a user and an address")
	}
	ipHash := hashIP(ip)
	sealed, err := s.box.Seal(deviceAAD(userID, ipHash), ip)
	if err != nil {
		return fmt.Errorf("sealing the address: %w", err)
	}
	now := at.Unix()
	_, err = s.db.Exec(`
		INSERT INTO user_devices (user_id, ip_hash, ip_enc, last_node_id, first_seen, last_seen)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (user_id, ip_hash) DO UPDATE SET
			last_node_id = excluded.last_node_id,
			last_seen    = excluded.last_seen
		WHERE excluded.last_seen - user_devices.last_seen >= ?`,
		userID, ipHash, sealed, nodeID, now, now, int64(DeviceRefreshInterval.Seconds()))
	return err
}

// UserDevices lists a user's observed addresses, most recently seen first.
//
// A row whose address cannot be opened is skipped rather than failing the
// query: CHIRAL_SECRET_KEY may have been rotated, and losing one address is
// better than a page that will not load at all.
func (s *Store) UserDevices(userID string) ([]Device, error) {
	rows, err := s.db.Query(`
		SELECT ip_hash, ip_enc, last_node_id, first_seen, last_seen
		FROM user_devices WHERE user_id = ? ORDER BY last_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Device
	unreadable := 0
	var firstErr error
	for rows.Next() {
		var d Device
		var ipHash, sealed string
		if err := rows.Scan(&ipHash, &sealed, &d.NodeID, &d.FirstSeen, &d.LastSeen); err != nil {
			return nil, err
		}
		ip, err := s.box.Open(deviceAAD(userID, ipHash), sealed)
		if err != nil {
			unreadable++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		d.IP = ip
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// One bad row is worth skipping; every row failing means the key is wrong,
	// and answering "no addresses recorded" to that would be a lie the
	// operator has no way to see through.
	if len(out) == 0 && unreadable > 0 {
		return nil, fmt.Errorf("none of %d recorded addresses could be decrypted: %w", unreadable, firstErr)
	}
	return out, nil
}

// PruneDevices drops observations past DeviceRetention.
func (s *Store) PruneDevices(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM user_devices WHERE last_seen < ?`,
		now.Add(-DeviceRetention).Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

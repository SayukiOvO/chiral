package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// TrafficDelta carries one authenticated node's raw, unmultiplied traffic.
// User names are credential emails; inbound/outbound names are runtime tags.
type TrafficDelta struct {
	Scope string
	Name  string
	Up    int64
	Down  int64
}

const statsContinuityGapReason = "provider identity or snapshot epoch changed; new baseline excludes unverified historical usage"

// RecordStatsBatch commits billing, chart data and a replay receipt atomically.
// Unknown (revoked or machine) credentials never charge a human. An email owned
// by another node is rejected, rather than trusting an agent's attribution.
// applied is false for an already committed sequence, including old replays.
func (s *Store) RecordStatsBatch(nodeID, reporterID string, sequence uint64, entries []TrafficDelta, continuityIndeterminate bool, at time.Time) (applied bool, err error) {
	if !chiralv1.ValidStatsIdentity(reporterID, sequence) {
		return false, fmt.Errorf("invalid acknowledged traffic identity or sequence")
	}
	if len(entries) > chiralv1.MaxStatsBatchEntries {
		return false, fmt.Errorf("traffic batch exceeds entry limit")
	}
	if continuityIndeterminate && len(entries) != 0 {
		return false, fmt.Errorf("traffic continuity gap must not contain entries")
	}
	canonical := append([]TrafficDelta(nil), entries...)
	sort.Slice(canonical, func(i, j int) bool {
		if canonical[i].Scope == canonical[j].Scope {
			return canonical[i].Name < canonical[j].Name
		}
		return canonical[i].Scope < canonical[j].Scope
	})
	for i, entry := range canonical {
		if entry.Scope != "user" && entry.Scope != "inbound" && entry.Scope != "outbound" {
			return false, fmt.Errorf("invalid traffic scope")
		}
		if !chiralv1.ValidStatsName(entry.Name) || entry.Up < 0 || entry.Down < 0 || entry.Up > math.MaxInt64-entry.Down {
			return false, fmt.Errorf("invalid traffic entry")
		}
		if i > 0 && canonical[i-1].Scope == entry.Scope && canonical[i-1].Name == entry.Name {
			return false, fmt.Errorf("duplicate traffic entry")
		}
	}
	raw, err := json.Marshal(struct {
		Entries                 []TrafficDelta `json:"entries"`
		ContinuityIndeterminate bool           `json:"continuity_indeterminate"`
	}{canonical, continuityIndeterminate})
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var last uint64
	var previousDigest string
	err = tx.QueryRow(`SELECT sequence, digest FROM stats_receipts WHERE node_id = ? AND reporter_id = ?`, nodeID, reporterID).Scan(&last, &previousDigest)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if sequence <= last {
		if sequence == last && digest != previousDigest {
			return false, fmt.Errorf("traffic sequence was reused with different content")
		}
		if sequence < last {
			// The high-water receipt retains only the last digest, but gap
			// markers remain permanent evidence. A replay cannot invent or
			// erase a previously committed continuity marker.
			var recordedGap bool
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM stats_continuity_gaps
				WHERE node_id = ? AND reporter_id = ? AND sequence = ?)`, nodeID, reporterID, sequence).Scan(&recordedGap); err != nil {
				return false, err
			}
			if recordedGap != continuityIndeterminate {
				return false, fmt.Errorf("traffic sequence was reused with a different continuity marker")
			}
		}
		return false, nil
	}
	if sequence != last+1 {
		return false, fmt.Errorf("traffic sequence gap")
	}
	for _, entry := range canonical {
		if entry.Scope == "user" {
			if err := recordCredentialDelta(tx, nodeID, entry, at); err != nil {
				return false, err
			}
		} else if err := recordRuntimeDelta(tx, nodeID, entry); err != nil {
			return false, err
		}
	}
	if continuityIndeterminate {
		if _, err := tx.Exec(`INSERT INTO stats_continuity_gaps (node_id, reporter_id, sequence, received_at, reason)
			VALUES (?, ?, ?, ?, ?)`, nodeID, reporterID, sequence, at.Unix(), statsContinuityGapReason); err != nil {
			return false, err
		}
	}
	_, err = tx.Exec(`INSERT INTO stats_receipts (node_id, reporter_id, sequence, digest, continuity_indeterminate, received_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (node_id, reporter_id) DO UPDATE SET
		sequence = excluded.sequence, digest = excluded.digest, continuity_indeterminate = excluded.continuity_indeterminate,
		received_at = excluded.received_at`, nodeID, reporterID, sequence, digest, continuityIndeterminate, at.Unix())
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func checkedTrafficSum(previous, delta int64) (int64, error) {
	if previous < 0 || delta < 0 || previous > math.MaxInt64-delta {
		return 0, fmt.Errorf("traffic total exceeds supported range")
	}
	return previous + delta, nil
}

func recordCredentialDelta(tx *sql.Tx, nodeID string, entry TrafficDelta, at time.Time) error {
	var userID, owner string
	var rate float64
	var oldUp, oldDown, oldUsage int64
	err := tx.QueryRow(`SELECT c.user_id, c.node_id, c.up_bytes, c.down_bytes, u.used_bytes,
		COALESCE(x.traffic_rate, r.traffic_rate, n.traffic_rate, 1.0)
		FROM credentials c JOIN nodes n ON n.id = c.node_id JOIN users u ON u.id = c.user_id
		LEFT JOIN external_proxies x ON x.id = c.exit_proxy_id
		LEFT JOIN node_relays r ON r.id = c.exit_relay_id
		WHERE c.email = ?`, entry.Name).Scan(&userID, &owner, &oldUp, &oldDown, &oldUsage, &rate)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		if owner != nodeID {
			return fmt.Errorf("traffic credential does not belong to the reporting node")
		}
		up, err := checkedTrafficSum(oldUp, entry.Up)
		if err != nil {
			return err
		}
		down, err := checkedTrafficSum(oldDown, entry.Down)
		if err != nil {
			return err
		}
		billed, err := billedTrafficBytes(entry.Up, entry.Down, rate)
		if err != nil {
			return err
		}
		usage, err := checkedTrafficSum(oldUsage, billed)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE credentials SET up_bytes = ?, down_bytes = ? WHERE email = ?`, up, down, entry.Name); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE users SET used_bytes = ? WHERE id = ?`, usage, userID); err != nil {
			return err
		}
	}
	if entry.Up == 0 && entry.Down == 0 {
		return nil
	}
	// Preserve the existing chart policy: an unknown credential contributes to
	// its node's raw traffic history, but not a subscriber's quota.
	bucket := at.Truncate(TrafficBucket).Unix()
	oldUp, oldDown = 0, 0
	err = tx.QueryRow(`SELECT up_bytes, down_bytes FROM traffic_buckets WHERE bucket_start = ? AND node_id = ? AND user_id = ?`, bucket, nodeID, userID).Scan(&oldUp, &oldDown)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	up, err := checkedTrafficSum(oldUp, entry.Up)
	if err != nil {
		return err
	}
	down, err := checkedTrafficSum(oldDown, entry.Down)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO traffic_buckets (bucket_start, node_id, user_id, up_bytes, down_bytes)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (bucket_start, node_id, user_id) DO UPDATE SET
		up_bytes = excluded.up_bytes, down_bytes = excluded.down_bytes`, bucket, nodeID, userID, up, down)
	return err
}

func recordRuntimeDelta(tx *sql.Tx, nodeID string, entry TrafficDelta) error {
	var oldUp, oldDown int64
	err := tx.QueryRow(`SELECT up_bytes, down_bytes FROM runtime_traffic_totals WHERE node_id = ? AND scope = ? AND name = ?`, nodeID, entry.Scope, entry.Name).Scan(&oldUp, &oldDown)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	up, err := checkedTrafficSum(oldUp, entry.Up)
	if err != nil {
		return err
	}
	down, err := checkedTrafficSum(oldDown, entry.Down)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO runtime_traffic_totals (node_id, scope, name, up_bytes, down_bytes)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (node_id, scope, name) DO UPDATE SET
		up_bytes = excluded.up_bytes, down_bytes = excluded.down_bytes`, nodeID, entry.Scope, entry.Name, up, down)
	return err
}

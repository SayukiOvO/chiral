package store

import (
	"time"
)

// Sampling and retention. Heartbeats arrive every ~10s but a chart does not
// need that resolution, and keeping it would grow the database for no benefit.
const (
	// NodeSampleInterval is the finest resolution kept for node resources.
	NodeSampleInterval = 60 * time.Second
	// NodeSampleRetention bounds the raw window; older samples are pruned.
	NodeSampleRetention = 7 * 24 * time.Hour
	// TrafficBucket is the granularity traffic deltas are accumulated into.
	TrafficBucket = time.Hour
	// TrafficRetention keeps enough history for a quota period's worth of
	// charts without growing without limit.
	TrafficRetention = 90 * 24 * time.Hour
)

// NodeSample is one point of a node's resource history.
type NodeSample struct {
	At             int64   `json:"at"`
	CPUPercent     float64 `json:"cpu_percent"`
	MemUsedBytes   int64   `json:"mem_used_bytes"`
	MemTotalBytes  int64   `json:"mem_total_bytes"`
	DiskUsedBytes  int64   `json:"disk_used_bytes"`
	DiskTotalBytes int64   `json:"disk_total_bytes"`
	NetTxBps       int64   `json:"net_tx_bps"`
	NetRxBps       int64   `json:"net_rx_bps"`
}

// PutNodeSample records a resource sample, rounded down to the sample
// interval. Heartbeats land far more often than that, so the primary key
// silently absorbs the extras: the first heartbeat of each interval wins,
// which is a fair representative and keeps writes cheap.
func (s *Store) PutNodeSample(nodeID string, at time.Time, sample NodeSample) error {
	bucket := at.Truncate(NodeSampleInterval).Unix()
	_, err := s.db.Exec(`
		INSERT INTO node_samples
			(node_id, at, cpu_percent, mem_used_bytes, mem_total_bytes,
			 disk_used_bytes, disk_total_bytes, net_tx_bps, net_rx_bps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (node_id, at) DO NOTHING`,
		nodeID, bucket, sample.CPUPercent, sample.MemUsedBytes, sample.MemTotalBytes,
		sample.DiskUsedBytes, sample.DiskTotalBytes, sample.NetTxBps, sample.NetRxBps)
	return err
}

// NodeSamples returns a node's samples in [from, to), oldest first.
func (s *Store) NodeSamples(nodeID string, from, to time.Time) ([]NodeSample, error) {
	rows, err := s.db.Query(`
		SELECT at, cpu_percent, mem_used_bytes, mem_total_bytes,
		       disk_used_bytes, disk_total_bytes, net_tx_bps, net_rx_bps
		FROM node_samples
		WHERE node_id = ? AND at >= ? AND at < ?
		ORDER BY at`, nodeID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeSample{}
	for rows.Next() {
		var s NodeSample
		if err := rows.Scan(&s.At, &s.CPUPercent, &s.MemUsedBytes, &s.MemTotalBytes,
			&s.DiskUsedBytes, &s.DiskTotalBytes, &s.NetTxBps, &s.NetRxBps); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TrafficPoint is one bucket of accumulated traffic.
type TrafficPoint struct {
	At        int64 `json:"at"`
	UpBytes   int64 `json:"up_bytes"`
	DownBytes int64 `json:"down_bytes"`
}

// AddTraffic accumulates one delta into its bucket. userID may be empty when
// the credential's owner is already gone; the fleet total still counts.
func (s *Store) AddTraffic(nodeID, userID string, at time.Time, up, down int64) error {
	if up == 0 && down == 0 {
		return nil
	}
	bucket := at.Truncate(TrafficBucket).Unix()
	_, err := s.db.Exec(`
		INSERT INTO traffic_buckets (bucket_start, node_id, user_id, up_bytes, down_bytes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (bucket_start, node_id, user_id) DO UPDATE SET
			up_bytes = up_bytes + excluded.up_bytes,
			down_bytes = down_bytes + excluded.down_bytes`,
		bucket, nodeID, userID, up, down)
	return err
}

// TrafficSeries sums traffic per bucket over a window. Empty userID and
// nodeID mean "the whole fleet"; either one narrows it.
func (s *Store) TrafficSeries(userID, nodeID string, from, to time.Time) ([]TrafficPoint, error) {
	query := `
		SELECT bucket_start, SUM(up_bytes), SUM(down_bytes)
		FROM traffic_buckets
		WHERE bucket_start >= ? AND bucket_start < ?`
	args := []any{from.Unix(), to.Unix()}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	if nodeID != "" {
		query += ` AND node_id = ?`
		args = append(args, nodeID)
	}
	query += ` GROUP BY bucket_start ORDER BY bucket_start`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrafficPoint{}
	for rows.Next() {
		var p TrafficPoint
		if err := rows.Scan(&p.At, &p.UpBytes, &p.DownBytes); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneHistory drops series data past its retention window and reports how
// many rows went. Called periodically; the panel is the only writer, so a
// plain delete is enough.
func (s *Store) PruneHistory(now time.Time) (int64, error) {
	var total int64
	res, err := s.db.Exec(`DELETE FROM node_samples WHERE at < ?`,
		now.Add(-NodeSampleRetention).Unix())
	if err != nil {
		return total, err
	}
	n, _ := res.RowsAffected()
	total += n

	res, err = s.db.Exec(`DELETE FROM traffic_buckets WHERE bucket_start < ?`,
		now.Add(-TrafficRetention).Unix())
	if err != nil {
		return total, err
	}
	n, _ = res.RowsAffected()
	return total + n, nil
}

// CredentialOwner resolves the user and node a stats email belongs to, so a
// traffic report can be filed against the right series. Returns IsNotFound
// when the credential is gone.
func (s *Store) CredentialOwner(email string) (userID, nodeID string, err error) {
	err = s.db.QueryRow(`SELECT user_id, node_id FROM credentials WHERE email = ?`, email).
		Scan(&userID, &nodeID)
	return userID, nodeID, err
}

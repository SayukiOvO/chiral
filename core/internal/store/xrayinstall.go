package store

import "time"

// XrayInstall is a node's most recent kernel install attempt.
type XrayInstall struct {
	NodeID      string `json:"node_id"`
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Activate    bool   `json:"activate"`
	// Phase mirrors chiral.v1.XrayInstallPhase by NAME, not by number: a dump
	// stays readable, and a renumbered enum cannot silently reinterpret rows
	// written by an older build.
	Phase     string `json:"phase"`
	Message   string `json:"message"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const installCols = `node_id, version, download_url, sha256, activate, phase, message, started_at, updated_at`

func scanInstall(row interface{ Scan(...any) error }) (XrayInstall, error) {
	var in XrayInstall
	var activate int
	err := row.Scan(&in.NodeID, &in.Version, &in.DownloadURL, &in.SHA256, &activate,
		&in.Phase, &in.Message, &in.StartedAt, &in.UpdatedAt)
	in.Activate = activate != 0
	return in, err
}

// StartXrayInstall records that a node has been told to install a version,
// replacing whatever attempt came before.
func (s *Store) StartXrayInstall(in XrayInstall, at time.Time) error {
	activate := 0
	if in.Activate {
		activate = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO node_xray_installs (`+installCols+`)
		VALUES (?, ?, ?, ?, ?, ?, '', ?, ?)
		ON CONFLICT (node_id) DO UPDATE SET
			version      = excluded.version,
			download_url = excluded.download_url,
			sha256       = excluded.sha256,
			activate     = excluded.activate,
			phase        = excluded.phase,
			message      = '',
			started_at   = excluded.started_at,
			updated_at   = excluded.updated_at`,
		in.NodeID, in.Version, in.DownloadURL, in.SHA256, activate, in.Phase,
		at.Unix(), at.Unix())
	return err
}

// UpdateXrayInstall records a phase change reported by the agent.
//
// The version in the WHERE clause is the whole point. A status can arrive after
// Core has moved on to a different version — a slow node finishing an attempt
// that was superseded — and writing it anyway would stamp the new attempt with
// the old one's outcome, which is how a failed upgrade gets read as a success.
func (s *Store) UpdateXrayInstall(nodeID, version, phase, message string, at time.Time) (bool, error) {
	res, err := s.db.Exec(`
		UPDATE node_xray_installs SET phase = ?, message = ?, updated_at = ?
		WHERE node_id = ? AND version = ?`,
		phase, message, at.Unix(), nodeID, version)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// XrayInstallFor returns a node's latest attempt, or sql.ErrNoRows.
func (s *Store) XrayInstallFor(nodeID string) (XrayInstall, error) {
	return scanInstall(s.db.QueryRow(
		`SELECT `+installCols+` FROM node_xray_installs WHERE node_id = ?`, nodeID))
}

// XrayInstalls lists every node's latest attempt, most recently updated first.
func (s *Store) XrayInstalls() ([]XrayInstall, error) {
	rows, err := s.db.Query(`SELECT ` + installCols + ` FROM node_xray_installs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []XrayInstall
	for rows.Next() {
		in, err := scanInstall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// SetNodePlatform records the GOOS/GOARCH a node reported, and says whether it
// changed. An empty platform is ignored: an agent too old to report one must
// not blank what a newer one established, or the node becomes un-upgradable
// with no visible reason.
func (s *Store) SetNodePlatform(id, platform string) (bool, error) {
	if platform == "" {
		return false, nil
	}
	res, err := s.db.Exec(
		`UPDATE nodes SET platform = ? WHERE id = ? AND platform != ?`, platform, id, platform)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

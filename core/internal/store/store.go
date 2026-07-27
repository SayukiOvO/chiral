// Package store is the SQLite (WAL) persistence layer. It is the only place
// that talks to the database; other packages go through it.
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/SayukiOvO/chiral/core/internal/secret"
	"github.com/SayukiOvO/chiral/core/migrations"
)

type Store struct {
	db *sql.DB
	// box encrypts secret variable components at rest. Never nil — a Box
	// built from an empty key stores values in the clear.
	box *secret.Box
}

// Open opens (creating if needed) the SQLite database at path and applies any
// pending migrations. box encrypts secret variable components; pass a Box
// built from an empty key to store them in the clear.
func Open(path string, box *secret.Box) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite handles one writer at a time; a single connection avoids
	// SQLITE_BUSY churn under concurrent writes.
	db.SetMaxOpenConns(1)
	if box == nil {
		box, _ = secret.NewBox("")
	}
	s := &Store{db: db, box: box}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var done int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}
		script, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(script)); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, time.Now().Unix()); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// NewID returns a random 16-hex-char identifier.
func NewID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not recoverable
	}
	return hex.EncodeToString(b)
}

type Node struct {
	ID           string
	Name         string
	Hostname     string
	PublicIP     string
	AgentVersion string
	// XrayVersion is the version the node's LIVE Xray process was started
	// from; empty when nothing is running. XrayInstalledVersion is what the
	// next start would use. See migration 0010 for why one column could not be
	// both.
	XrayVersion          string
	XrayInstalledVersion string
	CreatedAt            int64
	RegisteredAt         sql.NullInt64
	LastSeenAt           sql.NullInt64
	// ConfigSkeleton is the node's config.json minus its inbounds, which are
	// rendered from the profiles bound to the node. Empty means the default.
	ConfigSkeleton string
	// DisplayName is the customer-facing label shown in the portal. Empty
	// means unset, and never falls back to Name: internal names encode the
	// provider and datacentre, which is not something to hand to subscribers
	// by accident. See migration 0009.
	DisplayName string
}

const nodeCols = `id, name, hostname, public_ip, agent_version, xray_version, xray_installed_version, created_at, registered_at, last_seen_at, config_skeleton, display_name`

// skeletonAAD / configAAD bind a ciphertext to the exact row that holds it.
func skeletonAAD(nodeID string) string { return "node-skeleton:" + nodeID }
func configAAD(nodeID string, version int64) string {
	return fmt.Sprintf("node-config:%s:%d", nodeID, version)
}

// scanNode is a method because a node's skeleton is encrypted at rest: it may
// carry credentials of its own (an outbound to an upstream proxy, say).
func (s *Store) scanNode(row interface{ Scan(...any) error }) (Node, error) {
	var n Node
	err := row.Scan(&n.ID, &n.Name, &n.Hostname, &n.PublicIP, &n.AgentVersion, &n.XrayVersion, &n.XrayInstalledVersion, &n.CreatedAt, &n.RegisteredAt, &n.LastSeenAt, &n.ConfigSkeleton, &n.DisplayName)
	if err != nil {
		return n, err
	}
	if n.ConfigSkeleton != "" {
		plain, oerr := s.box.Open(skeletonAAD(n.ID), n.ConfigSkeleton)
		if oerr != nil {
			return n, fmt.Errorf("opening config skeleton for node %s: %w", n.ID, oerr)
		}
		n.ConfigSkeleton = plain
	}
	return n, nil
}

// UpdateNode changes the operator-facing name and the customer-facing one.
//
// Two names because they have two audiences: `name` is what the operator uses
// to find a box and usually encodes the provider and datacentre, while
// display_name is what subscribers see. Blank display_name means "unset" and
// never falls back to name — see migration 0009.
func (s *Store) UpdateNode(id, name, displayName string) error {
	res, err := s.db.Exec(`UPDATE nodes SET name = ?, display_name = ? WHERE id = ?`,
		name, displayName, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CreateNode inserts a node awaiting registration via its join token.
func (s *Store) CreateNode(name, joinTokenHash string) (Node, error) {
	n := Node{ID: NewID(), Name: name, CreatedAt: time.Now().Unix()}
	_, err := s.db.Exec(`INSERT INTO nodes (id, name, join_token_hash, created_at) VALUES (?, ?, ?, ?)`,
		n.ID, n.Name, joinTokenHash, n.CreatedAt)
	return n, err
}

func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT ` + nodeCols + ` FROM nodes ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		n, err := s.scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNode(id string) (Node, error) {
	return s.scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE id = ?`, id))
}

func (s *Store) DeleteNode(id string) error {
	res, err := s.db.Exec(`DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// FindNodeByJoinTokenHash resolves a pending join token; sql.ErrNoRows means
// the token is unknown or already used.
func (s *Store) FindNodeByJoinTokenHash(hash string) (Node, error) {
	return s.scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE join_token_hash = ?`, hash))
}

func (s *Store) FindNodeByCredentialHash(hash string) (Node, error) {
	return s.scanNode(s.db.QueryRow(`SELECT `+nodeCols+` FROM nodes WHERE credential_hash = ?`, hash))
}

// RedeemJoinToken atomically exchanges a join token for the long-term
// credential: the token lookup, its invalidation, and the credential write
// are one guarded UPDATE, so a token can never be redeemed twice. Returns the
// redeemed node, or sql.ErrNoRows if the token is unknown or already used.
func (s *Store) RedeemJoinToken(joinTokenHash, credentialHash, hostname, agentVersion, publicIP string) (Node, error) {
	res, err := s.db.Exec(`UPDATE nodes SET credential_hash = ?, join_token_hash = NULL, hostname = ?, agent_version = ?, public_ip = ?, registered_at = ? WHERE join_token_hash = ?`,
		credentialHash, hostname, agentVersion, publicIP, time.Now().Unix(), joinTokenHash)
	if err != nil {
		return Node{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Node{}, sql.ErrNoRows
	}
	return s.FindNodeByCredentialHash(credentialHash)
}

// ResetJoinToken issues a fresh join token for a node, e.g. to recover an
// agent whose registration response was lost. Any previously issued
// credential stays valid until a new registration overwrites it.
func (s *Store) ResetJoinToken(id, joinTokenHash string) error {
	res, err := s.db.Exec(`UPDATE nodes SET join_token_hash = ? WHERE id = ?`, joinTokenHash, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateHello refreshes node facts reported in the stream's Hello frame.
// Empty publicIP keeps the previously recorded value.
func (s *Store) UpdateHello(id, publicIP, agentVersion, xrayVersion string) error {
	_, err := s.db.Exec(`UPDATE nodes SET public_ip = CASE WHEN ? = '' THEN public_ip ELSE ? END, agent_version = ?, xray_version = ?, xray_installed_version = ? WHERE id = ?`,
		publicIP, publicIP, agentVersion, xrayVersion, xrayVersion, id)
	return err
}

// SetXrayVersions records what a heartbeat reported, and returns whether that
// changed anything.
//
// Guarded by the WHERE clause rather than by a read-then-write: heartbeats
// arrive every few seconds per node against a handle pinned to a single
// connection, and the steady state is "no change". The returned bool is what
// the upgrade state machine watches — a version that changed without anybody
// asking is the signal that a node restarted into something unexpected.
func (s *Store) SetXrayVersions(id, running, installed string) (bool, error) {
	// An agent too old to report either field sends both empty. Writing that
	// through would blank what Hello established and make every pre-upgrade
	// node look like it has no kernel installed — a Core newer than its agents
	// is the normal state of a panel whose job is rolling upgrades out.
	if running == "" && installed == "" {
		return false, nil
	}
	res, err := s.db.Exec(`
		UPDATE nodes SET xray_version = ?, xray_installed_version = ?
		WHERE id = ? AND (xray_version != ? OR xray_installed_version != ?)`,
		running, installed, id, running, installed)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) TouchLastSeen(id string, ts int64) error {
	_, err := s.db.Exec(`UPDATE nodes SET last_seen_at = ? WHERE id = ?`, ts, id)
	return err
}

type NodeConfig struct {
	NodeID    string
	Version   int64
	Config    string
	CreatedAt int64
	Applied   int64 // 0 pending, 1 applied, -1 failed
	Error     string
}

// InsertConfig stores config as the next version for the node and returns it.
func (s *Store) InsertConfig(nodeID, config string) (NodeConfig, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return NodeConfig{}, err
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) + 1 FROM node_configs WHERE node_id = ?`, nodeID).Scan(&version); err != nil {
		return NodeConfig{}, err
	}
	c := NodeConfig{NodeID: nodeID, Version: version, Config: config, CreatedAt: time.Now().Unix()}
	// A rendered config contains the very private keys that variable_components
	// encrypts — storing it in the clear would hand every node's key material
	// to anyone holding a copy of the database, which is exactly the threat
	// the secret package exists to stop.
	sealed, err := s.box.Seal(configAAD(nodeID, version), config)
	if err != nil {
		return NodeConfig{}, err
	}
	if _, err := tx.Exec(`INSERT INTO node_configs (node_id, version, config, created_at) VALUES (?, ?, ?, ?)`,
		c.NodeID, c.Version, sealed, c.CreatedAt); err != nil {
		return NodeConfig{}, err
	}
	return c, tx.Commit()
}

// ConfigVersion is one entry of a node's config history, without the config
// itself — the bodies are large and sealed, and a version list does not need
// them.
type ConfigVersion struct {
	Version   int64  `json:"version"`
	CreatedAt int64  `json:"created_at"`
	Applied   int    `json:"applied"`
	Error     string `json:"error"`
}

// ConfigVersions lists a node's history, newest first.
func (s *Store) ConfigVersions(nodeID string, limit int) ([]ConfigVersion, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(`
		SELECT version, created_at, applied, error FROM node_configs
		WHERE node_id = ? ORDER BY version DESC LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConfigVersion{}
	for rows.Next() {
		var v ConfigVersion
		if err := rows.Scan(&v.Version, &v.CreatedAt, &v.Applied, &v.Error); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ConfigAt returns one stored version, decrypted.
func (s *Store) ConfigAt(nodeID string, version int64) (NodeConfig, error) {
	return s.scanConfig(s.db.QueryRow(
		`SELECT node_id, version, config, created_at, applied, error FROM node_configs
		 WHERE node_id = ? AND version = ?`, nodeID, version))
}

// ConfigHistoryDepth is how many versions per node survive the sweep.
//
// The table was never pruned before, so every apply left a full config blob
// behind forever. Twenty is far more than anyone rolls back through and keeps
// the growth bounded; the newest is always the one a reconnecting agent gets,
// so pruning can never strand a node.
const ConfigHistoryDepth = 20

// PruneConfigs drops all but the newest ConfigHistoryDepth versions per node.
func (s *Store) PruneConfigs() (int64, error) {
	res, err := s.db.Exec(`
		DELETE FROM node_configs WHERE (node_id, version) IN (
			SELECT node_id, version FROM (
				SELECT node_id, version,
				       ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY version DESC) AS rn
				FROM node_configs
			) WHERE rn > ?
		)`, ConfigHistoryDepth)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

func (s *Store) LatestConfig(nodeID string) (NodeConfig, error) {
	return s.scanConfig(s.db.QueryRow(
		`SELECT node_id, version, config, created_at, applied, error FROM node_configs WHERE node_id = ? ORDER BY version DESC LIMIT 1`, nodeID))
}

func (s *Store) scanConfig(row interface{ Scan(...any) error }) (NodeConfig, error) {
	var c NodeConfig
	if err := row.Scan(&c.NodeID, &c.Version, &c.Config, &c.CreatedAt, &c.Applied, &c.Error); err != nil {
		return c, err
	}
	plain, err := s.box.Open(configAAD(c.NodeID, c.Version), c.Config)
	if err != nil {
		return c, fmt.Errorf("opening config v%d for node %s: %w", c.Version, c.NodeID, err)
	}
	c.Config = plain
	return c, nil
}

// LatestPushableConfig returns the newest config that has not failed
// agent-side validation (pending or applied). Re-push paths use this so a
// config rejected by `xray -test` is never pushed again and again.
func (s *Store) LatestPushableConfig(nodeID string) (NodeConfig, error) {
	return s.scanConfig(s.db.QueryRow(
		`SELECT node_id, version, config, created_at, applied, error FROM node_configs WHERE node_id = ? AND applied >= 0 ORDER BY version DESC LIMIT 1`, nodeID))
}

func (s *Store) SetConfigResult(nodeID string, version int64, applied bool, errMsg string) error {
	state := int64(1)
	if !applied {
		state = -1
	}
	_, err := s.db.Exec(`UPDATE node_configs SET applied = ?, error = ? WHERE node_id = ? AND version = ?`,
		state, errMsg, nodeID, version)
	return err
}

// SetConfigSkeleton replaces a node's config skeleton, encrypting it at rest.
func (s *Store) SetConfigSkeleton(nodeID, skeleton string) error {
	sealed := skeleton
	if skeleton != "" {
		var err error
		if sealed, err = s.box.Seal(skeletonAAD(nodeID), skeleton); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(`UPDATE nodes SET config_skeleton = ? WHERE id = ?`, sealed, nodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// IsNotFound reports whether err means "row does not exist".
func IsNotFound(err error) bool {
	return err == sql.ErrNoRows || (err != nil && strings.Contains(err.Error(), sql.ErrNoRows.Error()))
}

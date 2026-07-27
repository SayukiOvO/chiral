package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	errPlaintextSubToken = errors.New(
		"store: refusing to store a subscription token without CHIRAL_SECRET_KEY")
	errUserScopedSeriesNeedsUser = errors.New(
		"store: a user-scoped traffic series requires a user")
)

// The end user's side: login identities, their sessions, and the short-lived
// tokens for claiming an account or resetting a password.
//
// Kept in its own file and its own tables. See migration 0009 for why none of
// this lives on `users` or in the admin tables.

const (
	// PortalSessionTTL is long because a customer checks their quota once a
	// month, not once a day, and being logged out is pure friction for them.
	// Admin sessions stay at a week: an admin session is a foothold on the
	// fleet, a portal session is a view of one person's own usage.
	PortalSessionTTL = 30 * 24 * time.Hour
	// PortalClaimTTL bounds an operator-issued claim link.
	PortalClaimTTL = 7 * 24 * time.Hour
	// PortalResetTTL bounds a password reset. Short: it is emailed, and an
	// email sits in an inbox indefinitely.
	PortalResetTTL = 30 * time.Minute
	// MaxPortalAttempts is how many wrong answers a challenge tolerates before
	// it is destroyed, so a short code cannot be walked through.
	MaxPortalAttempts = 5
)

// Portal challenge purposes. Prefixed to keep them distinct from the admin
// MFA purposes in mfa.go, which live in a different table and must never be
// confused with these at a call site.
const (
	PortalPurposeClaim       = "claim"
	PortalPurposeReset       = "password_reset"
	PortalPurposeEmailVerify = "email_verify"
)

// UserAccount is an end user's login identity.
type UserAccount struct {
	UserID        string
	Email         string
	PasswordHash  string
	EmailVerified bool
	Disabled      bool
	CreatedAt     int64
	UpdatedAt     int64
	LastLogin     int64
}

// NormalizeEmail is how an address is stored and compared.
//
// Lowercasing the local part departs from RFC 5321, which makes it
// case-sensitive. No provider anyone actually uses honours that, and treating
// Mai@ and mai@ as different accounts produces duplicate signups and
// unresolvable "my password is wrong" reports.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

const userAccountCols = `user_id, email, password_hash, email_verified, disabled, created_at, updated_at, last_login`

func scanUserAccount(row interface{ Scan(...any) error }) (UserAccount, error) {
	var a UserAccount
	err := row.Scan(&a.UserID, &a.Email, &a.PasswordHash, &a.EmailVerified,
		&a.Disabled, &a.CreatedAt, &a.UpdatedAt, &a.LastLogin)
	return a, err
}

// CreateUserAccount attaches a login identity to an existing proxy user.
func (s *Store) CreateUserAccount(userID, email, passwordHash string) (UserAccount, error) {
	now := time.Now().Unix()
	a := UserAccount{
		UserID: userID, Email: NormalizeEmail(email), PasswordHash: passwordHash,
		CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.db.Exec(`
		INSERT INTO user_accounts (`+userAccountCols+`)
		VALUES (?, ?, ?, 0, 0, ?, ?, 0)`,
		a.UserID, a.Email, a.PasswordHash, a.CreatedAt, a.UpdatedAt)
	return a, err
}

func (s *Store) UserAccountByEmail(email string) (UserAccount, error) {
	return scanUserAccount(s.db.QueryRow(
		`SELECT `+userAccountCols+` FROM user_accounts WHERE email = ?`, NormalizeEmail(email)))
}

func (s *Store) UserAccount(userID string) (UserAccount, error) {
	return scanUserAccount(s.db.QueryRow(
		`SELECT `+userAccountCols+` FROM user_accounts WHERE user_id = ?`, userID))
}

// SetUserAccountPassword changes a password and drops every session for that
// account in the same transaction.
//
// Changing a password is what someone does when they think it has been
// compromised, so leaving the attacker's session alive would defeat the point.
// Mirrors SetAdminPassword.
func (s *Store) SetUserAccountPassword(userID, passwordHash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE user_accounts SET password_hash = ?, updated_at = ? WHERE user_id = ?`,
		passwordHash, time.Now().Unix(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM portal_sessions WHERE user_id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetUserAccountDisabled switches portal access and drops live sessions when
// switching it off, so the block takes effect now rather than in 30 days.
func (s *Store) SetUserAccountDisabled(userID string, disabled bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE user_accounts SET disabled = ?, updated_at = ? WHERE user_id = ?`,
		disabled, time.Now().Unix(), userID); err != nil {
		return err
	}
	if disabled {
		if _, err := tx.Exec(`DELETE FROM portal_sessions WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) TouchUserAccountLogin(userID string) error {
	_, err := s.db.Exec(`UPDATE user_accounts SET last_login = ? WHERE user_id = ?`,
		time.Now().Unix(), userID)
	return err
}

// --- sessions ---

func (s *Store) CreatePortalSession(tokenHash, userID string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.Exec(`
		INSERT INTO portal_sessions (token_hash, user_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		tokenHash, userID, now.Unix(), now.Add(ttl).Unix())
	return err
}

// UserByPortalSession resolves a portal token to its user.
//
// The join onto user_accounts with disabled = 0 is not decoration: without it,
// switching portal access off would leave the person's existing session
// working for up to PortalSessionTTL. Same reasoning as AdminBySession's
// disabled check.
//
// It also deliberately does NOT check users.enabled. A suspended subscriber
// should be able to sign in and read why their service stopped, rather than
// meeting a login failure that tells them nothing.
func (s *Store) UserByPortalSession(tokenHash string) (User, error) {
	var userID string
	err := s.db.QueryRow(`
		SELECT ps.user_id FROM portal_sessions ps
		JOIN user_accounts ua ON ua.user_id = ps.user_id
		WHERE ps.token_hash = ? AND ps.expires_at > ? AND ua.disabled = 0`,
		tokenHash, time.Now().Unix()).Scan(&userID)
	if err != nil {
		return User{}, err
	}
	return s.GetUser(userID)
}

func (s *Store) DeletePortalSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM portal_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeletePortalSessionsFor(userID string) error {
	_, err := s.db.Exec(`DELETE FROM portal_sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) PrunePortalSessions(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM portal_sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --- challenges ---

type PortalChallenge struct {
	UserID   string
	Purpose  string
	Data     string
	Attempts int
}

// CreatePortalChallenge stores a challenge, replacing any existing one on the
// same key. Upsert rather than insert so "send me another link" works instead
// of colliding on the primary key.
func (s *Store) CreatePortalChallenge(tokenHash, userID, purpose, data string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.Exec(`
		INSERT INTO portal_challenges (token_hash, user_id, purpose, data, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (token_hash) DO UPDATE SET
			user_id    = excluded.user_id,
			purpose    = excluded.purpose,
			data       = excluded.data,
			attempts   = 0,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at`,
		tokenHash, userID, purpose, data, now.Unix(), now.Add(ttl).Unix())
	return err
}

// GetPortalChallenge returns a live challenge of the expected purpose. The
// purpose must match: a claim link must never be spendable as a password
// reset, or vice versa.
func (s *Store) GetPortalChallenge(tokenHash, purpose string) (PortalChallenge, error) {
	var c PortalChallenge
	err := s.db.QueryRow(`
		SELECT user_id, purpose, data, attempts FROM portal_challenges
		WHERE token_hash = ? AND purpose = ? AND expires_at > ?`,
		tokenHash, purpose, time.Now().Unix()).Scan(&c.UserID, &c.Purpose, &c.Data, &c.Attempts)
	return c, err
}

func (s *Store) DeletePortalChallenge(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM portal_challenges WHERE token_hash = ?`, tokenHash)
	return err
}

// BumpPortalChallengeAttempts counts a wrong answer and reports whether the
// budget is spent.
func (s *Store) BumpPortalChallengeAttempts(tokenHash string) (exhausted bool, err error) {
	var attempts int
	err = s.db.QueryRow(`
		UPDATE portal_challenges SET attempts = attempts + 1
		WHERE token_hash = ? RETURNING attempts`, tokenHash).Scan(&attempts)
	if err != nil {
		return false, err
	}
	return attempts >= MaxPortalAttempts, nil
}

func (s *Store) PrunePortalChallenges(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM portal_challenges WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --- the subscription token, recoverable ---

func subTokenAAD(userID string) string { return "sub-token:" + userID }

// SubToken returns a user's subscription token, or "" if it predates
// recoverable storage.
//
// Read only by the portal's own display path, and never part of userCols:
// putting it there would decrypt a secret on every ListUsers, and one row
// sealed under a rotated key would fail the entire query.
func (s *Store) SubToken(userID string) (string, error) {
	var sealed string
	if err := s.db.QueryRow(`SELECT sub_token_enc FROM users WHERE id = ?`, userID).Scan(&sealed); err != nil {
		return "", err
	}
	if sealed == "" {
		return "", nil
	}
	return s.box.Open(subTokenAAD(userID), sealed)
}

// --- user-scoped traffic ---

// UserNodeTrafficSeries returns one user's hourly traffic per node.
//
// A separate function rather than a call to TrafficSeries with a userID,
// because that one treats an empty userID as "the whole fleet". One missed nil
// check there would hand a customer every other subscriber's usage on their
// own nodes; here an empty userID is an error, so the dangerous case cannot be
// reached from the portal at all.
//
// It also groups by node in a single query, which is what keeps the portal's
// home page one round trip instead of 1+N.
func (s *Store) UserNodeTrafficSeries(userID string, from, to time.Time) (map[string][]TrafficPoint, error) {
	if userID == "" {
		return nil, errUserScopedSeriesNeedsUser
	}
	rows, err := s.db.Query(`
		SELECT node_id, bucket_start, SUM(up_bytes), SUM(down_bytes)
		FROM traffic_buckets
		WHERE user_id = ? AND bucket_start >= ? AND bucket_start < ?
		GROUP BY node_id, bucket_start
		ORDER BY node_id, bucket_start`,
		userID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]TrafficPoint{}
	for rows.Next() {
		var nodeID string
		var p TrafficPoint
		if err := rows.Scan(&nodeID, &p.At, &p.UpBytes, &p.DownBytes); err != nil {
			return nil, err
		}
		out[nodeID] = append(out[nodeID], p)
	}
	return out, rows.Err()
}

// NodeAnnouncedOnline reports the availability the alerting sweep last
// announced for a node.
//
// Reusing that state rather than inventing a fourth notion of "up": it is
// already debounced and already persisted, so the portal's green dot and the
// operator's Telegram message cannot tell different stories about the same
// minute. The cost is honest — availability here lags by up to the sweep
// interval plus the debounce.
func (s *Store) NodeAnnouncedOnline(nodeID string) (bool, error) {
	var announced bool
	err := s.db.QueryRow(`SELECT announced_online FROM node_alert_state WHERE node_id = ?`, nodeID).
		Scan(&announced)
	if err == sql.ErrNoRows {
		// Never swept, so nothing has been announced either way.
		return false, nil
	}
	return announced, err
}

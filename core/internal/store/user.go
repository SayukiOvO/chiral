package store

import (
	"database/sql"
	"fmt"
	"time"
)

// User is a subscriber. Quota and expiry are enforced by the Core; `enabled`
// is the operator's own switch, kept separate so "the operator turned this
// off" and "this ran out of quota" stay distinguishable.
type User struct {
	ID       string
	Name     string
	SubToken string // only populated when freshly issued; never read back
	// QuotaBytes 0 means unlimited; UsedBytes accumulates across every
	// credential the user holds.
	QuotaBytes int64
	UsedBytes  int64
	// ExpiresAt 0 means no expiry. RenewPeriod 0 disables auto-renewal.
	ExpiresAt   int64
	RenewPeriod int64
	Enabled     bool
	// Active records whether the credentials are currently installed on the
	// nodes, so we only push the difference rather than re-pushing blindly.
	Active bool
	// DeviceLimit is the expected number of concurrent source addresses, 0 for
	// none. Displayed, never enforced: no Xray API can end an established
	// session, so there is nothing for it to drive. See migration 0008.
	DeviceLimit int
	CreatedAt   int64
	UpdatedAt   int64
}

// Credential is one user's access to one profile on one node — the unit of
// strong isolation. Email is Xray's per-user stats key.
type Credential struct {
	ID        string
	UserID    string
	ProfileID string
	NodeID    string
	Email     string
	Secret    string
	UpBytes   int64
	DownBytes int64
	CreatedAt int64
}

func credentialAAD(id string) string { return "credential:" + id }

const userCols = `id, name, quota_bytes, used_bytes, expires_at, renew_period, enabled, active, device_limit, created_at, updated_at`

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Name, &u.QuotaBytes, &u.UsedBytes, &u.ExpiresAt,
		&u.RenewPeriod, &u.Enabled, &u.Active, &u.DeviceLimit, &u.CreatedAt, &u.UpdatedAt)
	return u, err
}

// CreateUser inserts a user together with their subscription token.
//
// The token is stored twice: hashed for the /sub/{token} lookup, and sealed so
// the portal can show the person their own link. Storing the sealed copy
// reverses what 0003_users.sql said, deliberately — see migration 0009.
//
// subToken may be empty in tests that do not care about subscriptions; then
// nothing is sealed and the hash is whatever the caller passed.
func (s *Store) CreateUser(u User, subTokenHash string) (User, error) {
	return s.createUser(u, subTokenHash, "")
}

// CreateUserWithToken is CreateUser plus the raw token, so it can be sealed.
func (s *Store) CreateUserWithToken(u User, subToken, subTokenHash string) (User, error) {
	return s.createUser(u, subTokenHash, subToken)
}

func (s *Store) createUser(u User, subTokenHash, subToken string) (User, error) {
	u.ID = NewID()
	now := time.Now().Unix()
	u.CreatedAt, u.UpdatedAt = now, now

	sealed := ""
	if subToken != "" {
		if !s.box.Enabled() {
			return User{}, errPlaintextSubToken
		}
		var err error
		if sealed, err = s.box.Seal(subTokenAAD(u.ID), subToken); err != nil {
			return User{}, err
		}
	}
	_, err := s.db.Exec(`
		INSERT INTO users (id, name, sub_token_hash, sub_token_enc, quota_bytes, used_bytes,
			expires_at, renew_period, enabled, active, device_limit, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, 0, ?, ?, ?)`,
		u.ID, u.Name, subTokenHash, sealed, u.QuotaBytes, u.ExpiresAt, u.RenewPeriod,
		u.Enabled, u.DeviceLimit, u.CreatedAt, u.UpdatedAt)
	return u, err
}

func (s *Store) GetUser(id string) (User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// FindUserBySubTokenHash resolves a subscription request.
func (s *Store) FindUserBySubTokenHash(hash string) (User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE sub_token_hash = ?`, hash))
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT ` + userCols + ` FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser writes the operator-editable fields.
func (s *Store) UpdateUser(u User) error {
	res, err := s.db.Exec(`
		UPDATE users SET name = ?, quota_bytes = ?, expires_at = ?, renew_period = ?,
			enabled = ?, device_limit = ?, updated_at = ? WHERE id = ?`,
		u.Name, u.QuotaBytes, u.ExpiresAt, u.RenewPeriod, u.Enabled, u.DeviceLimit,
		time.Now().Unix(), u.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetUserActive records whether the user's credentials are installed.
func (s *Store) SetUserActive(id string, active bool) error {
	_, err := s.db.Exec(`UPDATE users SET active = ?, updated_at = ? WHERE id = ?`,
		active, time.Now().Unix(), id)
	return err
}

// ResetSubToken replaces the subscription token hash, invalidating the old link.
// ResetSubToken replaces a user's subscription token, storing both the lookup
// hash and the sealed copy the portal displays.
func (s *Store) ResetSubToken(id, subToken, subTokenHash string) error {
	sealed := ""
	if subToken != "" {
		if !s.box.Enabled() {
			return errPlaintextSubToken
		}
		var err error
		if sealed, err = s.box.Seal(subTokenAAD(id), subToken); err != nil {
			return err
		}
	}
	res, err := s.db.Exec(
		`UPDATE users SET sub_token_hash = ?, sub_token_enc = ?, updated_at = ? WHERE id = ?`,
		subTokenHash, sealed, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteUser(id string) error {
	res, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RenewUser pushes expiry forward by one period and zeroes usage, so quota is
// per period rather than lifetime.
func (s *Store) RenewUser(id string, newExpiry int64) error {
	_, err := s.db.Exec(`UPDATE users SET expires_at = ?, used_bytes = 0, updated_at = ? WHERE id = ?`,
		newExpiry, time.Now().Unix(), id)
	return err
}

// --- user ↔ profile binding ---

func (s *Store) BindUserProfile(userID, profileID string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_profiles (user_id, profile_id) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		userID, profileID)
	return err
}

func (s *Store) UnbindUserProfile(userID, profileID string) error {
	_, err := s.db.Exec(`DELETE FROM user_profiles WHERE user_id = ? AND profile_id = ?`,
		userID, profileID)
	return err
}

func (s *Store) UserProfileIDs(userID string) ([]string, error) {
	return s.idList(`SELECT profile_id FROM user_profiles WHERE user_id = ? ORDER BY profile_id`, userID)
}

// ProfileUserIDs lists the users entitled to a profile.
func (s *Store) ProfileUserIDs(profileID string) ([]string, error) {
	return s.idList(`SELECT user_id FROM user_profiles WHERE profile_id = ? ORDER BY user_id`, profileID)
}

func (s *Store) idList(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- credentials ---

const credCols = `id, user_id, profile_id, node_id, email, secret, up_bytes, down_bytes, created_at`

func (s *Store) scanCredential(row interface{ Scan(...any) error }) (Credential, error) {
	var c Credential
	if err := row.Scan(&c.ID, &c.UserID, &c.ProfileID, &c.NodeID, &c.Email, &c.Secret,
		&c.UpBytes, &c.DownBytes, &c.CreatedAt); err != nil {
		return c, err
	}
	plain, err := s.box.Open(credentialAAD(c.ID), c.Secret)
	if err != nil {
		return c, fmt.Errorf("opening credential %s: %w", c.Email, err)
	}
	c.Secret = plain
	return c, nil
}

func (s *Store) scanCredentials(rows *sql.Rows) ([]Credential, error) {
	defer rows.Close()
	out := []Credential{}
	for rows.Next() {
		c, err := s.scanCredential(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// PutCredential inserts a credential if the user × profile × node triple has
// none yet, and returns the existing one otherwise. Minting is idempotent so
// assembly can call it freely without churning secrets — a regenerated secret
// would silently lock the user out until they refetched their subscription.
func (s *Store) PutCredential(c Credential) (Credential, error) {
	if existing, err := s.FindCredential(c.UserID, c.ProfileID, c.NodeID); err == nil {
		return existing, nil
	} else if !IsNotFound(err) {
		return Credential{}, err
	}
	c.ID = NewID()
	c.CreatedAt = time.Now().Unix()
	sealed, err := s.box.Seal(credentialAAD(c.ID), c.Secret)
	if err != nil {
		return Credential{}, err
	}
	if _, err := s.db.Exec(`
		INSERT INTO credentials (id, user_id, profile_id, node_id, email, secret, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.UserID, c.ProfileID, c.NodeID, c.Email, sealed, c.CreatedAt); err != nil {
		return Credential{}, err
	}
	return c, nil
}

func (s *Store) FindCredential(userID, profileID, nodeID string) (Credential, error) {
	return s.scanCredential(s.db.QueryRow(`SELECT `+credCols+
		` FROM credentials WHERE user_id = ? AND profile_id = ? AND node_id = ?`,
		userID, profileID, nodeID))
}

// NodeCredentials returns every credential installed on a node, which is what
// assembly needs to fill the inbounds' clients arrays.
func (s *Store) NodeCredentials(nodeID string) ([]Credential, error) {
	rows, err := s.db.Query(`SELECT `+credCols+` FROM credentials WHERE node_id = ? ORDER BY email`, nodeID)
	if err != nil {
		return nil, err
	}
	return s.scanCredentials(rows)
}

// NodeProfileCredentials narrows that to one profile's inbound.
func (s *Store) NodeProfileCredentials(nodeID, profileID string) ([]Credential, error) {
	rows, err := s.db.Query(`SELECT `+credCols+
		` FROM credentials WHERE node_id = ? AND profile_id = ? ORDER BY email`, nodeID, profileID)
	if err != nil {
		return nil, err
	}
	return s.scanCredentials(rows)
}

func (s *Store) UserCredentials(userID string) ([]Credential, error) {
	rows, err := s.db.Query(`SELECT `+credCols+` FROM credentials WHERE user_id = ? ORDER BY email`, userID)
	if err != nil {
		return nil, err
	}
	return s.scanCredentials(rows)
}

// DeleteCredentialsForBinding drops the credentials minted for a user's
// profile, used when the entitlement is revoked.
func (s *Store) DeleteCredentialsForBinding(userID, profileID string) error {
	_, err := s.db.Exec(`DELETE FROM credentials WHERE user_id = ? AND profile_id = ?`, userID, profileID)
	return err
}

// AddCredentialTraffic applies one agent-reported delta to a credential and
// to its user's total, in one transaction so the two cannot drift.
//
// Deltas are always non-negative: the agent reads Xray's counters with
// `statsquery -reset`, so a kernel restart yields a smaller delta rather than
// the negative jump a "remember the last value" scheme would produce.
func (s *Store) AddCredentialTraffic(email string, up, down int64) error {
	if up < 0 || down < 0 {
		return fmt.Errorf("traffic delta must not be negative (up=%d down=%d)", up, down)
	}
	if up == 0 && down == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var userID string
	if err := tx.QueryRow(`SELECT user_id FROM credentials WHERE email = ?`, email).Scan(&userID); err != nil {
		// An unknown email is not an error worth failing the whole report
		// over: it is normal right after a credential is revoked, while the
		// node still had in-flight traffic for it.
		if err == sql.ErrNoRows {
			return nil
		}
		return err
	}
	if _, err := tx.Exec(
		`UPDATE credentials SET up_bytes = up_bytes + ?, down_bytes = down_bytes + ? WHERE email = ?`,
		up, down, email); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE users SET used_bytes = used_bytes + ? WHERE id = ?`, up+down, userID); err != nil {
		return err
	}
	return tx.Commit()
}

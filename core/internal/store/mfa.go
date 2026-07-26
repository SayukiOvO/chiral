package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Second-factor lifetimes. All short: these exist only to bridge two steps of
// one interaction, and a long-lived one is a bypass waiting to be found.
const (
	// LoginChallengeTTL bounds the window between a correct password and a
	// second factor.
	LoginChallengeTTL = 5 * time.Minute
	// WebAuthnChallengeTTL bounds one ceremony.
	WebAuthnChallengeTTL = 5 * time.Minute
	// EmailCodeTTL is longer, because mail takes time to arrive.
	EmailCodeTTL = 15 * time.Minute
	// MaxChallengeAttempts caps guesses against one emailed code before it is
	// burned; six digits would otherwise be brute-forceable within its life.
	MaxChallengeAttempts = 5
)

// MFA credential kinds.
const (
	MFATOTP    = "totp"
	MFAPasskey = "passkey"
	MFAEmail   = "email"
)

// Challenge purposes.
const (
	PurposeLogin            = "login"
	PurposeWebAuthnRegister = "webauthn_register"
	PurposeWebAuthnLogin    = "webauthn_login"
	PurposeEmailVerify      = "email_verify"
	PurposeEmailCode        = "email_code"
)

// MFACredential is one enrolled second factor.
type MFACredential struct {
	ID           string
	AdminID      string
	Kind         string
	Name         string
	Secret       string
	CredentialID string
	CreatedAt    int64
	LastUsedAt   int64
	Confirmed    bool
}

func mfaAAD(id string) string { return "mfa-credential:" + id }

const mfaCols = `id, admin_id, kind, name, secret, credential_id, created_at, last_used_at, confirmed`

func (s *Store) scanMFA(row interface{ Scan(...any) error }) (MFACredential, error) {
	var c MFACredential
	if err := row.Scan(&c.ID, &c.AdminID, &c.Kind, &c.Name, &c.Secret,
		&c.CredentialID, &c.CreatedAt, &c.LastUsedAt, &c.Confirmed); err != nil {
		return c, err
	}
	if c.Secret != "" {
		plain, err := s.box.Open(mfaAAD(c.ID), c.Secret)
		if err != nil {
			return c, fmt.Errorf("opening mfa credential %s: %w", c.Name, err)
		}
		c.Secret = plain
	}
	return c, nil
}

// PutMFACredential stores a factor. A TOTP shared secret is as good as the
// password it guards, so it is encrypted at rest like the rest of the key
// material here.
func (s *Store) PutMFACredential(c MFACredential) (MFACredential, error) {
	if c.ID == "" {
		c.ID = NewID()
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = time.Now().Unix()
	}
	sealed := ""
	if c.Secret != "" {
		var err error
		if sealed, err = s.box.Seal(mfaAAD(c.ID), c.Secret); err != nil {
			return MFACredential{}, err
		}
	}
	_, err := s.db.Exec(`
		INSERT INTO mfa_credentials
			(id, admin_id, kind, name, secret, credential_id, created_at, last_used_at, confirmed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			name = excluded.name, secret = excluded.secret,
			credential_id = excluded.credential_id, confirmed = excluded.confirmed`,
		c.ID, c.AdminID, c.Kind, c.Name, sealed, c.CredentialID,
		c.CreatedAt, c.LastUsedAt, c.Confirmed)
	return c, err
}

// MFACredentials lists an admin's factors. Unconfirmed ones are included so
// the UI can show a half-finished enrolment; callers deciding whether MFA is
// required must filter on Confirmed.
func (s *Store) MFACredentials(adminID string) ([]MFACredential, error) {
	rows, err := s.db.Query(`SELECT `+mfaCols+
		` FROM mfa_credentials WHERE admin_id = ? ORDER BY created_at`, adminID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MFACredential{}
	for rows.Next() {
		c, err := s.scanMFA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConfirmedMFACredentials returns only factors that have been proven, which is
// what "does this account have MFA" must be decided on.
func (s *Store) ConfirmedMFACredentials(adminID string) ([]MFACredential, error) {
	all, err := s.MFACredentials(adminID)
	if err != nil {
		return nil, err
	}
	out := make([]MFACredential, 0, len(all))
	for _, c := range all {
		if c.Confirmed {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Store) GetMFACredential(id string) (MFACredential, error) {
	return s.scanMFA(s.db.QueryRow(`SELECT `+mfaCols+` FROM mfa_credentials WHERE id = ?`, id))
}

// FindPasskey resolves an assertion's credential id to its enrolment.
func (s *Store) FindPasskey(credentialID string) (MFACredential, error) {
	return s.scanMFA(s.db.QueryRow(`SELECT `+mfaCols+
		` FROM mfa_credentials WHERE credential_id = ? AND kind = ?`, credentialID, MFAPasskey))
}

func (s *Store) DeleteMFACredential(adminID, id string) error {
	res, err := s.db.Exec(`DELETE FROM mfa_credentials WHERE id = ? AND admin_id = ?`, id, adminID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) TouchMFACredential(id string) error {
	_, err := s.db.Exec(`UPDATE mfa_credentials SET last_used_at = ? WHERE id = ?`,
		time.Now().Unix(), id)
	return err
}

// --- recovery codes ---

// ReplaceRecoveryCodes swaps in a fresh set, discarding whatever was there.
// Only hashes are stored; the plaintext is shown once by the caller.
func (s *Store) ReplaceRecoveryCodes(adminID string, hashes []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM mfa_recovery_codes WHERE admin_id = ?`, adminID); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, h := range hashes {
		if _, err := tx.Exec(
			`INSERT INTO mfa_recovery_codes (admin_id, code_hash, created_at) VALUES (?, ?, ?)`,
			adminID, h, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UseRecoveryCode consumes a code if it exists and is unused. The update is
// the check: two concurrent attempts with the same code cannot both succeed.
func (s *Store) UseRecoveryCode(adminID, codeHash string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE mfa_recovery_codes SET used_at = ? WHERE admin_id = ? AND code_hash = ? AND used_at = 0`,
		time.Now().Unix(), adminID, codeHash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountUnusedRecoveryCodes tells an admin how many they have left.
func (s *Store) CountUnusedRecoveryCodes(adminID string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM mfa_recovery_codes WHERE admin_id = ? AND used_at = 0`, adminID).Scan(&n)
	return n, err
}

// --- challenges ---

type Challenge struct {
	AdminID   string
	Purpose   string
	Data      string
	Attempts  int
	CreatedAt int64
	ExpiresAt int64
}

func (s *Store) CreateChallenge(tokenHash, adminID, purpose, data string, ttl time.Duration) error {
	now := time.Now()
	// Upsert rather than a bare insert. Several challenges are keyed off
	// something stable — the email code hangs off the login challenge, the
	// verification code off the admin id — so "send me another one" arrives on
	// the same primary key. A plain INSERT turns that ordinary request into a
	// constraint violation and a 500.
	//
	// Replacing resets attempts: the caller asked for a fresh code, and the
	// old code's failed guesses are not this code's budget.
	_, err := s.db.Exec(`
		INSERT INTO auth_challenges (token_hash, admin_id, purpose, data, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (token_hash) DO UPDATE SET
			admin_id   = excluded.admin_id,
			purpose    = excluded.purpose,
			data       = excluded.data,
			attempts   = 0,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at`,
		tokenHash, adminID, purpose, data, now.Unix(), now.Add(ttl).Unix())
	return err
}

// GetChallenge returns a live challenge of the expected purpose. A challenge
// issued for one purpose must never satisfy another — that is how a
// registration ceremony would otherwise be replayed as a login.
func (s *Store) GetChallenge(tokenHash, purpose string) (Challenge, error) {
	var c Challenge
	err := s.db.QueryRow(`
		SELECT admin_id, purpose, data, attempts, created_at, expires_at
		FROM auth_challenges
		WHERE token_hash = ? AND purpose = ? AND expires_at > ?`,
		tokenHash, purpose, time.Now().Unix()).
		Scan(&c.AdminID, &c.Purpose, &c.Data, &c.Attempts, &c.CreatedAt, &c.ExpiresAt)
	return c, err
}

// BumpChallengeAttempts records a failed guess and reports whether the
// challenge has now been exhausted.
func (s *Store) BumpChallengeAttempts(tokenHash string) (exhausted bool, err error) {
	if _, err := s.db.Exec(
		`UPDATE auth_challenges SET attempts = attempts + 1 WHERE token_hash = ?`, tokenHash); err != nil {
		return false, err
	}
	var attempts int
	if err := s.db.QueryRow(
		`SELECT attempts FROM auth_challenges WHERE token_hash = ?`, tokenHash).Scan(&attempts); err != nil {
		return false, err
	}
	return attempts >= MaxChallengeAttempts, nil
}

func (s *Store) DeleteChallenge(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM auth_challenges WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) PruneChallenges(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM auth_challenges WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// --- email ---

func (s *Store) SetAdminEmail(adminID, email string, verified bool) error {
	res, err := s.db.Exec(
		`UPDATE admins SET email = ?, email_verified = ?, updated_at = ? WHERE id = ?`,
		email, verified, time.Now().Unix(), adminID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AdminEmail(adminID string) (email string, verified bool, err error) {
	err = s.db.QueryRow(`SELECT email, email_verified FROM admins WHERE id = ?`, adminID).
		Scan(&email, &verified)
	return email, verified, err
}

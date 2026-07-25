package store

import (
	"database/sql"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/auth"
)

// SessionTTL is how long a login lasts. Short enough that a stolen session
// token expires on its own, long enough not to be a nuisance.
const SessionTTL = 7 * 24 * time.Hour

type Admin struct {
	ID           string
	Username     string
	PasswordHash string
	Role         auth.Role
	Disabled     bool
	CreatedAt    int64
	UpdatedAt    int64
	LastLogin    int64
	// Email is only usable as a second factor once verified: sending codes to
	// an unproven address would let a typo lock someone out.
	Email         string
	EmailVerified bool
}

const adminCols = `id, username, password_hash, role, disabled, created_at, updated_at, last_login, email, email_verified`

func scanAdmin(row interface{ Scan(...any) error }) (Admin, error) {
	var a Admin
	err := row.Scan(&a.ID, &a.Username, &a.PasswordHash, &a.Role, &a.Disabled,
		&a.CreatedAt, &a.UpdatedAt, &a.LastLogin, &a.Email, &a.EmailVerified)
	return a, err
}

func (s *Store) CreateAdmin(username, passwordHash string, role auth.Role) (Admin, error) {
	now := time.Now().Unix()
	a := Admin{
		ID: NewID(), Username: username, PasswordHash: passwordHash,
		Role: role, CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.db.Exec(`
		INSERT INTO admins (id, username, password_hash, role, disabled, created_at, updated_at, last_login)
		VALUES (?, ?, ?, ?, 0, ?, ?, 0)`,
		a.ID, a.Username, a.PasswordHash, a.Role, a.CreatedAt, a.UpdatedAt)
	return a, err
}

func (s *Store) GetAdmin(id string) (Admin, error) {
	return scanAdmin(s.db.QueryRow(`SELECT `+adminCols+` FROM admins WHERE id = ?`, id))
}

func (s *Store) FindAdminByUsername(username string) (Admin, error) {
	return scanAdmin(s.db.QueryRow(`SELECT `+adminCols+` FROM admins WHERE username = ?`, username))
}

func (s *Store) ListAdmins() ([]Admin, error) {
	rows, err := s.db.Query(`SELECT ` + adminCols + ` FROM admins ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Admin{}
	for rows.Next() {
		a, err := scanAdmin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CountAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&n)
	return n, err
}

// CountEnabledSuperadmins guards against removing the last way in.
func (s *Store) CountEnabledSuperadmins() (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM admins WHERE role = ? AND disabled = 0`, auth.RoleSuperadmin).Scan(&n)
	return n, err
}

func (s *Store) UpdateAdminRole(id string, role auth.Role, disabled bool) error {
	res, err := s.db.Exec(`UPDATE admins SET role = ?, disabled = ?, updated_at = ? WHERE id = ?`,
		role, disabled, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetAdminPassword changes the password and invalidates every existing
// session for that admin: a password change is how someone reacts to a
// suspected compromise, so it has to cut off whoever might be logged in.
func (s *Store) SetAdminPassword(id, passwordHash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE admins SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.Exec(`DELETE FROM admin_sessions WHERE admin_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteAdmin(id string) error {
	res, err := s.db.Exec(`DELETE FROM admins WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) TouchAdminLogin(id string) error {
	_, err := s.db.Exec(`UPDATE admins SET last_login = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

// --- sessions ---

// CreateSession stores only the token's hash, like every other credential in
// the panel.
func (s *Store) CreateSession(tokenHash, adminID string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.Exec(`
		INSERT INTO admin_sessions (token_hash, admin_id, created_at, expires_at)
		VALUES (?, ?, ?, ?)`,
		tokenHash, adminID, now.Unix(), now.Add(ttl).Unix())
	return err
}

// AdminBySession resolves a live session to its admin. An expired or unknown
// session is IsNotFound, and so is a disabled account — a disabled admin's
// existing session must stop working immediately, not at expiry.
func (s *Store) AdminBySession(tokenHash string) (Admin, error) {
	return scanAdmin(s.db.QueryRow(`
		SELECT `+prefixed(adminCols, "a")+`
		FROM admin_sessions s JOIN admins a ON a.id = s.admin_id
		WHERE s.token_hash = ? AND s.expires_at > ? AND a.disabled = 0`,
		tokenHash, time.Now().Unix()))
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

// PruneSessions drops expired rows; called from the periodic sweep.
func (s *Store) PruneSessions(now time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM admin_sessions WHERE expires_at <= ?`, now.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// prefixed qualifies a column list with a table alias, so a join can reuse it.
func prefixed(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

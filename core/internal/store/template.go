package store

import (
	"database/sql"
	"fmt"
	"time"
)

// Scope of a variable. User scope arrives with M3.
const (
	ScopeGlobal  = "global"
	ScopeProfile = "profile"
	ScopeNode    = "node"
)

// Component is one member of a variable group: "" for a single-valued
// variable, otherwise a name such as "private" or "public".
type Component struct {
	Name string
	// Value is plaintext — the store seals and opens secret components on the
	// way in and out, so callers never handle ciphertext.
	Value  string
	Secret bool
}

// Variable is one entry in the pool.
type Variable struct {
	ID         string
	Name       string
	Scope      string
	ProfileID  sql.NullString
	NodeID     sql.NullString
	Generator  string
	Components []Component
	CreatedAt  int64
	UpdatedAt  int64
}

// aad binds a ciphertext to the exact row that holds it, so a component
// cannot be relocated to another variable by someone with write access.
func aad(variableID, component string) string { return variableID + ":" + component }

// PutVariable inserts or replaces a variable and its components atomically.
func (s *Store) PutVariable(v Variable) (Variable, error) {
	if v.ID == "" {
		v.ID = NewID()
	}
	now := time.Now().Unix()
	if v.CreatedAt == 0 {
		v.CreatedAt = now
	}
	v.UpdatedAt = now

	tx, err := s.db.Begin()
	if err != nil {
		return Variable{}, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO variables (id, name, scope, profile_id, node_id, generator, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name, generator = excluded.generator, updated_at = excluded.updated_at`,
		v.ID, v.Name, v.Scope, v.ProfileID, v.NodeID, v.Generator, v.CreatedAt, v.UpdatedAt); err != nil {
		return Variable{}, err
	}
	if _, err := tx.Exec(`DELETE FROM variable_components WHERE variable_id = ?`, v.ID); err != nil {
		return Variable{}, err
	}
	for _, c := range v.Components {
		stored := c.Value
		if c.Secret {
			sealed, err := s.box.Seal(aad(v.ID, c.Name), c.Value)
			if err != nil {
				return Variable{}, fmt.Errorf("sealing %s.%s: %w", v.Name, c.Name, err)
			}
			stored = sealed
		}
		if _, err := tx.Exec(`
			INSERT INTO variable_components (variable_id, component, value, secret) VALUES (?, ?, ?, ?)`,
			v.ID, c.Name, stored, boolToInt(c.Secret)); err != nil {
			return Variable{}, err
		}
	}
	return v, tx.Commit()
}

func (s *Store) loadComponents(variableID string) ([]Component, error) {
	rows, err := s.db.Query(
		`SELECT component, value, secret FROM variable_components WHERE variable_id = ? ORDER BY component`, variableID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Component
	for rows.Next() {
		var c Component
		var isSecret int
		if err := rows.Scan(&c.Name, &c.Value, &isSecret); err != nil {
			return nil, err
		}
		c.Secret = isSecret != 0
		if c.Secret {
			plain, err := s.box.Open(aad(variableID, c.Name), c.Value)
			if err != nil {
				return nil, fmt.Errorf("opening component %q: %w", c.Name, err)
			}
			c.Value = plain
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const variableCols = `id, name, scope, profile_id, node_id, generator, created_at, updated_at`

func (s *Store) scanVariables(rows *sql.Rows) ([]Variable, error) {
	defer rows.Close()
	var out []Variable
	for rows.Next() {
		var v Variable
		if err := rows.Scan(&v.ID, &v.Name, &v.Scope, &v.ProfileID, &v.NodeID, &v.Generator, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		cs, err := s.loadComponents(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Components = cs
	}
	return out, nil
}

// GlobalVariables returns the global scope.
func (s *Store) GlobalVariables() ([]Variable, error) {
	rows, err := s.db.Query(`SELECT ` + variableCols + ` FROM variables WHERE scope = 'global' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	return s.scanVariables(rows)
}

// ProfileVariables returns the variables owned by a profile.
func (s *Store) ProfileVariables(profileID string) ([]Variable, error) {
	rows, err := s.db.Query(`SELECT `+variableCols+` FROM variables WHERE scope = 'profile' AND profile_id = ? ORDER BY name`, profileID)
	if err != nil {
		return nil, err
	}
	return s.scanVariables(rows)
}

// NodeVariables returns the variables owned by a node.
func (s *Store) NodeVariables(nodeID string) ([]Variable, error) {
	rows, err := s.db.Query(`SELECT `+variableCols+` FROM variables WHERE scope = 'node' AND node_id = ? ORDER BY name`, nodeID)
	if err != nil {
		return nil, err
	}
	return s.scanVariables(rows)
}

// GetVariable loads one variable by id.
func (s *Store) GetVariable(id string) (Variable, error) {
	rows, err := s.db.Query(`SELECT `+variableCols+` FROM variables WHERE id = ?`, id)
	if err != nil {
		return Variable{}, err
	}
	vs, err := s.scanVariables(rows)
	if err != nil {
		return Variable{}, err
	}
	if len(vs) == 0 {
		return Variable{}, sql.ErrNoRows
	}
	return vs[0], nil
}

func (s *Store) DeleteVariable(id string) error {
	res, err := s.db.Exec(`DELETE FROM variables WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Profile is a bundle of server + client templates. Its variables live in the
// pool under profile scope.
type Profile struct {
	ID              string
	Name            string
	InboundTemplate string
	ClientEntry     string
	CreatedAt       int64
	UpdatedAt       int64
}

func (s *Store) CreateProfile(name string) (Profile, error) {
	p := Profile{ID: NewID(), Name: name, CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix()}
	_, err := s.db.Exec(`INSERT INTO profiles (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		p.ID, p.Name, p.CreatedAt, p.UpdatedAt)
	return p, err
}

const profileCols = `id, name, inbound_template, client_entry, created_at, updated_at`

func scanProfile(row interface{ Scan(...any) error }) (Profile, error) {
	var p Profile
	err := row.Scan(&p.ID, &p.Name, &p.InboundTemplate, &p.ClientEntry, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *Store) GetProfile(id string) (Profile, error) {
	return scanProfile(s.db.QueryRow(`SELECT `+profileCols+` FROM profiles WHERE id = ?`, id))
}

func (s *Store) ListProfiles() ([]Profile, error) {
	rows, err := s.db.Query(`SELECT ` + profileCols + ` FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateProfile replaces the editable fields of a profile.
func (s *Store) UpdateProfile(p Profile) error {
	res, err := s.db.Exec(`
		UPDATE profiles SET name = ?, inbound_template = ?, client_entry = ?, updated_at = ? WHERE id = ?`,
		p.Name, p.InboundTemplate, p.ClientEntry, time.Now().Unix(), p.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteProfile(id string) error {
	res, err := s.db.Exec(`DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// PutClientTemplate stores (or replaces) one client-type template.
func (s *Store) PutClientTemplate(profileID, client, tmpl string) error {
	_, err := s.db.Exec(`
		INSERT INTO profile_client_templates (profile_id, client, template) VALUES (?, ?, ?)
		ON CONFLICT(profile_id, client) DO UPDATE SET template = excluded.template`,
		profileID, client, tmpl)
	return err
}

// ClientTemplates returns every client template of a profile, keyed by client.
func (s *Store) ClientTemplates(profileID string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT client, template FROM profile_client_templates WHERE profile_id = ?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var c, t string
		if err := rows.Scan(&c, &t); err != nil {
			return nil, err
		}
		out[c] = t
	}
	return out, rows.Err()
}

func (s *Store) DeleteClientTemplate(profileID, client string) error {
	_, err := s.db.Exec(`DELETE FROM profile_client_templates WHERE profile_id = ? AND client = ?`, profileID, client)
	return err
}

// BindProfileNode binds a profile to a node; binding twice is a no-op.
func (s *Store) BindProfileNode(profileID, nodeID string) error {
	_, err := s.db.Exec(
		`INSERT INTO profile_nodes (profile_id, node_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, profileID, nodeID)
	return err
}

func (s *Store) UnbindProfileNode(profileID, nodeID string) error {
	_, err := s.db.Exec(`DELETE FROM profile_nodes WHERE profile_id = ? AND node_id = ?`, profileID, nodeID)
	return err
}

// ProfileNodeIDs lists the nodes bound to a profile.
func (s *Store) ProfileNodeIDs(profileID string) ([]string, error) {
	return s.idColumn(`SELECT node_id FROM profile_nodes WHERE profile_id = ? ORDER BY node_id`, profileID)
}

// NodeProfileIDs lists the profiles bound to a node — the set the node's
// config is assembled from.
func (s *Store) NodeProfileIDs(nodeID string) ([]string, error) {
	return s.idColumn(`SELECT profile_id FROM profile_nodes WHERE node_id = ? ORDER BY profile_id`, nodeID)
}

func (s *Store) idColumn(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

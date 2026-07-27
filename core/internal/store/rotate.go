package store

import (
	"database/sql"
	"fmt"

	"github.com/SayukiOvO/chiral/core/internal/secret"
)

// Re-sealing everything under a new CHIRAL_SECRET_KEY.
//
// Without this, the key can never be changed: every sealed value carries the
// old key's fingerprint, and Open refuses anything it did not seal. A leaked
// key would be unrecoverable-from, which is not a property to discover during
// an incident.
//
// Everything happens in ONE transaction. A half-rotated database is worse than
// either end state — some rows would open under the old key and some under the
// new, and no single value of CHIRAL_SECRET_KEY would start the panel.

// sealedColumn is one place ciphertext lives, and how to rebuild its AAD.
//
// The AAD binds a ciphertext to its row, so re-sealing has to reproduce it
// exactly. Listing the columns here rather than deriving them means adding a
// new sealed column is a deliberate edit to this table — and the test below
// fails if a column is sealed anywhere the rotation does not know about.
type sealedColumn struct {
	table   string
	column  string
	keyCols []string
	// where narrows the rows to the ones that actually hold ciphertext.
	// variable_components is the reason it exists: only secret components are
	// sealed, and re-sealing a public one would encrypt a value that the read
	// path never decrypts — turning it into ciphertext in the UI.
	where string
	// aad rebuilds the additional authenticated data from the key columns.
	aad func(keys []any) string
}

func sealedColumns() []sealedColumn {
	return []sealedColumn{
		{
			table: "variable_components", column: "value", keyCols: []string{"variable_id", "component"},
			where: "secret = 1",
			aad:   func(k []any) string { return aad(asString(k[0]), asString(k[1])) },
		},
		{
			table: "node_configs", column: "config", keyCols: []string{"node_id", "version"},
			aad: func(k []any) string { return configAAD(asString(k[0]), asInt64(k[1])) },
		},
		{
			table: "nodes", column: "config_skeleton", keyCols: []string{"id"},
			aad: func(k []any) string { return skeletonAAD(asString(k[0])) },
		},
		{
			table: "credentials", column: "secret", keyCols: []string{"id"},
			aad: func(k []any) string { return credentialAAD(asString(k[0])) },
		},
		{
			table: "alert_targets", column: "config", keyCols: []string{"id"},
			aad: func(k []any) string { return alertAAD(asString(k[0])) },
		},
		{
			table: "mfa_credentials", column: "secret", keyCols: []string{"id"},
			aad: func(k []any) string { return mfaAAD(asString(k[0])) },
		},
		{
			table: "user_devices", column: "ip_enc", keyCols: []string{"user_id", "ip_hash"},
			aad: func(k []any) string { return deviceAAD(asString(k[0]), asString(k[1])) },
		},
		{
			table: "users", column: "sub_token_enc", keyCols: []string{"id"},
			aad: func(k []any) string { return subTokenAAD(asString(k[0])) },
		},
	}
}

// RotateSecretKey re-seals every encrypted value from this store's key to next.
//
// Reports how many values moved. A value already sealed under the new key is
// left alone, so running it twice is a no-op. Values written in the clear (by
// a panel that ran with no key at all) are sealed rather than skipped — the
// point of having a key is that nothing stays plaintext.
func (s *Store) RotateSecretKey(next *secret.Box) (int, error) {
	if !next.Enabled() {
		return 0, fmt.Errorf("refusing to rotate to an empty key: that would store every secret in the clear")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	moved := 0
	for _, col := range sealedColumns() {
		n, err := rotateColumn(tx, s.box, next, col)
		if err != nil {
			return 0, fmt.Errorf("%s.%s: %w", col.table, col.column, err)
		}
		moved += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return moved, nil
}

func rotateColumn(tx *sql.Tx, old, next *secret.Box, col sealedColumn) (int, error) {
	sel := "SELECT " + col.column
	for _, k := range col.keyCols {
		sel += ", " + k
	}
	sel += " FROM " + col.table + " WHERE " + col.column + " != ''"
	if col.where != "" {
		sel += " AND " + col.where
	}

	rows, err := tx.Query(sel)
	if err != nil {
		return 0, err
	}

	type pending struct {
		value string
		keys  []any
	}
	var todo []pending
	for rows.Next() {
		scanned := make([]any, 1+len(col.keyCols))
		holders := make([]any, len(scanned))
		for i := range scanned {
			holders[i] = &scanned[i]
		}
		if err := rows.Scan(holders...); err != nil {
			rows.Close()
			return 0, err
		}
		todo = append(todo, pending{value: asString(scanned[0]), keys: scanned[1:]})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	moved := 0
	for _, p := range todo {
		aad := col.aad(p.keys)
		// Already ours: leave it alone, so a rerun is a no-op rather than a
		// pointless re-encryption.
		if next.Owns(p.value) {
			continue
		}
		plain, err := old.Open(aad, p.value)
		if err != nil {
			return 0, fmt.Errorf("opening a value: %w", err)
		}
		resealed, err := next.Seal(aad, plain)
		if err != nil {
			return 0, err
		}
		set := "UPDATE " + col.table + " SET " + col.column + " = ? WHERE "
		args := []any{resealed}
		for i, k := range col.keyCols {
			if i > 0 {
				set += " AND "
			}
			set += k + " = ?"
			args = append(args, p.keys[i])
		}
		if _, err := tx.Exec(set, args...); err != nil {
			return 0, err
		}
		moved++
	}
	return moved, nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func asInt64(v any) int64 {
	if n, ok := v.(int64); ok {
		return n
	}
	return 0
}

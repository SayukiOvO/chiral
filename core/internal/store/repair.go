package store

import (
	"encoding/base64"
	"fmt"
)

// One-time repairs for data written by a version of Chiral that got something
// wrong. Run at startup, idempotent, and silent when there is nothing to do.

var b64url = base64.RawURLEncoding

// RepairX25519Clamping puts every stored x25519 private key into the canonical
// form of RFC 7748 §5, and reports how many it had to change.
//
// Keys generated before this fix were the raw output of the CSPRNG. Go's ecdh
// clamps internally when deriving the public half, so the PUBLIC key we
// published has always been correct — it belongs to the clamped scalar. Only
// the private half was stored in a form Xray's REALITY server does not use the
// way we assumed, and the result was a server that rejected every client and
// silently proxied them to the genuine target site. No error anywhere: not in
// `xray -test`, not in the panel, not in the node's log.
//
// That same asymmetry is what makes this repair safe to run unattended:
// clamping changes only bits the public key never depended on, so every
// subscription already in a customer's client keeps working, byte for byte.
// A fix that regenerated the keypair instead would silently break every one of
// them.
func (s *Store) RepairX25519Clamping() (int, error) {
	rows, err := s.db.Query(`
		SELECT c.variable_id, c.value, c.secret
		FROM variable_components c
		JOIN variables v ON v.id = c.variable_id
		WHERE v.generator = 'x25519' AND c.component = 'private'`)
	if err != nil {
		return 0, err
	}
	type item struct {
		id     string
		stored string
		secret int
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.stored, &it.secret); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	fixed := 0
	for _, it := range items {
		plain := it.stored
		if it.secret == 1 {
			p, err := s.box.Open(aad(it.id, "private"), it.stored)
			if err != nil {
				// A key we cannot read is a key we must not rewrite. Skip it;
				// the caller logs, and the operator's real problem is the
				// missing CHIRAL_SECRET_KEY, not the clamping.
				continue
			}
			plain = p
		}
		raw, err := b64url.DecodeString(plain)
		if err != nil || len(raw) != 32 {
			continue
		}
		clamped := make([]byte, 32)
		copy(clamped, raw)
		clamped[0] &= 248
		clamped[31] &= 127
		clamped[31] |= 64
		if string(clamped) == string(raw) {
			continue // already canonical
		}

		out := b64url.EncodeToString(clamped)
		if it.secret == 1 {
			sealed, err := s.box.Seal(aad(it.id, "private"), out)
			if err != nil {
				return fixed, fmt.Errorf("resealing the repaired key for %s: %w", it.id, err)
			}
			out = sealed
		}
		if _, err := s.db.Exec(
			`UPDATE variable_components SET value = ? WHERE variable_id = ? AND component = 'private'`,
			out, it.id); err != nil {
			return fixed, err
		}
		fixed++
	}
	return fixed, nil
}

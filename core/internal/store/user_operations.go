package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrUserOperationUncertain means an earlier operation might have executed.
// It is an execution barrier, never an authorization grant or retry hint.
var ErrUserOperationUncertain = errors.New("previous user operation has an uncertain result; reconciliation is required")

// UserOperationPending checks the durable downgrade barrier even when a new
// stream no longer advertises execution receipts. Losing the capability must
// never turn an unknown API write into a legacy blind replay.
func (s *Store) UserOperationPending(nodeID, tag, email string) (bool, error) {
	var pending bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM user_operation_receipts WHERE node_id=? AND inbound_tag=? AND email=? AND result=0)`, nodeID, tag, email).Scan(&pending)
	return pending, err
}

// BeginUserOperation journals the exact intent before it can enter a stream.
// Pending rows survive restarts and are never replaced by another operation.
// A historical APPLIED receipt never proves present runtime state; every new
// caller must dispatch a fresh ID and obtain another execution receipt.
func (s *Store) BeginUserOperation(nodeID, tag, email, id string, kind int32, digest string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result int
	err = tx.QueryRow(`SELECT result FROM user_operation_receipts WHERE node_id=? AND inbound_tag=? AND email=?`, nodeID, tag, email).Scan(&result)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if result == 0 {
			return ErrUserOperationUncertain
		}
	}
	now := time.Now().Unix()
	_, err = tx.Exec(`INSERT INTO user_operation_receipts (node_id,inbound_tag,email,operation_id,kind,request_digest,result,created_at,updated_at)
		VALUES (?,?,?,?,?,?,0,?,?) ON CONFLICT(node_id,inbound_tag,email) DO UPDATE SET
		operation_id=excluded.operation_id,kind=excluded.kind,request_digest=excluded.request_digest,result=0,created_at=excluded.created_at,updated_at=excluded.updated_at`, nodeID, tag, email, id, kind, digest, now, now)
	if err != nil {
		return fmt.Errorf("journal user operation: %w", err)
	}
	return tx.Commit()
}

// FinishUserOperation accepts only the currently journaled exact ID. UNKNOWN
// is kept as pending; an arbitrary ConfigAck must not remove this barrier.
// result=1 is applied; result=2 proves no runtime mutation was attempted.
func (s *Store) FinishUserOperation(nodeID, id string, result int32) error {
	if result != 1 && result != 2 {
		return fmt.Errorf("invalid terminal user operation result")
	}
	r, err := s.db.Exec(`UPDATE user_operation_receipts SET result=?, updated_at=? WHERE node_id=? AND operation_id=? AND result=0`, result, time.Now().Unix(), nodeID, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("user operation receipt does not match a pending intent")
	}
	return nil
}

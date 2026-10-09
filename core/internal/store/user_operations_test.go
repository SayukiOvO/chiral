package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPendingUserOperationSurvivesDatabaseReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipts.db")
	s, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateNode("receipt-node", "token-hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BeginUserOperation(n.ID, "in", "email", "first-id", 1, "digest"); err != nil {
		t.Fatalf("initial begin err=%v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.BeginUserOperation(n.ID, "in", "email", "replacement-id", 2, "different-digest"); !errors.Is(err, ErrUserOperationUncertain) {
		t.Fatalf("restart replaced unknown intent: %v", err)
	}
	if err := s.FinishUserOperation(n.ID, "wrong-id", 1); err == nil {
		t.Fatal("wrong operation ID completed intent")
	}
	if err := s.FinishUserOperation(n.ID, "first-id", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.BeginUserOperation(n.ID, "in", "email", "duplicate-id", 1, "digest"); err != nil {
		t.Fatalf("historical receipt blocked fresh confirmation: %v", err)
	}
	if err := s.FinishUserOperation(n.ID, "duplicate-id", 1); err != nil {
		t.Fatal("historical success was reused without a fresh pending intent", err)
	}
	if err := s.BeginUserOperation(n.ID, "in", "email", "remove-id", 2, "remove-digest"); err != nil {
		t.Fatalf("changed intent err=%v", err)
	}
	// The table has no account field or secret; its only request representation
	// is the digest supplied by the manager before dispatch.
	var digest string
	if err := s.db.QueryRow(`SELECT request_digest FROM user_operation_receipts WHERE node_id=?`, n.ID).Scan(&digest); err != nil || digest != "remove-digest" {
		t.Fatalf("persisted digest=%q err=%v", digest, err)
	}
}

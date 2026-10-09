package node

import (
	"context"
	"errors"
	"testing"
	"time"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

func receiptSession(f *statsServiceFixture) *Session {
	s := &Session{nodeID: f.nodeID, hello: &chiralv1.Hello{UserOpReceipts: true}, send: make(chan *chiralv1.CoreFrame, sendQueueSize), done: make(chan struct{}), lastSeen: time.Now()}
	f.svc.mgr.attach(f.nodeID, s)
	return s
}

func receiptOp(email string) *chiralv1.UserOp {
	return &chiralv1.UserOp{Kind: chiralv1.UserOpKind_USER_OP_KIND_ADD, InboundTag: "in", Email: email, AccountJson: []byte(`{"id":"private-account-not-stored"}`)}
}

func nextUserOperation(t *testing.T, s *Session) *chiralv1.CoreFrame {
	t.Helper()
	select {
	case frame := <-s.send:
		if frame.GetUserOp() == nil {
			t.Fatal("expected UserOp")
		}
		return frame
	case <-time.After(2 * time.Second):
		t.Fatal("operation was not queued")
		return nil
	}
}

func executionResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("receipt did not release waiter")
		return nil
	}
}

func TestUserOperationRequiresExactReceiptAndUnknownSurvivesReplacement(t *testing.T) {
	f := newStatsServiceFixture(t)
	s := receiptSession(f)
	op := receiptOp(f.email)
	done := make(chan error, 1)
	go func() { done <- f.svc.mgr.ExecuteUserOp(context.Background(), f.nodeID, op) }()
	frame := nextUserOperation(t, s)
	if frame.GetUserOp().GetOperationId() == "" || op.GetOperationId() != "" {
		t.Fatal("operation ID missing or caller object was mutated")
	}
	select {
	case err := <-done:
		t.Fatalf("enqueue treated as execution: %v", err)
	default:
	}
	if !s.prepareUserOperationSend(frame) {
		t.Fatal("writer refused pending intent")
	}
	f.svc.mgr.acknowledgeUserOperation(f.nodeID, s, &chiralv1.UserOpAck{OperationId: "wrong", Result: chiralv1.UserOpResult_USER_OP_RESULT_APPLIED})
	select {
	case err := <-done:
		t.Fatalf("wrong receipt accepted: %v", err)
	default:
	}
	f.svc.mgr.acknowledgeUserOperation(f.nodeID, s, &chiralv1.UserOpAck{OperationId: frame.GetUserOp().GetOperationId(), Result: chiralv1.UserOpResult_USER_OP_RESULT_APPLIED})
	if err := executionResult(t, done); err != nil {
		t.Fatal(err)
	}
	previousID := frame.GetUserOp().GetOperationId()
	s = receiptSession(f)
	go func() { done <- f.svc.mgr.ExecuteUserOp(context.Background(), f.nodeID, op) }()
	frame = nextUserOperation(t, s)
	if frame.GetUserOp().GetOperationId() == previousID {
		t.Fatal("historical ID reused on replacement session")
	}
	select {
	case err := <-done:
		t.Fatalf("historical receipt falsely confirmed current state: %v", err)
	default:
	}
	s.prepareUserOperationSend(frame)
	f.svc.mgr.acknowledgeUserOperation(f.nodeID, s, &chiralv1.UserOpAck{OperationId: frame.GetUserOp().GetOperationId(), Result: chiralv1.UserOpResult_USER_OP_RESULT_APPLIED})
	if err := executionResult(t, done); err != nil {
		t.Fatal(err)
	}
	op.Kind = chiralv1.UserOpKind_USER_OP_KIND_REMOVE
	go func() { done <- f.svc.mgr.ExecuteUserOp(context.Background(), f.nodeID, op) }()
	frame = nextUserOperation(t, s)
	s.prepareUserOperationSend(frame)
	f.svc.mgr.acknowledgeUserOperation(f.nodeID, s, &chiralv1.UserOpAck{OperationId: frame.GetUserOp().GetOperationId(), Result: chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN})
	if err := executionResult(t, done); !errors.Is(err, ErrUserOperationUnknown) {
		t.Fatalf("unknown result=%v", err)
	}
	replacement := receiptSession(f)
	replacement.hello.UserOpReceipts = false
	// A late old-stream receipt must not unlock this uncertain credential.
	f.svc.mgr.acknowledgeUserOperation(f.nodeID, s, &chiralv1.UserOpAck{OperationId: frame.GetUserOp().GetOperationId(), Result: chiralv1.UserOpResult_USER_OP_RESULT_APPLIED})
	if err := f.svc.mgr.ExecuteUserOp(context.Background(), f.nodeID, op); !errors.Is(err, ErrUserOperationUnknown) {
		t.Fatalf("replacement replayed unknown intent: %v", err)
	}
	// A fresh manager models process restart while retaining the actual SQLite
	// journal. It cannot bypass the barrier merely because memory was lost.
	restarted := NewManager(time.Minute, f.svc.logger)
	NewService(f.st, restarted, f.st, f.svc.logger)
	restarted.attach(f.nodeID, replacement)
	if err := restarted.ExecuteUserOp(context.Background(), f.nodeID, op); !errors.Is(err, ErrUserOperationUnknown) {
		t.Fatalf("restart replayed unknown intent: %v", err)
	}
	select {
	case <-replacement.send:
		t.Fatal("unknown operation entered replacement stream")
	default:
	}
}

func TestUserOperationCancellationBeforeAndAfterDispatch(t *testing.T) {
	f := newStatsServiceFixture(t)
	s := receiptSession(f)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- f.svc.mgr.ExecuteUserOp(ctx, f.nodeID, receiptOp(f.email)) }()
	buffered := nextUserOperation(t, s)
	cancel()
	if err := executionResult(t, done); !errors.Is(err, context.Canceled) || errors.Is(err, ErrUserOperationUnknown) {
		t.Fatalf("unsent cancellation=%v", err)
	}
	if s.prepareUserOperationSend(buffered) {
		t.Fatal("cancelled buffered write was dispatched")
	}
	ctx, cancel = context.WithCancel(context.Background())
	go func() { done <- f.svc.mgr.ExecuteUserOp(ctx, f.nodeID, receiptOp(f.email)) }()
	dispatched := nextUserOperation(t, s)
	if !s.prepareUserOperationSend(dispatched) {
		t.Fatal("known-unsent cancellation blocked a new attempt")
	}
	cancel()
	if err := executionResult(t, done); !errors.Is(err, ErrUserOperationUnknown) {
		t.Fatalf("sent cancellation=%v", err)
	}
	if err := f.svc.mgr.ExecuteUserOp(context.Background(), f.nodeID, receiptOp(f.email)); !errors.Is(err, ErrUserOperationUnknown) {
		t.Fatalf("sent cancellation permitted replay: %v", err)
	}
	// An exact late receipt on the SAME stream may resolve the intent, without
	// invoking the runtime again or pretending the cancelled caller succeeded.
	f.svc.mgr.acknowledgeUserOperation(f.nodeID, s, &chiralv1.UserOpAck{OperationId: dispatched.GetUserOp().GetOperationId(), Result: chiralv1.UserOpResult_USER_OP_RESULT_APPLIED})
	go func() { done <- f.svc.mgr.ExecuteUserOp(context.Background(), f.nodeID, receiptOp(f.email)) }()
	fresh := nextUserOperation(t, s)
	s.prepareUserOperationSend(fresh)
	f.svc.mgr.acknowledgeUserOperation(f.nodeID, s, &chiralv1.UserOpAck{OperationId: fresh.GetUserOp().GetOperationId(), Result: chiralv1.UserOpResult_USER_OP_RESULT_APPLIED})
	if err := executionResult(t, done); err != nil {
		t.Fatalf("exact late receipt did not resolve uncertainty: %v", err)
	}
}

func TestUserOperationLegacyNodePreservesEnqueueContract(t *testing.T) {
	f := newStatsServiceFixture(t)
	s := receiptSession(f)
	s.hello.UserOpReceipts = false
	if err := f.svc.mgr.ExecuteUserOp(context.Background(), f.nodeID, receiptOp(f.email)); err != nil {
		t.Fatal(err)
	}
	if nextUserOperation(t, s).GetUserOp().GetOperationId() != "" {
		t.Fatal("new execution protocol sent to old node")
	}
}

package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/protobuf/proto"
)

const userOperationTimeout = 30 * time.Second
const maxPendingUserOperations = 32

// ErrUserOperationUnknown is never a retryable failure: the runtime may have
// changed already. Its durable barrier survives replacing the stream or Core.
var ErrUserOperationUnknown = store.ErrUserOperationUncertain

type pendingUserOperation struct {
	id        string
	sent      bool
	cancelled bool
	result    chan error
}

// ExecuteUserOp is the actual profile enforcement path, not an enqueue ACK.
// Older nodes deliberately keep their historical behavior. New nodes must
// return an exact receipt; a missing receipt is persisted as uncertain.
func (m *Manager) ExecuteUserOp(ctx context.Context, nodeID string, op *chiralv1.UserOp) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	s := m.sessions[nodeID]
	m.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("node %s is offline", nodeID)
	}
	if !s.hello.GetUserOpReceipts() {
		if m.userOpStore != nil {
			pending, err := m.userOpStore.UserOperationPending(nodeID, op.GetInboundTag(), op.GetEmail())
			if err != nil {
				return err
			}
			if pending {
				return ErrUserOperationUnknown
			}
		}
		if op == nil {
			return fmt.Errorf("invalid user operation")
		}
		legacy := proto.Clone(op).(*chiralv1.UserOp)
		legacy.OperationId = ""
		legacy.ExecutionTimeoutMillis = 0
		legacy.ExecutionSequence = 0
		return s.enqueue(&chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{UserOp: legacy}})
	}
	if m.userOpStore == nil {
		return fmt.Errorf("user operation journal is not configured")
	}
	if op == nil || op.GetInboundTag() == "" || op.GetEmail() == "" || len(op.GetInboundTag()) > 4096 || len(op.GetEmail()) > 4096 || len(op.GetAccountJson()) > 512<<10 || (op.GetKind() != chiralv1.UserOpKind_USER_OP_KIND_ADD && op.GetKind() != chiralv1.UserOpKind_USER_OP_KIND_REMOVE) {
		return fmt.Errorf("invalid user operation")
	}
	request := proto.Clone(op).(*chiralv1.UserOp)
	request.OperationId = ""
	request.ExecutionTimeoutMillis = 0
	request.ExecutionSequence = 0
	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	request.OperationId = hex.EncodeToString(nonce[:])
	budget := userOperationTimeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < budget {
		budget = time.Until(deadline)
	}
	if budget < time.Millisecond {
		return context.DeadlineExceeded
	}
	request.ExecutionTimeoutMillis = uint32(budget / time.Millisecond)
	p := &pendingUserOperation{id: request.OperationId, result: make(chan error, 1)}
	s.opMu.Lock()
	if len(s.userOps) >= maxPendingUserOperations {
		s.opMu.Unlock()
		return fmt.Errorf("node user operation queue is full")
	}
	err = m.userOpStore.BeginUserOperation(nodeID, request.GetInboundTag(), request.GetEmail(), p.id, int32(request.GetKind()), hex.EncodeToString(digest[:]))
	if err != nil {
		s.opMu.Unlock()
		return err
	}
	if s.userOps == nil {
		s.userOps = make(map[string]*pendingUserOperation)
	}
	s.userOpSequence++
	request.ExecutionSequence = s.userOpSequence
	s.userOps[p.id] = p
	err = s.enqueue(&chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{UserOp: request}})
	if err != nil {
		delete(s.userOps, p.id)
		finishErr := m.userOpStore.FinishUserOperation(nodeID, p.id, 2)
		s.opMu.Unlock()
		return errors.Join(err, finishErr)
	}
	s.opMu.Unlock()
	waitCtx, cancel := context.WithTimeout(ctx, userOperationTimeout)
	defer cancel()
	select {
	case err := <-p.result:
		return err
	case <-waitCtx.Done():
		return m.cancelUserOperation(s, p, waitCtx.Err())
	case <-s.done:
		return m.cancelUserOperation(s, p, fmt.Errorf("node session ended"))
	}
}

func (m *Manager) cancelUserOperation(s *Session, p *pendingUserOperation, cause error) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	// Prefer a receipt which committed immediately before cancellation.
	select {
	case err := <-p.result:
		return err
	default:
	}
	if !p.sent {
		p.cancelled = true
		delete(s.userOps, p.id)
		if err := m.userOpStore.FinishUserOperation(s.nodeID, p.id, 2); err != nil {
			return errors.Join(ErrUserOperationUnknown, cause, err)
		}
		return cause
	}
	// Keep the exact pending ID so a late receipt on this same stream can
	// resolve it. A replacement stream cannot acknowledge this operation.
	return errors.Join(ErrUserOperationUnknown, cause)
}

// prepareUserOperationSend runs immediately before stream.Send under the
// session writer. Cancellation while buffered can therefore prove no runtime
// invocation; once handed to gRPC every missing result is uncertain.
func (s *Session) prepareUserOperationSend(f *chiralv1.CoreFrame) bool {
	op := f.GetUserOp()
	if op == nil || op.GetOperationId() == "" {
		return true
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	p := s.userOps[op.GetOperationId()]
	if p == nil || p.cancelled {
		return false
	}
	select {
	case <-s.done:
		return false
	default:
	}
	p.sent = true
	return true
}

func (m *Manager) acknowledgeUserOperation(nodeID string, s *Session, ack *chiralv1.UserOpAck) {
	if ack == nil || !s.hello.GetUserOpReceipts() {
		return
	}
	m.mu.RLock()
	current := m.sessions[nodeID] == s
	if !current {
		m.mu.RUnlock()
		return
	}
	defer m.mu.RUnlock()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	p := s.userOps[ack.GetOperationId()]
	if p == nil || !p.sent || p.cancelled {
		return
	}
	var err error
	committed := false
	switch ack.GetResult() {
	case chiralv1.UserOpResult_USER_OP_RESULT_APPLIED:
		err = m.userOpStore.FinishUserOperation(nodeID, p.id, 1)
		committed = err == nil
	case chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED:
		err = m.userOpStore.FinishUserOperation(nodeID, p.id, 2)
		committed = err == nil
		if err == nil {
			err = fmt.Errorf("agent did not execute user operation")
		}
	case chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN:
		err = ErrUserOperationUnknown
	default:
		return
	}
	// Do not trust provider text with credentials in Core-facing errors.
	if ack.GetResult() != chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN && !committed {
		err = errors.Join(ErrUserOperationUnknown, err)
	}
	select {
	case p.result <- err:
	default:
	}
	if committed {
		delete(s.userOps, p.id)
	}
}

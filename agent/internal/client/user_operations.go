package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/protobuf/proto"
)

const maxUserOperationReceipts = 128

type executionReceipt struct {
	digest [32]byte
	frame  *chiralv1.AgentFrame
}

type queuedMutation struct {
	frame      *chiralv1.CoreFrame
	receivedAt time.Time
}

func isMutationFrame(f *chiralv1.CoreFrame) bool {
	if f.GetConfigPush() != nil || f.GetUserOp() != nil {
		return true
	}
	return f.GetCommand().GetRestartXray() != nil
}

func (c *Client) runMutationWorker(ctx context.Context, send chan<- *chiralv1.AgentFrame, work <-chan queuedMutation) {
	// IDs are never retried across streams. The small stream-local cache only
	// prevents a duplicate transport frame from invoking the runtime twice.
	receipts := make(map[string]executionReceipt)
	var order []string
	var highestSequence uint64
	for {
		select {
		case <-ctx.Done():
			return
		case queued := <-work:
			f := queued.frame
			if ctx.Err() != nil {
				return
			}
			c.mutationMu.Lock()
			if ctx.Err() != nil {
				c.mutationMu.Unlock()
				return
			}
			if op := f.GetUserOp(); op != nil && op.GetOperationId() != "" {
				raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(op)
				digest := sha256.Sum256(raw)
				if previous, ok := receipts[op.GetOperationId()]; ok {
					if previous.digest == digest {
						trySend(ctx, send, previous.frame)
					} else {
						trySend(ctx, send, userOperationAck(op.GetOperationId(), chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN, "Operation ID was reused with a different request."))
					}
				} else if op.GetExecutionSequence() == 0 || op.GetExecutionSequence() <= highestSequence {
					trySend(ctx, send, userOperationAck(op.GetOperationId(), chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN, "Operation sequence was already observed; execution cannot be repeated."))
				} else {
					highestSequence = op.GetExecutionSequence()
					remaining := userOperationBudget(op) - time.Since(queued.receivedAt)
					var ack *chiralv1.AgentFrame
					if remaining <= 0 {
						ack = userOperationAck(op.GetOperationId(), chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "Operation expired before execution.")
					} else {
						callCtx, cancel := context.WithTimeout(ctx, remaining)
						ack = c.executeUserOperation(callCtx, op)
						cancel()
					}
					if len(order) == maxUserOperationReceipts {
						delete(receipts, order[0])
						order = order[1:]
					}
					order = append(order, op.GetOperationId())
					receipts[op.GetOperationId()] = executionReceipt{digest: digest, frame: ack}
					trySend(ctx, send, ack)
				}
			} else {
				c.handleFrame(ctx, send, f)
			}
			c.mutationMu.Unlock()
		}
	}
}

func userOperationBudget(op *chiralv1.UserOp) time.Duration {
	budget := time.Duration(op.GetExecutionTimeoutMillis()) * time.Millisecond
	if budget <= 0 || budget > 30*time.Second {
		return 30 * time.Second
	}
	return budget
}

func userOperationAck(id string, result chiralv1.UserOpResult, detail string) *chiralv1.AgentFrame {
	return &chiralv1.AgentFrame{Frame: &chiralv1.AgentFrame_UserOpAck{UserOpAck: &chiralv1.UserOpAck{OperationId: id, Result: result, Error: detail}}}
}

func (c *Client) handleUserOperation(ctx context.Context, send chan<- *chiralv1.AgentFrame, op *chiralv1.UserOp) {
	ack := c.executeUserOperation(ctx, op)
	if op.GetOperationId() != "" {
		trySend(ctx, send, ack)
	}
}

func (c *Client) executeUserOperation(ctx context.Context, op *chiralv1.UserOp) *chiralv1.AgentFrame {
	if op == nil {
		return userOperationAck("", chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "User operation is missing.")
	}
	id := op.GetOperationId()
	if id != "" {
		if decoded, err := hex.DecodeString(id); err != nil || len(decoded) != 16 {
			return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "Operation ID is invalid.")
		}
	}
	if op.GetInboundTag() == "" || op.GetEmail() == "" || len(op.GetInboundTag()) > 4096 || len(op.GetEmail()) > 4096 || len(op.GetAccountJson()) > 512<<10 || (op.GetKind() != chiralv1.UserOpKind_USER_OP_KIND_ADD && op.GetKind() != chiralv1.UserOpKind_USER_OP_KIND_REMOVE) {
		return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "User operation is invalid.")
	}
	if op.GetRetireCredential() && op.GetKind() != chiralv1.UserOpKind_USER_OP_KIND_REMOVE {
		return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "Only a removal can retire a credential.")
	}
	if op.GetKind() == chiralv1.UserOpKind_USER_OP_KIND_ADD {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(op.GetAccountJson(), &object); err != nil || object == nil {
			return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "User account must be a JSON object.")
		}
	}
	if ctx.Err() != nil {
		return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "Connection ended before execution.")
	}
	callCtx, cancel := context.WithTimeout(ctx, userOperationBudget(op))
	defer cancel()
	var err error
	if op.GetKind() == chiralv1.UserOpKind_USER_OP_KIND_ADD {
		err = c.rt.AddUser(callCtx, op.GetInboundTag(), op.GetEmail(), op.GetAccountJson())
	} else {
		if retirer, ok := c.rt.(runtimeprovider.CredentialRetirer); ok && op.GetRetireCredential() {
			err = retirer.RetireUser(callCtx, op.GetInboundTag(), op.GetEmail())
		} else {
			err = c.rt.RemoveUser(callCtx, op.GetInboundTag(), op.GetEmail())
		}
	}
	if err != nil {
		if errors.Is(err, runtimeprovider.ErrOperationNotExecuted) {
			return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, "Runtime rejected the operation before execution.")
		}
		c.logger.Error("user operation result uncertain", "kind", op.GetKind(), "email", op.GetEmail(), "err", err)
		if id == "" {
			c.QueueEvent(chiralv1.EventKind_EVENT_KIND_ERROR, fmt.Sprintf("User operation %v failed; inspect the Agent log.", op.GetKind()))
		}
		return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN, "Runtime operation did not return a confirmed result; reconciliation is required.")
	}
	return userOperationAck(id, chiralv1.UserOpResult_USER_OP_RESULT_APPLIED, "")
}

package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SayukiOvO/chiral/agent/internal/collector"
	"github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
	"google.golang.org/grpc"
)

const userReceiptID = "0123456789abcdef0123456789abcdef"

type resultRuntime struct {
	fakeRuntime
	callErr error
	calls   int
}

func (r *resultRuntime) AddUser(context.Context, string, string, []byte) error {
	r.calls++
	return r.callErr
}

func TestUserOperationDistinguishesPreflightRefusalAndUnknownWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		account string
		want    chiralv1.UserOpResult
		calls   int
	}{
		{"confirmed", nil, `{"id":"private"}`, chiralv1.UserOpResult_USER_OP_RESULT_APPLIED, 1},
		{"preflight", runtimeprovider.ErrOperationNotExecuted, `{"id":"private"}`, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, 1},
		{"ambiguous", errors.New("private-upstream-secret"), `{"id":"private"}`, chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN, 1},
		{"timeout", context.DeadlineExceeded, `{"id":"private"}`, chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN, 1},
		{"invalid-account", nil, `null`, chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &resultRuntime{callErr: tc.err}
			c := NewWithRuntime(Config{}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
			ack := c.executeUserOperation(context.Background(), &chiralv1.UserOp{OperationId: userReceiptID, Kind: chiralv1.UserOpKind_USER_OP_KIND_ADD, InboundTag: "in", Email: "email", AccountJson: []byte(tc.account)}).GetUserOpAck()
			if ack.GetResult() != tc.want || rt.calls != tc.calls {
				t.Fatalf("result=%v calls=%d", ack.GetResult(), rt.calls)
			}
			if strings.Contains(ack.GetError(), "private") {
				t.Fatal("runtime error leaked to Core receipt")
			}
		})
	}
}

type receiptStream struct {
	grpc.ClientStream
	ctx context.Context
	in  chan *chiralv1.CoreFrame
	out chan *chiralv1.AgentFrame
}

func (s *receiptStream) Send(f *chiralv1.AgentFrame) error {
	select {
	case s.out <- f:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *receiptStream) Recv() (*chiralv1.CoreFrame, error) {
	select {
	case f := <-s.in:
		return f, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

type receiptService struct {
	chiralv1.AgentServiceClient
	stream *receiptStream
}

func (s receiptService) Channel(ctx context.Context, _ ...grpc.CallOption) (grpc.BidiStreamingClient[chiralv1.AgentFrame, chiralv1.CoreFrame], error) {
	s.stream.ctx = ctx
	return s.stream, nil
}

type receiptDependentRuntime struct {
	fakeRuntime
	started     chan struct{}
	receipt     chan struct{}
	receiptOnce sync.Once
	order       chan string
	calls       atomic.Int32
}

func (r *receiptDependentRuntime) AddUser(ctx context.Context, _ string, _ string, _ []byte) error {
	if r.calls.Add(1) == 1 {
		close(r.started)
	}
	select {
	case <-r.receipt:
		r.order <- "add"
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *receiptDependentRuntime) Apply(int64, []byte) error { r.order <- "config"; return nil }
func (r *receiptDependentRuntime) Restart() error            { r.order <- "restart"; return nil }
func (r *receiptDependentRuntime) StatsBatch(context.Context) (*runtimeprovider.AccountingBatch, error) {
	return nil, nil
}
func (r *receiptDependentRuntime) AcknowledgeStats(string, uint64) error {
	r.receiptOnce.Do(func() { close(r.receipt) })
	return nil
}

func TestStreamContinuesReceiptsWhileSerialMutationWaits(t *testing.T) {
	rt := &receiptDependentRuntime{started: make(chan struct{}), receipt: make(chan struct{}), order: make(chan string, 3)}
	c := NewWithRuntime(Config{HeartbeatInterval: time.Hour, StatsInterval: time.Hour, UserOperationReceipts: true}, rt, collector.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &receiptStream{in: make(chan *chiralv1.CoreFrame, 8), out: make(chan *chiralv1.AgentFrame, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.runStream(ctx, receiptService{stream: s}) }()
	select {
	case hello := <-s.out:
		if !hello.GetHello().GetUserOpReceipts() {
			t.Fatal("execution capability not negotiated")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Hello")
	}
	op := &chiralv1.UserOp{OperationId: userReceiptID, ExecutionSequence: 1, Kind: chiralv1.UserOpKind_USER_OP_KIND_ADD, InboundTag: "in", Email: "email", AccountJson: []byte(`{"id":"private"}`)}
	s.in <- &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{UserOp: op}}
	select {
	case <-rt.started:
	case <-time.After(2 * time.Second):
		t.Fatal("user operation never started")
	}
	s.in <- &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_ConfigPush{ConfigPush: &chiralv1.ConfigPush{Version: 7, ConfigJson: []byte(`{}`)}}}
	s.in <- &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_Command{Command: &chiralv1.Command{Cmd: &chiralv1.Command_RestartXray{RestartXray: &chiralv1.RestartXray{}}}}}
	// This frame can only unblock AddUser if the receiver is still running.
	s.in <- &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_StatsAck{StatsAck: &chiralv1.StatsAck{ReporterId: statsReporterID, Sequence: 1}}}
	for _, want := range []string{"add", "config", "restart"} {
		select {
		case got := <-rt.order:
			if got != want {
				t.Fatalf("mutation order=%s want=%s", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("receiver deadlocked behind mutation")
		}
	}
	readReceipt := func() {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case frame := <-s.out:
				if ack := frame.GetUserOpAck(); ack != nil {
					if ack.GetOperationId() != userReceiptID || ack.GetResult() != chiralv1.UserOpResult_USER_OP_RESULT_APPLIED {
						t.Fatalf("wrong receipt: %v", ack)
					}
					return
				}
			case <-deadline:
				t.Fatal("execution receipt missing")
			}
		}
	}
	readReceipt()
	s.in <- &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{UserOp: op}}
	readReceipt()
	if rt.calls.Load() != 1 {
		t.Fatal("duplicate operation invoked runtime again")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream exit=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled stream did not exit")
	}
}

type legacyOrderedRuntime struct {
	fakeRuntime
	applying chan struct{}
	release  chan struct{}
	order    chan string
}

func (r *legacyOrderedRuntime) Apply(int64, []byte) error {
	close(r.applying)
	<-r.release
	r.order <- "config"
	return nil
}

func (r *legacyOrderedRuntime) Install(context.Context, *chiralv1.XrayInstall, runtimeprovider.Relay, runtimeprovider.ProgressFunc) (chiralv1.XrayInstallPhase, string) {
	r.order <- "install"
	return chiralv1.XrayInstallPhase_XRAY_INSTALL_PHASE_ACTIVE, "confirmed"
}

func TestLegacyStreamAppliesConfigurationBeforeFollowingInstall(t *testing.T) {
	rt := &legacyOrderedRuntime{applying: make(chan struct{}), release: make(chan struct{}), order: make(chan string, 2)}
	c := NewWithRuntime(Config{HeartbeatInterval: time.Hour, StatsInterval: time.Hour}, rt, collector.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := &receiptStream{in: make(chan *chiralv1.CoreFrame), out: make(chan *chiralv1.AgentFrame, 16)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.runStream(ctx, receiptService{stream: s}) }()
	select {
	case hello := <-s.out:
		if hello.GetHello().GetUserOpReceipts() {
			t.Fatal("legacy runtime advertised execution receipts")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Hello")
	}
	s.in <- &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_ConfigPush{ConfigPush: &chiralv1.ConfigPush{Version: 7, ConfigJson: []byte(`{}`)}}}
	select {
	case <-rt.applying:
	case <-time.After(2 * time.Second):
		t.Fatal("configuration did not start")
	}
	installReceived := make(chan struct{})
	go func() {
		select {
		case s.in <- &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_XrayInstall{XrayInstall: &chiralv1.XrayInstall{Version: "26.9.8", Activate: true}}}:
			close(installReceived)
		case <-ctx.Done():
		}
	}()
	// In the original native/SHADOW dispatch path the reader cannot accept the
	// next install frame until Apply returns. A candidate worker would receive
	// it immediately and allow the install to overtake validation.
	select {
	case <-installReceived:
		close(rt.release)
		t.Fatal("legacy installation overtook the pending configuration")
	case <-time.After(50 * time.Millisecond):
	}
	close(rt.release)
	for _, want := range []string{"config", "install"} {
		select {
		case got := <-rt.order:
			if got != want {
				t.Fatalf("legacy dispatch order=%s want=%s", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("legacy operation did not finish")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stream exit=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled legacy stream did not exit")
	}
}

func TestQueuedUserOperationExpiresWithoutRuntimeInvocation(t *testing.T) {
	rt := &resultRuntime{}
	c := NewWithRuntime(Config{}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	work := make(chan queuedMutation, 1)
	send := make(chan *chiralv1.AgentFrame, 1)
	go c.runMutationWorker(ctx, send, work)
	work <- queuedMutation{receivedAt: time.Now().Add(-time.Second), frame: &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{UserOp: &chiralv1.UserOp{OperationId: userReceiptID, ExecutionSequence: 1, Kind: chiralv1.UserOpKind_USER_OP_KIND_ADD, InboundTag: "in", Email: "email", AccountJson: []byte(`{"id":"private"}`), ExecutionTimeoutMillis: 1}}}}
	select {
	case frame := <-send:
		if frame.GetUserOpAck().GetResult() != chiralv1.UserOpResult_USER_OP_RESULT_NOT_EXECUTED || rt.calls != 0 {
			t.Fatal("expired operation reached runtime")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expired receipt missing")
	}
}

func TestReceiptWindowRemainsBoundedWithoutBlockingNewOperations(t *testing.T) {
	rt := &resultRuntime{}
	c := NewWithRuntime(Config{}, rt, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	work := make(chan queuedMutation, 1)
	send := make(chan *chiralv1.AgentFrame, 1)
	go c.runMutationWorker(ctx, send, work)
	dispatch := func(sequence uint64) chiralv1.UserOpResult {
		t.Helper()
		work <- queuedMutation{receivedAt: time.Now(), frame: &chiralv1.CoreFrame{Frame: &chiralv1.CoreFrame_UserOp{UserOp: &chiralv1.UserOp{OperationId: fmt.Sprintf("%032x", sequence), ExecutionSequence: sequence, Kind: chiralv1.UserOpKind_USER_OP_KIND_ADD, InboundTag: "in", Email: "email", AccountJson: []byte(`{"id":"private"}`)}}}}
		select {
		case frame := <-send:
			return frame.GetUserOpAck().GetResult()
		case <-time.After(2 * time.Second):
			t.Fatal("receipt missing")
			return 0
		}
	}
	for sequence := uint64(1); sequence <= maxUserOperationReceipts+2; sequence++ {
		if got := dispatch(sequence); got != chiralv1.UserOpResult_USER_OP_RESULT_APPLIED {
			t.Fatalf("new sequence %d was blocked: %v", sequence, got)
		}
	}
	wantCalls := maxUserOperationReceipts + 2
	if got := dispatch(1); got != chiralv1.UserOpResult_USER_OP_RESULT_UNKNOWN || rt.calls != wantCalls {
		t.Fatal("evicted operation was replayed")
	}
	if got := dispatch(uint64(wantCalls)); got != chiralv1.UserOpResult_USER_OP_RESULT_APPLIED || rt.calls != wantCalls {
		t.Fatal("cached receipt invoked runtime again")
	}
	if got := dispatch(uint64(wantCalls + 1)); got != chiralv1.UserOpResult_USER_OP_RESULT_APPLIED || rt.calls != wantCalls+1 {
		t.Fatal("receipt retention limit stopped ordinary operations")
	}
}

package profile

import (
	"context"
	"errors"
	"testing"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type executionPusher struct {
	*fakePusher
	result error
	calls  int
}

func (p *executionPusher) ExecuteUserOp(ctx context.Context, _ string, _ *chiralv1.UserOp) error {
	p.calls++
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.result
}

func TestSyncUserDoesNotMarkActiveWithoutExecutionReceipt(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := entitle(t, st, "execution-confirmed", p.ID, nil)
	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}
	executor := &executionPusher{fakePusher: push, result: errors.New("execution outcome unknown")}
	svc.userOps = executor
	if changed, err := svc.SyncUser(context.Background(), u.ID); err != nil || !changed {
		t.Fatalf("sync changed=%v err=%v", changed, err)
	}
	got, _ := st.GetUser(u.ID)
	if got.Active || executor.calls != 1 || len(push.userOps) != 0 {
		t.Fatal("enqueue fallback or unknown result marked user active")
	}
	executor.result = nil
	if _, err := svc.SyncUser(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetUser(u.ID)
	if !got.Active || executor.calls != 2 {
		t.Fatal("confirmed execution did not mark user active")
	}
}

func TestPermanentCredentialRetirementReturnsExecutionFailure(t *testing.T) {
	svc, st, push := newFixture(t)
	p, n := realityProfile(t, svc, st)
	u := entitle(t, st, "retirement-confirmed", p.ID, nil)
	if _, err := svc.AssembleNode(n.ID); err != nil {
		t.Fatal(err)
	}
	want := errors.New("retirement outcome unknown")
	svc.userOps = &executionPusher{fakePusher: push, result: want}
	if err := svc.RemoveUserFromProfile(context.Background(), u.ID, p.ID); !errors.Is(err, want) {
		t.Fatalf("retirement failure swallowed: %v", err)
	}
	if err := svc.RetireUserCredentials(context.Background(), u.ID); !errors.Is(err, want) {
		t.Fatalf("user deletion retirement failure swallowed: %v", err)
	}
	// Losing capability information when offline must not downgrade a
	// permanent deletion to success, even for the old enqueue-only pusher.
	svc.userOps = push
	push.offline = map[string]bool{n.ID: true}
	if err := svc.RemoveUserFromProfile(context.Background(), u.ID, p.ID); err == nil {
		t.Fatal("offline retirement pretended success")
	}
	creds, err := st.UserCredentials(u.ID)
	if err != nil || len(creds) != 1 {
		t.Fatalf("retirement removed necessary credential context: %v %v", creds, err)
	}
}

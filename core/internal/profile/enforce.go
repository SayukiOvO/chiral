package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/user"
	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// Enforcement keeps what the nodes are serving in line with what the database
// says a user is entitled to.
//
// Two states, deliberately distinct: `enabled/quota/expiry` say what SHOULD be
// true, and `users.active` records what the nodes were last told. Only the
// difference is pushed, so a sweep over an unchanged fleet sends nothing, and
// a push that fails simply leaves the difference in place to retry.

// UserOpPusher sends an online add/remove to one node. Implemented by
// node.Manager; declared here so this package stays off the gRPC layer.
type UserOpPusher interface {
	SendUserOp(nodeID string, op *chiralv1.UserOp) error
}

// InboundTagFor reports the inbound tag a profile renders to on a node, which
// is what an online user operation has to name.
func (s *Service) InboundTagFor(profileID, nodeID string) (string, error) {
	p, err := s.st.GetProfile(profileID)
	if err != nil {
		return "", err
	}
	if p.InboundTemplate == "" {
		return "", fmt.Errorf("profile %q has no inbound template", p.Name)
	}
	ctx, err := s.contextFor(profileID, nodeID)
	if err != nil {
		return "", err
	}
	rendered, err := ctx.Render(p.InboundTemplate)
	if err != nil {
		return "", fmt.Errorf("profile %q: %w", p.Name, err)
	}
	var inbound struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal([]byte(rendered), &inbound); err != nil {
		return "", fmt.Errorf("profile %q inbound is not a JSON object: %w", p.Name, err)
	}
	if inbound.Tag == "" {
		return "", fmt.Errorf("profile %q inbound has no tag; online user operations need one", p.Name)
	}
	return inbound.Tag, nil
}

// SyncUser brings one user's live presence on the nodes in line with their
// entitlement, and returns whether anything was pushed.
//
// The user's `active` flag is only flipped once every node accepted its
// operation. A partial success stays "not yet done" so the next sweep retries
// it — better to re-send a redundant removal than to record a ban that only
// landed on half the fleet.
func (s *Service) SyncUser(ctx context.Context, userID string) (bool, error) {
	if s.userOps == nil {
		return false, nil
	}
	u, err := s.st.GetUser(userID)
	if err != nil {
		return false, err
	}
	want := user.Allowed(u, time.Now().Unix())
	if want == u.Active {
		return false, nil
	}

	creds, err := s.st.UserCredentials(userID)
	if err != nil {
		return false, err
	}
	if len(creds) == 0 {
		// Nothing to install anywhere; record the state so we stop looking.
		return false, s.st.SetUserActive(userID, want)
	}

	allOK := true
	for _, c := range creds {
		if err := s.pushUserOp(u, c, want); err != nil {
			// Offline nodes are the common case and not worth an error log:
			// assembly will include or exclude the user on the next config
			// push, which happens automatically on reconnect.
			s.logger.Info("online user op not delivered", "user", u.Name,
				"node", c.NodeID, "allow", want, "reason", err)
			allOK = false
		}
	}
	if !allOK {
		return true, nil
	}
	s.logger.Info("user access synced", "user", u.Name, "allowed", want, "credentials", len(creds))
	return true, s.st.SetUserActive(userID, want)
}

func (s *Service) pushUserOp(u store.User, c store.Credential, allow bool) error {
	tag, err := s.InboundTagFor(c.ProfileID, c.NodeID)
	if err != nil {
		return err
	}
	op := &chiralv1.UserOp{
		Kind:       chiralv1.UserOpKind_USER_OP_KIND_REMOVE,
		InboundTag: tag,
		Email:      c.Email,
	}
	if allow {
		p, err := s.st.GetProfile(c.ProfileID)
		if err != nil {
			return err
		}
		if p.ClientEntry == "" {
			return fmt.Errorf("profile %q has no client entry", p.Name)
		}
		rctx, err := s.contextFor(c.ProfileID, c.NodeID)
		if err != nil {
			return err
		}
		entry, err := rctx.With(user.CredentialVars(c)).Render(p.ClientEntry)
		if err != nil {
			return err
		}
		op.Kind = chiralv1.UserOpKind_USER_OP_KIND_ADD
		op.AccountJson = []byte(entry)
	}
	return s.userOps.SendUserOp(c.NodeID, op)
}

// EnforceQuotas is the periodic sweep: renew whoever is due, then push the
// difference for anyone whose entitlement no longer matches what the nodes
// were told. Returns how many users changed state.
//
// It is also what makes quota enforcement eventually consistent: traffic
// arrives between sweeps, so a user goes over quota quietly and is cut off at
// the next pass rather than mid-request.
func (s *Service) EnforceQuotas(ctx context.Context) (int, error) {
	users, err := s.st.ListUsers()
	if err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	changed := 0
	for _, u := range users {
		if renewed := s.renewIfDue(u, now); renewed {
			// Re-read: renewal zeroes usage and moves expiry, which is
			// usually what makes the user allowed again.
			if fresh, err := s.st.GetUser(u.ID); err == nil {
				u = fresh
			}
		}
		did, err := s.SyncUser(ctx, u.ID)
		if err != nil {
			s.logger.Error("syncing user failed", "user", u.Name, "err", err)
			continue
		}
		if did {
			changed++
		}
	}
	return changed, nil
}

// renewIfDue rolls an expired user into their next period. Quota is per
// period, so usage resets with the expiry date.
func (s *Service) renewIfDue(u store.User, now int64) bool {
	if u.RenewPeriod <= 0 || u.ExpiresAt == 0 || now < u.ExpiresAt {
		return false
	}
	next := u.ExpiresAt
	// Catch up in whole periods, so a panel that was down for a while does not
	// leave the user with an expiry still in the past.
	for next <= now {
		next += u.RenewPeriod
	}
	if err := s.st.RenewUser(u.ID, next); err != nil {
		s.logger.Error("renewing user failed", "user", u.Name, "err", err)
		return false
	}
	s.logger.Info("user renewed", "user", u.Name, "expires_at", next)
	return true
}

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

// ApplyUserNodes re-assembles and delivers every node listed, so the STORED
// config matches who is currently entitled.
//
// This is what makes a membership change durable. The online add/remove that
// SyncUser sends only edits the running kernel: an agent restart, an Xray
// restart, or a reconnect re-applies the last stored config, which would
// otherwise resurrect a banned user or drop a newly granted one. Correctness
// lives in the stored config; the online op is what avoids waiting for it.
//
// Errors are collected per node rather than aborting: one unreachable node
// must not stop the others from converging.
func (s *Service) ApplyUserNodes(ctx context.Context, nodeIDs []string) map[string]error {
	errs := map[string]error{}
	for _, id := range unique(nodeIDs) {
		if _, err := s.Apply(ctx, id); err != nil {
			errs[id] = err
			s.logger.Error("re-assembling node after a membership change failed",
				"node", id, "err", err)
		}
	}
	return errs
}

// RemoveUserFromProfile takes a user off the running kernels of every node
// serving one profile, without touching their other entitlements.
//
// Called before the credentials are dropped, since the email and inbound tag
// an online removal needs live on those rows. A node that is offline is not an
// error: the caller rewrites the stored config afterwards, which is what that
// node will pick up.
func (s *Service) RemoveUserFromProfile(ctx context.Context, userID, profileID string) error {
	if s.userOps == nil {
		return nil
	}
	creds, err := s.st.UserCredentials(userID)
	if err != nil {
		return err
	}
	for _, c := range creds {
		if c.ProfileID != profileID {
			continue
		}
		tag, err := s.InboundTagFor(c.ProfileID, c.NodeID)
		if err != nil {
			s.logger.Info("cannot name the inbound to remove from",
				"user", userID, "node", c.NodeID, "reason", err)
			continue
		}
		if err := s.userOps.SendUserOp(c.NodeID, &chiralv1.UserOp{
			Kind:       chiralv1.UserOpKind_USER_OP_KIND_REMOVE,
			InboundTag: tag,
			Email:      c.Email,
		}); err != nil {
			s.logger.Info("online removal not delivered", "user", userID,
				"node", c.NodeID, "reason", err)
		}
	}
	return nil
}

// NodesForUser lists the nodes a user currently holds a credential on. Call it
// BEFORE deleting anything: the credentials are what say where they are
// installed, and dropping them first would leave nothing to reconcile.
func (s *Service) NodesForUser(userID string) ([]string, error) {
	creds, err := s.st.UserCredentials(userID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(creds))
	for _, c := range creds {
		ids = append(ids, c.NodeID)
	}
	return unique(ids), nil
}

// NodesForUserProfile narrows that to one entitlement.
func (s *Service) NodesForUserProfile(userID, profileID string) ([]string, error) {
	creds, err := s.st.UserCredentials(userID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(creds))
	for _, c := range creds {
		if c.ProfileID == profileID {
			ids = append(ids, c.NodeID)
		}
	}
	return unique(ids), nil
}

func unique(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
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
// The user's `active` flag is only flipped once every node's operation was
// ENQUEUED without error. A node that is offline stays "not yet done" so the
// next sweep retries it — better to re-send a redundant removal than to record
// a ban that only landed on half the fleet.
//
// Enqueued, not applied, and the difference is worth naming: SendUserOp hands
// the frame to the session's send channel and returns. If the kernel then
// refuses it — its API unreachable, the inbound gone, the user already absent —
// the agent reports an Event that nothing here consumes, and `active` has
// already been written as though the change landed. The flag therefore means
// "Core believes this is installed", which is what the config re-assembly at
// the end of EnforceQuotas exists to make true regardless: the stored config
// is the authority, and the next restart converges to it even when the online
// op was lost.
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
	// Nodes whose stored config no longer matches who is entitled. Collected
	// across the whole sweep and re-assembled once at the end: a node serving
	// fifty users who all expire at midnight deserves one re-render, not fifty.
	var dirty []string
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
			nodes, err := s.NodesForUser(u.ID)
			if err != nil {
				s.logger.Error("listing a user's nodes failed", "user", u.Name, "err", err)
				continue
			}
			dirty = append(dirty, nodes...)
		}
	}

	// The half that was missing, and the half that makes the other half stick.
	//
	// SyncUser only edits the RUNNING kernel. The stored config is what a
	// reconnecting agent replays, what the agent starts from before Core is
	// even reachable, and what Core re-pushes on reconnect — so a user cut off
	// by this sweep and never written out of the config comes back on the next
	// agent restart, host reboot or Xray restart, and never leaves again:
	// SyncUser returns early once active matches, so nothing retries. The panel
	// goes on reporting allowed=false about somebody who is online.
	//
	// The interactive path has always done both (see updateUser). This one did
	// not, which meant every cut-off that happened by the CLOCK rather than by
	// an admin's click was the one that did not survive a restart.
	if len(dirty) > 0 {
		for id, err := range s.ApplyUserNodes(ctx, dirty) {
			s.logger.Error("re-assembling a node after enforcement failed",
				"node", id, "err", err)
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

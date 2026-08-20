// Package user owns subscribers, their entitlements, and the strongly-isolated
// credentials minted for them: one per user × profile × node, so a leaked
// credential burns exactly one access point.
package user

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
)

type Service struct {
	st     *store.Store
	logger *slog.Logger
}

func NewService(st *store.Store, logger *slog.Logger) *Service {
	return &Service{st: st, logger: logger}
}

// Reasons a user's credentials should not be working. Empty means they should.
const (
	ReasonSuspended      = "suspended"
	ReasonExpired        = "expired"
	ReasonQuotaExhausted = "quota_exhausted"
)

// Reason reports why a user is not entitled to working credentials, or "" if
// they are.
//
// The portal needs to tell someone WHICH condition stopped them, not just
// that one did. Rather than keeping a second copy of these conditions there —
// which would drift the first time a fourth is added — Allowed is defined in
// terms of this, so the two cannot disagree by construction.
//
// Order matters where conditions overlap: an operator switching someone off is
// a more useful thing to say than a quota that also happens to be spent.
func Reason(u store.User, now int64) string {
	if !u.Enabled {
		return ReasonSuspended
	}
	if u.ExpiresAt != 0 && now >= u.ExpiresAt {
		return ReasonExpired
	}
	if u.QuotaBytes != 0 && u.UsedBytes >= u.QuotaBytes {
		return ReasonQuotaExhausted
	}
	return ""
}

// Allowed reports whether a user should currently have working credentials.
// Kept in one place so assembly, enforcement, and the subscription endpoint
// cannot disagree about who is entitled to what.
func Allowed(u store.User, now int64) bool { return Reason(u, now) == "" }

// StatsEmail builds the key Xray reports traffic under.
//
// Uniqueness comes from the three ids, never from names. Names are mutable,
// are not unique once one is freed and reused, and cannot survive
// sanitisation: a purely non-ASCII name (Chinese, say) leaves nothing behind,
// so deriving the key from names alone would collide two users onto one key
// and silently merge their traffic — and, because credentials.email is
// UNIQUE, break assembly for the whole node.
//
// The name is kept only as a readable prefix, and dropped entirely when
// sanitising leaves nothing usable.
//
// The key is fixed at mint time, so renaming a user later keeps their traffic
// history intact.
func StatsEmail(userName, userID, profileID, nodeID string) string {
	return StatsEmailForExit(userName, userID, profileID, nodeID, "")
}

// StatsEmailForExit is the same key with the exit appended.
//
// The exit has to be in the email because the email is what the routing rule
// matches on: one person on one inbound holds one credential per exit, and
// Xray picks between them by user. It is also what traffic arrives under, so
// this is what makes "how much did they use through that provider" a question
// with an answer.
//
// An empty exit produces exactly the old key, so every credential minted
// before this existed keeps its identity and its counters.
func StatsEmailForExit(userName, userID, profileID, nodeID, exitID string) string {
	prefix := sanitize(userName)
	if prefix != "" {
		prefix += "."
	}
	if exitID == "" {
		return fmt.Sprintf("%s%s@%s.%s", prefix, userID, profileID, nodeID)
	}
	return fmt.Sprintf("%s%s@%s.%s.%s", prefix, userID, profileID, nodeID, exitID)
}

// sanitize reduces a display name to characters that are safe in a stats key.
//
// Two hazards it exists for: Xray separates the fields of a stat name with
// ">>>", and `xray api rmu` takes the email as a positional argument, so a
// leading '-' would be parsed as a flag and the user could never be removed.
// Anything unsuitable is dropped rather than replaced, so a name that reduces
// to nothing returns "" and the caller omits the prefix.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_':
			b.WriteRune(r)
		case r == '-' && b.Len() > 0:
			// Never leading: `xray api rmu` would read it as a flag.
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// EnsureCredentials mints any missing credentials for the users entitled to
// this profile on this node, and returns the ones that should currently be
// installed. Users who are disabled, expired, or over quota are minted for but
// left out of the result: revoking access must not destroy the credential, or
// re-enabling a user would hand them a different secret and silently break
// every client they had already configured.
func (s *Service) EnsureCredentials(profileID, nodeID string) ([]store.Credential, error) {
	return s.EnsureCredentialsForExit(profileID, nodeID, "", nil)
}

// EnsureCredentialsForExit does the same for one relayed exit.
//
// allowed, when non-nil, restricts minting to the users who may use that exit
// — an exit is somebody else's node, and being entitled to this access point
// is not the same as being entitled to leave through that provider.
func (s *Service) EnsureCredentialsForExit(profileID, nodeID, exitID string, allowed map[string]bool) ([]store.Credential, error) {
	return s.ensureForExit(profileID, nodeID, exitID, "", allowed)
}

// EnsureCredentialsForRelay does the same for a line out through another node
// of this fleet. Same shape, different exit column, so the two kinds of exit
// cascade with the row they belong to.
func (s *Service) EnsureCredentialsForRelay(profileID, nodeID, relayID string, allowed map[string]bool) ([]store.Credential, error) {
	return s.ensureForExit(profileID, nodeID, "", relayID, allowed)
}

func (s *Service) ensureForExit(profileID, nodeID, exitID, relayID string, allowed map[string]bool) ([]store.Credential, error) {
	userIDs, err := s.st.ProfileUserIDs(profileID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := make([]store.Credential, 0, len(userIDs))
	for _, uid := range userIDs {
		u, err := s.st.GetUser(uid)
		if err != nil {
			if store.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		if allowed != nil && !allowed[u.ID] {
			continue
		}
		secret, err := template.Generate(template.GenUUID)
		if err != nil {
			return nil, err
		}
		// PutCredential is idempotent: an existing credential is returned
		// as-is and this freshly generated secret is discarded.
		c, err := s.st.PutCredential(store.Credential{
			UserID:      u.ID,
			ProfileID:   profileID,
			NodeID:      nodeID,
			ExitProxyID: exitID,
			ExitRelayID: relayID,
			Email: StatsEmailForExit(u.Name, u.ID, profileID, nodeID,
				exitKey(exitID, relayID)),
			Secret: secret.Components[""],
		})
		if err != nil {
			return nil, fmt.Errorf("minting credential for %s: %w", u.Name, err)
		}
		if Allowed(u, now) {
			out = append(out, c)
		}
	}
	return out, nil
}

// exitKey is the exit component of a stats email.
//
// A relayed line is prefixed so the two kinds of exit can never produce the
// same key from different rows: credentials.email is UNIQUE fleet-wide, and a
// collision there does not merge two counters quietly — it fails assembly for
// the whole node.
func exitKey(exitID, relayID string) string {
	if relayID != "" {
		return "r" + relayID
	}
	return exitID
}

// RelayStatsEmail is the key the link's OWN credential reports under, on the
// exit node.
//
// It belongs to no subscriber, so it is not in `credentials` and nothing bills
// it — which is correct: the bytes were already charged to somebody at the
// entry, where they authenticated as themselves. Counting them again here
// would bill the same gigabyte twice, once to a person and once to a machine.
// It is still worth naming: it makes "how much does this line carry" a
// question the exit's own stats can answer.
func RelayStatsEmail(relayID, profileID, nodeID string) string {
	return fmt.Sprintf("relay.%s@%s.%s", relayID, profileID, nodeID)
}

// RelayVars are what a client-entry template sees when rendering the link's
// own credential. The same names a subscriber's would bind, because to the
// exit node this is just one more client.
func RelayVars(r store.NodeRelay, profileID, nodeID string) map[string]string {
	email := RelayStatsEmail(r.ID, profileID, nodeID)
	return map[string]string{
		"user.uuid":     r.Secret,
		"user.password": r.Secret,
		"user.email":    email,
	}
}

// CredentialVars are the per-user names a client-entry or client template may
// reference. They layer on top of the profile's context at render time.
func CredentialVars(c store.Credential) map[string]string {
	return map[string]string{
		"user.uuid":     c.Secret,
		"user.password": c.Secret,
		"user.email":    c.Email,
	}
}

// UserVarNames lists those names for the editor's "known variables" set.
func UserVarNames() []string {
	return []string{"user.uuid", "user.password", "user.email"}
}

// EgressStatsEmail is the key an egress rule's own credential reports under,
// on the node it dials.
//
// Belongs to no subscriber, so it is not in `credentials` and nothing bills
// it — the bytes were already charged to whoever authenticated at the node
// the rule lives on. Same reasoning as RelayStatsEmail; a distinct prefix so
// the two kinds of machine credential can never collide.
func EgressStatsEmail(ruleID, profileID, nodeID string) string {
	return fmt.Sprintf("egress.%s@%s.%s", ruleID, profileID, nodeID)
}

// EgressVars are what a client-entry or client template sees when rendering
// an egress rule's own credential.
func EgressVars(r store.EgressRule) map[string]string {
	email := EgressStatsEmail(r.ID, r.TargetProfileID, r.TargetNodeID)
	return map[string]string{
		"user.uuid":     r.Secret,
		"user.password": r.Secret,
		"user.email":    email,
	}
}

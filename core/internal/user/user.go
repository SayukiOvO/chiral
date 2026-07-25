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

// Allowed reports whether a user should currently have working credentials.
// Kept in one place so assembly, enforcement, and the subscription endpoint
// cannot disagree about who is entitled to what.
func Allowed(u store.User, now int64) bool {
	if !u.Enabled {
		return false
	}
	if u.ExpiresAt != 0 && now >= u.ExpiresAt {
		return false
	}
	if u.QuotaBytes != 0 && u.UsedBytes >= u.QuotaBytes {
		return false
	}
	return true
}

// StatsEmail builds the key Xray reports traffic under. It must be unique
// across the whole fleet — two credentials sharing one would silently merge
// two users' usage — so it carries the node id, whose uniqueness is
// guaranteed, rather than the node name, whose is not.
//
// It is fixed when the credential is minted: renaming a user afterwards keeps
// their traffic history intact, which matters more than the cosmetics.
func StatsEmail(userName, profileName, nodeID string) string {
	return fmt.Sprintf("%s@%s.%s", sanitize(userName), sanitize(profileName), nodeID)
}

// sanitize keeps the stats key parseable: Xray separates the fields of a stat
// name with ">>>", so a name containing it would corrupt the reported key.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "x"
	}
	return b.String()
}

// EnsureCredentials mints any missing credentials for the users entitled to
// this profile on this node, and returns the ones that should currently be
// installed. Users who are disabled, expired, or over quota are minted for but
// left out of the result: revoking access must not destroy the credential, or
// re-enabling a user would hand them a different secret and silently break
// every client they had already configured.
func (s *Service) EnsureCredentials(profileID, nodeID string) ([]store.Credential, error) {
	userIDs, err := s.st.ProfileUserIDs(profileID)
	if err != nil {
		return nil, err
	}
	p, err := s.st.GetProfile(profileID)
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
		secret, err := template.Generate(template.GenUUID)
		if err != nil {
			return nil, err
		}
		// PutCredential is idempotent: an existing credential is returned
		// as-is and this freshly generated secret is discarded.
		c, err := s.st.PutCredential(store.Credential{
			UserID:    u.ID,
			ProfileID: profileID,
			NodeID:    nodeID,
			Email:     StatsEmail(u.Name, p.Name, nodeID),
			Secret:    secret.Components[""],
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

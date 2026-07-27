// Package portal is the end user's side of the panel: the person who bought a
// subscription, as opposed to the operator who runs the fleet.
//
// The two are separate subjects with separate sessions, separate identity
// types and separate data access. Nothing in this package can answer a
// question about the fleet, because nothing in this package can reach the
// store directly — see View.
package portal

import (
	"errors"
	"time"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// Identity is a signed-in end user.
//
// It deliberately has no Role field and no Can/CanWrite/CanAdmin methods. An
// admin handler takes func(w, r) and reads auth.Identity from the request
// context; a portal handler takes an extra portal.Identity argument. The two
// signatures do not unify, so mounting one behind the other's guard is a
// compile error rather than a privilege escalation somebody has to notice in
// review.
//
// Keeping it roleless is a standing rule: the moment portal identities carry
// something auth.rank() understands, every requireAdmin route — nodes, users,
// variables, the audit log, and the config preview with its REALITY private
// keys — opens to customers.
type Identity struct {
	UserID string
	Name   string
}

// ErrNoAccess is returned when a user has no profiles at all. Not an error
// condition so much as a state the portal renders as "not provisioned yet".
var ErrNoAccess = errors.New("portal: no profiles granted")

// Subscriptions is all the portal needs from the subscription side: which
// client formats exist. The content itself is served by /sub/{token}, so
// rendering never happens here — the portal hands out a link, not a config.
type Subscriptions interface {
	Kinds() []string
}

// Data is the narrow slice of the store a View is allowed to use.
//
// Written out explicitly rather than taking *store.Store, because the
// signature trick above only stops a handler being mounted on the wrong guard
// — it does nothing to stop a correctly-guarded handler from calling
// ListNodes() and serialising the whole fleet. With the store absent from the
// portal's reach, over-fetching is a compile error too.
type Data interface {
	GetUser(id string) (store.User, error)
	UserProfileIDs(userID string) ([]string, error)
	ProfileNodeIDs(profileID string) ([]string, error)
	GetNode(id string) (store.Node, error)
	FindCredential(userID, profileID, nodeID string) (store.Credential, error)
	UserNodeTrafficSeries(userID string, from, to time.Time) (map[string][]store.TrafficPoint, error)
	SubToken(userID string) (string, error)
	UserDevices(userID string) ([]store.Device, error)
	NodeAnnouncedOnline(nodeID string) (bool, error)
}

// View answers questions about one user, and only about that user.
//
// Every method is already scoped by the identity it was built from; there is
// no method that takes another user's id. A handler that wanted to leak
// someone else's data would have to add a method here first, which is a much
// more visible change than a mis-scoped query inside a handler.
type View struct {
	id   Identity
	data Data
	subs Subscriptions
	now  func() time.Time
}

func NewView(id Identity, data Data, subs Subscriptions) *View {
	return &View{id: id, data: data, subs: subs, now: time.Now}
}

// Identity reports who this view belongs to.
func (v *View) Identity() Identity { return v.id }

// Account is the user's own status.
func (v *View) Account() (Account, error) {
	u, err := v.data.GetUser(v.id.UserID)
	if err != nil {
		return Account{}, err
	}
	profileIDs, err := v.data.UserProfileIDs(u.ID)
	if err != nil {
		return Account{}, err
	}
	return Account{
		Name:        u.Name,
		QuotaBytes:  u.QuotaBytes,
		UsedBytes:   u.UsedBytes,
		ExpiresAt:   u.ExpiresAt,
		RenewPeriod: u.RenewPeriod,
		CreatedAt:   u.CreatedAt,
		DeviceLimit: u.DeviceLimit,
		Status:      status(u, len(profileIDs), v.now().Unix()),
	}, nil
}

// Account is what an end user may know about their own subscription.
//
// Hand-written rather than derived from store.User: enabled and active are
// operator concepts (one is "should be off", the other "is off", and showing
// the second would flash "inactive" at somebody who is happily browsing), and
// profile_ids is internal structure the user has no use for.
type Account struct {
	Name        string `json:"name"`
	QuotaBytes  int64  `json:"quota_bytes"`
	UsedBytes   int64  `json:"used_bytes"`
	ExpiresAt   int64  `json:"expires_at"`
	RenewPeriod int64  `json:"renew_period"`
	CreatedAt   int64  `json:"created_at"`
	DeviceLimit int    `json:"device_limit"`
	// Status is the single reason the account is not usable, or "active".
	Status string `json:"status"`
}

// Status values. The portal shows one sentence per value rather than exposing
// the booleans behind them.
const (
	StatusActive   = "active"
	StatusNoAccess = "no_access"
	// The remaining values come straight from user.Reason: suspended,
	// expired, quota_exhausted. Not re-declared here, so adding one there
	// cannot leave this list stale.
)

// status folds the account's state into one word.
//
// It calls user.Reason so there is exactly one implementation of "why is this
// account not working"; copying those conditions here would drift the moment
// a fourth is added. no_access is computed separately on purpose: having no
// profiles is an authorisation gap, not an admission failure, and folding it
// into user.Allowed would change what assembly and the quota sweep mean by
// "allowed" across the whole panel.
func status(u store.User, profileCount int, now int64) string {
	if reason := user.Reason(u, now); reason != "" {
		return reason
	}
	if profileCount == 0 {
		return StatusNoAccess
	}
	return StatusActive
}

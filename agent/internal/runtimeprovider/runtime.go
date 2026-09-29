// Package runtimeprovider defines the node runtime contract used by the
// Agent's Core client. Implementations may supervise Xray directly or delegate
// the same operations to another local control plane.
package runtimeprovider

import (
	"context"
	"strings"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

// Runtime is the part of a node runtime the Core client needs for ordinary
// operation. Kernel installation is deliberately not part of this contract:
// the direct-Xray implementation owns its side-by-side installer separately,
// and an API-backed provider must not implement Xray binary management merely
// to satisfy the client.
type Runtime interface {
	// Apply installs one complete desired config revision. Implementations must
	// leave the previous working revision in place when validation fails.
	Apply(version int64, configJSON []byte) error
	Restart() error

	// SetProbe associates the data-path probe rendered with the same config
	// revision. It is used when the runtime supports a guarded kernel upgrade.
	SetProbe(outboundJSON []byte, url string)

	// AddUser and RemoveUser change a live inbound without restarting every
	// other subscriber on the node.
	AddUser(ctx context.Context, inboundTag, email string, accountJSON []byte) error
	RemoveUser(ctx context.Context, inboundTag, email string) error

	// Stats returns traffic deltas since the previous successful read.
	Stats(ctx context.Context) ([]Stat, error)
	// OnlineUsers returns an absolute snapshot. complete is false when the
	// provider could only enumerate part of the current set.
	OnlineUsers(ctx context.Context) (users []OnlineUser, complete bool, err error)

	State() chiralv1.XrayState
	ConfigVersion() int64
	RunningVersion() string
	InstalledVersion() string
}

// Stat is one raw traffic counter reported by a runtime provider.
type Stat struct {
	// Name uses Xray's established key form, for example
	// "user>>>alice@p.node>>>traffic>>>uplink". Keeping the wire-shaped value
	// here lets providers preserve counters that the Agent does not yet consume.
	Name  string
	Value int64
}

// UserTraffic is one credential's traffic since the previous read.
type UserTraffic struct {
	Email string
	Up    int64
	Down  int64
}

// UserTrafficFrom folds raw counters into per-user deltas, dropping the
// inbound and outbound counters Core does not attribute to a user.
func UserTrafficFrom(stats []Stat) []UserTraffic {
	byEmail := map[string]*UserTraffic{}
	order := []string{}
	for _, s := range stats {
		// user>>>{email}>>>traffic>>>{uplink|downlink}
		parts := strings.Split(s.Name, ">>>")
		if len(parts) != 4 || parts[0] != "user" || parts[2] != "traffic" {
			continue
		}
		email := parts[1]
		t, ok := byEmail[email]
		if !ok {
			t = &UserTraffic{Email: email}
			byEmail[email] = t
			order = append(order, email)
		}
		switch parts[3] {
		case "uplink":
			t.Up += s.Value
		case "downlink":
			t.Down += s.Value
		}
	}
	out := make([]UserTraffic, 0, len(order))
	for _, email := range order {
		t := byEmail[email]
		if t.Up == 0 && t.Down == 0 {
			continue
		}
		out = append(out, *t)
	}
	return out
}

// OnlineUser is one credential's currently-connected source addresses.
type OnlineUser struct {
	Email string
	IPs   map[string]int64 // address -> last-seen unix seconds
}

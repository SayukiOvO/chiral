package portal

import (
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
)

// Subscription is the link and the formats it is available in.
type Subscription struct {
	// Available is false for users created before the token became
	// recoverable. They see "regenerate to get a link" rather than a broken
	// copy button.
	Available bool `json:"available"`
	// URL is the base subscription link. Masked for display is the frontend's
	// job — the value itself has to be here, because copying it is the
	// primary action of the whole page.
	URL string `json:"url,omitempty"`
	// Kinds are the client formats this panel can render, from the server so
	// the picker cannot offer a format that does not exist.
	Kinds []string `json:"kinds"`
}

// Subscription returns the user's own link.
func (v *View) Subscription(publicURL string) (Subscription, error) {
	token, err := v.data.SubToken(v.id.UserID)
	if err != nil {
		return Subscription{}, err
	}
	sub := Subscription{Kinds: v.subs.Kinds()}
	if token == "" {
		return sub, nil
	}
	sub.Available = true
	sub.URL = strings.TrimRight(publicURL, "/") + "/sub/" + token
	return sub, nil
}

// Devices are the user's own recorded source addresses.
//
// A user seeing their own addresses is the least contentious version of this
// data — but on a SHARED account it is not only theirs. Whoever else is using
// the credentials appears here too, to anyone who can sign in. That is the
// unavoidable shape of "show me my devices" on an account that is, by
// definition of the feature, sometimes shared, and it is why the UI must say
// "addresses" rather than "your devices".
func (v *View) Devices() ([]Device, error) {
	rows, err := v.data.UserDevices(v.id.UserID)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(rows))
	for _, d := range rows {
		out = append(out, Device{IP: d.IP, LastSeen: d.LastSeen})
	}
	return out, nil
}

// Device is one source address, without the node it was seen on: which node
// someone connected through is operator detail, and pairing an address with a
// location is more than the portal needs to answer "is something else using my
// account".
type Device struct {
	IP       string `json:"ip"`
	LastSeen int64  `json:"last_seen"`
}

func points(in []store.TrafficPoint) []TrafficPoint {
	out := make([]TrafficPoint, 0, len(in))
	for _, p := range in {
		out = append(out, TrafficPoint{At: p.At, UpBytes: p.UpBytes, DownBytes: p.DownBytes})
	}
	return out
}

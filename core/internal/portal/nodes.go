package portal

import (
	"sort"
	"time"
)

// What an end user may know about a node.
//
// A whitelist, and written by hand rather than derived from the admin's
// nodeView by removing fields. The failure directions are not symmetric: a
// blacklist that someone forgets to extend starts shipping a new field to
// every customer silently, while a whitelist that someone forgets to extend
// shows a blank in the UI. Only one of those is discovered by accident.
//
// This is the same discipline as template.Context.ForClient, which deletes
// private key components from the client render context rather than trusting
// templates not to reference them — one level up, with nodes instead of
// variables.
//
// Deliberately absent, with reasons, because "why is this not here" is the
// question a future change will ask:
//
//   - name — internal names encode provider and datacentre (bwh-lax-3), which
//     tells anyone who wants the node taken down exactly where to complain.
//   - public_ip / hostname — the user's own client already has the address
//     from their subscription, so republishing it adds nothing for them while
//     making "one account, one GET, the whole fleet's addresses" true.
//   - agent_version / xray_version — says which Xray build is running, hence
//     which detection signatures apply; the timing of changes leaks the
//     upgrade schedule too.
//   - cpu / memory / disk / throughput — capacity data the user cannot act on,
//     and node-wide throughput is a live side channel about other subscribers.
//   - config_version — an internal counter whose only reader is reconnect
//     reconciliation; publishing it announces "the config just changed".
//   - last_seen_at — an exact timestamp turns the portal into a feedback loop
//     for whoever is testing whether a block worked.
type Node struct {
	ID string `json:"id"`
	// Name is the customer-facing label, empty when the operator has not set
	// one. Never falls back to the internal name — see the migration.
	Name string `json:"name"`
	// Index is this node's position in the user's own list, 1-based, so the
	// frontend can label an unnamed node "Line 03" in the reader's language
	// and have it stay the same node between loads. The server does not invent
	// user-facing strings; it has no idea which language to invent them in.
	Index int `json:"index"`
	// Availability is a three-state summary rather than the raw booleans:
	// "online" is true while Xray is down, and xray_state's ERROR is operator
	// vocabulary that would turn one incident into a support queue.
	Availability string `json:"availability"`
	// Traffic24h is this user's own hourly usage on this node, so it carries
	// no information about anyone else.
	Traffic24h []TrafficPoint `json:"traffic_24h"`
}

// TrafficPoint is one hour of one user's usage on one node.
type TrafficPoint struct {
	At        int64 `json:"at"`
	UpBytes   int64 `json:"up_bytes"`
	DownBytes int64 `json:"down_bytes"`
}

// Availability values.
const (
	// AvailAvailable: the node is announced up and its kernel is running.
	AvailAvailable = "available"
	// AvailProvisioning: the user is entitled to it but no credential has been
	// minted yet, so connecting would fail. An honest third state rather than
	// showing a red dot for something that is merely not ready.
	AvailProvisioning = "provisioning"
	AvailUnavailable  = "unavailable"
)

// Nodes lists the access points this user is entitled to.
//
// The set is derived, never "all nodes filtered in the browser": it is the
// union of nodes bound to the user's profiles. Fetching the fleet and letting
// the client narrow it would put the whole roster one devtools tab away.
//
// This walks the same path subscription.Render does — profiles, then their
// nodes, then the credential for each pair — so what the portal shows and what
// the subscription contains cannot disagree.
func (v *View) Nodes() ([]Node, error) {
	profileIDs, err := v.data.UserProfileIDs(v.id.UserID)
	if err != nil {
		return nil, err
	}
	sort.Strings(profileIDs)

	to := v.now()
	from := to.Add(-24 * time.Hour)
	series, err := v.data.UserNodeTrafficSeries(v.id.UserID, from, to)
	if err != nil {
		return nil, err
	}

	// A node bound to two of the user's profiles is still one node to them.
	seen := map[string]bool{}
	// Non-nil so an unprovisioned account serialises as [] rather than null,
	// which the frontend would have to special-case on every render.
	out := []Node{}
	for _, pid := range profileIDs {
		nodeIDs, err := v.data.ProfileNodeIDs(pid)
		if err != nil {
			return nil, err
		}
		sort.Strings(nodeIDs)
		for _, nid := range nodeIDs {
			if seen[nid] {
				continue
			}
			seen[nid] = true

			n, err := v.data.GetNode(nid)
			if err != nil {
				// A node deleted between the two reads is not worth failing
				// the whole page for.
				continue
			}
			avail := AvailUnavailable
			if _, err := v.data.FindCredential(v.id.UserID, pid, nid, ""); err != nil {
				avail = AvailProvisioning
			} else if up, err := v.data.NodeAnnouncedOnline(nid); err == nil && up {
				// Announced up is only half of it. The agent stays connected
				// through a kernel that has died — that is the whole reason
				// this field is a three-state summary and not the `online`
				// boolean — so a node whose Xray is not running must not be
				// shown as available. XrayVersion is the RUNNING process's
				// version and is empty when nothing runs; an agent too old to
				// report it keeps the value Hello established, so this cannot
				// mark a working fleet unavailable.
				if n.XrayVersion != "" {
					avail = AvailAvailable
				}
			}
			out = append(out, Node{
				ID:           nid,
				Name:         n.DisplayName,
				Availability: avail,
				Traffic24h:   points(series[nid]),
			})
		}
	}

	// Assigned after ordering, so an unnamed node keeps the same number
	// between page loads instead of shuffling.
	for i := range out {
		out[i].Index = i + 1
	}
	return out, nil
}

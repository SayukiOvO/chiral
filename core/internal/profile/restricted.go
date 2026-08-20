package profile

import (
	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
)

// Restricted destinations: networks a node can reach that most subscribers
// must not (docs/restricted-destinations.md). This file answers two questions
// at assembly time: which destinations apply on this node, and who exactly is
// barred.

// blocksFor builds the BlockSources for one node.
//
// A destination applies here when the node is in its scope, or when this node
// is the ENTRY of an enabled relay line whose exit is. The second half is the
// leak this feature would otherwise have: relayed traffic reaches the exit
// under the line's machine credential, where users can no longer be told
// apart — so a barred user's restricted traffic must die at the entry, the
// last point where they are still themselves.
func (s *Service) blocksFor(nodeID string) ([]template.BlockSource, error) {
	dests, err := s.st.ListRestrictedDestinations()
	if err != nil {
		return nil, err
	}
	if len(dests) == 0 {
		return nil, nil
	}
	// The nodes this one can push traffic to, for the scope check: relay line
	// exits AND egress-rule landings. Both are ways for this node's traffic
	// to leave through another, and both arrive there under a machine
	// credential that no longer says who was asking — so a destination scoped
	// to the far end has to be enforced HERE, the last place a user is still
	// themselves. Missing the egress half was a real bypass: an egress rule
	// needs no relay row, so RelaysFromEntry alone saw nothing at all.
	relays, err := s.st.RelaysFromEntry(nodeID)
	if err != nil {
		return nil, err
	}
	exitOf := map[string]bool{}
	for _, rl := range relays {
		exitOf[rl.ExitNodeID] = true
	}
	egress, err := s.st.EgressRulesOn(nodeID)
	if err != nil {
		return nil, err
	}
	for _, r := range egress {
		if r.Enabled && r.TargetKind == store.EgressNode && r.TargetNodeID != "" {
			exitOf[r.TargetNodeID] = true
		}
	}

	// Every credential on this node, by owner. Assembly has already minted
	// this round's credentials by the time blocks are built, so the set is
	// current — and it includes relay credentials, which is what routes a
	// barred user's traffic into a line in the first place.
	creds, err := s.st.NodeCredentials(nodeID)
	if err != nil {
		return nil, err
	}
	users, err := s.st.ListUsers()
	if err != nil {
		return nil, err
	}

	var out []template.BlockSource
	for _, d := range dests {
		applies := false
		for _, nid := range d.NodeIDs {
			if nid == nodeID || exitOf[nid] {
				applies = true
			}
		}
		if !applies {
			continue
		}
		allowed := map[string]bool{}
		for _, uid := range d.AllowedUserIDs {
			allowed[uid] = true
		}
		// Barred = every subscriber not on the allow list. Their every
		// credential email on this node, because the rule matches emails and
		// one person holds one per access point × exit.
		var emails []string
		for _, u := range users {
			if allowed[u.ID] {
				continue
			}
			for _, c := range creds {
				if c.UserID == u.ID {
					emails = append(emails, c.Email)
				}
			}
		}
		if len(emails) == 0 {
			// Everyone is allowed; a block with an empty user list would
			// match EVERYONE instead, so it must not exist at all.
			continue
		}
		out = append(out, template.BlockSource{
			CIDRs: d.CIDRs, Domains: d.Domains, Emails: emails,
		})
	}
	return out, nil
}

// RestrictedNodeIDs lists every node whose config carries a given
// destination's rules — its scoped nodes plus the entries of relay lines
// landing on them. The API re-applies these after a change.
func (s *Service) RestrictedNodeIDs(destID string) ([]string, error) {
	d, err := s.st.GetRestrictedDestination(destID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, nid := range d.NodeIDs {
		add(nid)
	}
	relays, err := s.st.ListNodeRelays()
	if err != nil {
		return nil, err
	}
	for _, rl := range relays {
		if !rl.Enabled {
			continue
		}
		for _, nid := range d.NodeIDs {
			if rl.ExitNodeID == nid {
				add(rl.EntryNodeID)
			}
		}
	}
	// And the nodes whose egress rules land on a scoped node: they carry the
	// inherited block rules, so a policy change has to re-push them too.
	sources, err := s.st.EgressSourceNodesFor(d.NodeIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range sources {
		add(id)
	}
	return out, nil
}

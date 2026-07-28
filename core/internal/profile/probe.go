package profile

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/user"
)

// Rendering the client outbound that proves a node is actually carrying
// traffic.
//
// The agent's old post-upgrade check asked the kernel a question over its own
// API. That proves the process parsed its config and is listening; it proves
// nothing about whether a subscriber gets online. An upgrade that breaks a
// transport or a security layer — which is the failure a kernel upgrade
// actually causes — leaves the API answering cheerfully while every customer is
// dark, and the canary would have called that ACTIVE.
//
// So the agent needs to be a client. Building a client outbound from a server
// inbound is not a transformation anybody should write twice: it means
// re-deriving REALITY public keys, mirroring transport settings, and keeping up
// with every protocol the panel gains. The panel already has exactly one place
// that knows how to do it — the hand-written xray-json client template each
// profile carries, the same one a subscription is rendered from.
//
// Which is the property worth having: the probe dials what a customer dials,
// through the same template, with a real credential. If the probe gets online,
// a customer on that access point does too.

// clientKindXrayJSON is the client kind whose fragments are Xray outbound
// objects. Duplicated from package subscription rather than imported: that
// package depends on this one for render contexts, and the constant is not
// worth the cycle.
const clientKindXrayJSON = "xray-json"

// ProbeOutbound renders one client outbound for the node, or explains why it
// could not.
//
// The reason matters as much as the outbound. A node with no probe is not a
// node that passed — it is a node whose data path nobody checked, and the
// distinction has to survive all the way to the operator staring at a canary.
func (s *Service) ProbeOutbound(nodeID string) (string, string) {
	profileIDs, err := s.st.NodeProfileIDs(nodeID)
	if err != nil {
		return "", "could not read the node's profiles: " + err.Error()
	}
	if len(profileIDs) == 0 {
		return "", "no profile is bound to this node, so there is no access point to test"
	}
	sort.Strings(profileIDs)

	var why string
	for _, pid := range profileIDs {
		out, reason := s.probeFor(pid, nodeID)
		if out != "" {
			return out, ""
		}
		if why == "" {
			why = reason
		}
	}
	return "", why
}

// probeFor renders the outbound for one profile on one node.
func (s *Service) probeFor(profileID, nodeID string) (string, string) {
	templates, err := s.st.ClientTemplates(profileID)
	if err != nil {
		return "", "could not read the profile's client templates: " + err.Error()
	}
	tmpl := templates[clientKindXrayJSON]
	if tmpl == "" {
		return "", "this profile has no xray-json client template, which is the only kind the agent can run"
	}
	cred, reason := s.probeCredential(profileID, nodeID)
	if cred.ID == "" {
		return "", reason
	}
	ctx, err := s.ClientContext(profileID, nodeID)
	if err != nil {
		return "", "could not build the client render context: " + err.Error()
	}
	// ClientContext has already stripped secret components, so a template that
	// reaches for a private key fails here rather than sealing one into a blob
	// that gets shipped to a node.
	body, err := ctx.With(user.CredentialVars(cred)).Render(tmpl)
	if err != nil {
		return "", "rendering the client outbound failed: " + err.Error()
	}
	// It has to be one JSON object, because the agent wraps it in a config
	// without looking inside. A template that renders a comma-separated pair,
	// or YAML, or a share link, would produce a config the ephemeral kernel
	// rejects — and that rejection would read as "the data path is broken"
	// rather than "this template is not what we thought".
	var probe map[string]any
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return "", fmt.Sprintf("the xray-json template did not render a single JSON object: %v", err)
	}
	// Pin the tag. The agent's harness routes everything to it by name rather
	// than relying on it being the only outbound, and a template that already
	// names itself something else would break that silently.
	probe["tag"] = "probe-out"
	pinned, err := json.Marshal(probe)
	if err != nil {
		return "", "re-encoding the client outbound failed: " + err.Error()
	}
	return string(pinned), ""
}

// probeCredential picks the identity the probe connects as.
//
// A real subscriber's credential, not a synthetic one. The alternative —
// minting a probe account and rendering it into every inbound — means a
// credential that exists on every node in the fleet, forever, for the sake of a
// check that runs during upgrades. Reusing one that is already there adds no
// exposure at all: the agent wrote that credential into config.json itself and
// has held it the whole time.
//
// The cost is a couple of hundred bytes billed to whoever is picked, during an
// upgrade. The alternative costs a permanent fleet-wide key.
//
// Deterministic by credential id so repeated renders of an unchanged node
// produce an unchanged probe, and the same subscriber is picked each time
// rather than the charge wandering around the user base.
func (s *Service) probeCredential(profileID, nodeID string) (store.Credential, string) {
	all, err := s.st.NodeCredentials(nodeID)
	if err != nil {
		return store.Credential{}, "could not read this access point's credentials: " + err.Error()
	}
	var creds []store.Credential
	for _, c := range all {
		if c.ProfileID == profileID {
			creds = append(creds, c)
		}
	}
	if len(creds) == 0 {
		return store.Credential{}, "no user is entitled to this access point, so there is no client to test as"
	}
	sort.Slice(creds, func(i, j int) bool { return creds[i].ID < creds[j].ID })
	for _, c := range creds {
		u, err := s.st.GetUser(c.UserID)
		if err != nil || !u.Enabled {
			continue
		}
		return c, ""
	}
	return store.Credential{}, "every user on this access point is disabled, so there is no client to test as"
}

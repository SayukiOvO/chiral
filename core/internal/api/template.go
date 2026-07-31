package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/SayukiOvO/chiral/core/internal/store"
	"github.com/SayukiOvO/chiral/core/internal/template"
)

// routeTemplates registers the profile / variable / config-assembly routes.
func (s *Server) routeTemplates(mux *http.ServeMux) {
	mux.Handle("GET /api/generators", s.requireAdmin(s.listGenerators))

	mux.Handle("GET /api/variables", s.requireAdmin(s.listVariables))
	mux.Handle("POST /api/variables", s.requireWrite(s.createVariable))
	mux.Handle("DELETE /api/variables/{id}", s.requireWrite(s.deleteVariable))

	mux.Handle("GET /api/profiles", s.requireAdmin(s.listProfiles))
	mux.Handle("POST /api/profiles", s.requireWrite(s.createProfile))
	mux.Handle("GET /api/profiles/{id}", s.requireAdmin(s.getProfile))
	mux.Handle("PUT /api/profiles/{id}", s.requireWrite(s.updateProfile))
	mux.Handle("DELETE /api/profiles/{id}", s.requireWrite(s.deleteProfile))
	mux.Handle("PUT /api/profiles/{id}/clients/{client}", s.requireWrite(s.putClientTemplate))
	mux.Handle("DELETE /api/profiles/{id}/clients/{client}", s.requireWrite(s.deleteClientTemplate))
	mux.Handle("POST /api/profiles/{id}/nodes/{nodeId}", s.requireWrite(s.bindNode))
	mux.Handle("DELETE /api/profiles/{id}/nodes/{nodeId}", s.requireWrite(s.unbindNode))
	mux.Handle("POST /api/profiles/{id}/apply", s.requireWrite(s.applyProfile))

	mux.Handle("PUT /api/nodes/{id}/skeleton", s.requireWrite(s.putSkeleton))
	// Write, not read. The preview is the fully assembled config.json: every
	// REALITY private key and every user's credential in the clear. Reading it
	// is not a lesser act than applying it — it is strictly more revealing —
	// so it cannot sit at the viewer level the way the other GETs do.
	// listVariables can, because it masks secret components on the way out.
	mux.Handle("GET /api/nodes/{id}/config/preview", s.requireWrite(s.previewNodeConfig))
	mux.Handle("POST /api/nodes/{id}/config/apply", s.requireWrite(s.applyNodeConfig))
	// The history is metadata only — no config bodies, which are the thing
	// preview is guarded for.
	mux.Handle("GET /api/nodes/{id}/config/versions", s.requireAdmin(s.nodeConfigVersions))
	mux.Handle("POST /api/nodes/{id}/config/rollback", s.requireWrite(s.rollbackNodeConfig))
}

// --- variables ---

type componentView struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}

type variableView struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Scope      string          `json:"scope"`
	ProfileID  string          `json:"profile_id,omitempty"`
	NodeID     string          `json:"node_id,omitempty"`
	Generator  string          `json:"generator,omitempty"`
	Components []componentView `json:"components"`
}

// secretMask stands in for a secret component's value. Private key material
// exists to be rendered into configs, not read in a browser, so the API never
// serves it — one less place for it to leak into a log or a screenshot.
const secretMask = "••••••••"

func toVariableView(v store.Variable) variableView {
	out := variableView{
		ID: v.ID, Name: v.Name, Scope: v.Scope,
		ProfileID: v.ProfileID.String, NodeID: v.NodeID.String, Generator: v.Generator,
	}
	for _, c := range v.Components {
		value := c.Value
		if c.Secret {
			value = secretMask
		}
		out.Components = append(out.Components, componentView{Name: c.Name, Value: value, Secret: c.Secret})
	}
	return out
}

// listVariables returns the whole pool by default, or one scope when filtered
// with ?scope=global, ?profile_id=… or ?node_id=….
func (s *Server) listVariables(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var (
		vars []store.Variable
		err  error
	)
	switch {
	case q.Get("profile_id") != "":
		vars, err = s.st.ProfileVariables(q.Get("profile_id"))
	case q.Get("node_id") != "":
		vars, err = s.st.NodeVariables(q.Get("node_id"))
	case q.Get("scope") == "global":
		vars, err = s.st.GlobalVariables()
	default:
		vars, err = s.st.AllVariables()
	}
	if err != nil {
		s.internalErr(w, "list variables", err)
		return
	}
	views := make([]variableView, 0, len(vars))
	for _, v := range vars {
		views = append(views, toVariableView(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"variables": views})
}

func (s *Server) createVariable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		Scope     string `json:"scope"`
		ProfileID string `json:"profile_id"`
		NodeID    string `json:"node_id"`
		// Generator produces a key group; leave empty and set Value for a
		// plain static variable.
		Generator string `json:"generator"`
		Value     string `json:"value"`
		// Import carries an existing private half for Generator instead of
		// making a new one — adopting this panel should not invalidate the
		// client configurations a server already handed out. Only the private
		// component is taken; the public half is derived here, so an imported
		// group cannot be internally inconsistent.
		Import string `json:"import"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !template.IsValidName(req.Name) {
		writeErr(w, http.StatusBadRequest, "name must be non-empty and use only letters, digits, _ and .")
		return
	}
	if req.Scope == "" {
		req.Scope = store.ScopeGlobal
	}
	profileID := nullIf(req.ProfileID)
	nodeID := nullIf(req.NodeID)

	var (
		v   store.Variable
		err error
	)
	switch {
	case req.Generator != "" && req.Import != "":
		v, err = s.profiles.ImportVariable(req.Name, req.Scope, profileID, nodeID,
			template.Generator(req.Generator), req.Import)
	case req.Generator != "":
		v, err = s.profiles.GenerateVariable(req.Name, req.Scope, profileID, nodeID, template.Generator(req.Generator))
	case req.Import != "":
		writeErr(w, http.StatusBadRequest, `"import" needs a "generator" saying what kind of key it is`)
		return
	default:
		// Reuse the existing row when this name is already taken in this
		// scope, so setting a value is one call whether or not it is the
		// first.
		//
		// Without this, changing a port or an SNI — the two most routine
		// edits an operator makes — meant deleting the variable and creating
		// it again, and between those two calls the profile does not render.
		// The database's own key is (scope, owner, name), so writing to that
		// key rather than to a surrogate id is also the only version of this
		// that cannot silently create a duplicate.
		v = store.Variable{
			Name: req.Name, Scope: req.Scope, ProfileID: profileID, NodeID: nodeID,
			Components: []store.Component{{Value: req.Value}},
		}
		if id, ferr := s.st.FindVariableID(req.Scope, req.ProfileID, req.NodeID, req.Name); ferr == nil {
			v.ID = id
		}
		v, err = s.st.PutVariable(v)
	}
	if err != nil {
		// Constraint violations and unknown generators are the caller's
		// mistake, not ours.
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toVariableView(v))
}

func (s *Server) deleteVariable(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteVariable(r.PathValue("id")); err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such variable")
			return
		}
		s.internalErr(w, "delete variable", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listGenerators(w http.ResponseWriter, r *http.Request) {
	type gen struct {
		Name      string `json:"name"`
		NeedsXray bool   `json:"needs_xray"`
		Available bool   `json:"available"`
	}
	out := make([]gen, 0)
	for _, g := range template.Generators() {
		needs := template.NeedsXray(g)
		out = append(out, gen{Name: string(g), NeedsXray: needs, Available: !needs || s.xrayAvailable})
	}
	writeJSON(w, http.StatusOK, map[string]any{"generators": out})
}

// --- profiles ---

type profileView struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	InboundTemplate string            `json:"inbound_template"`
	ClientEntry     string            `json:"client_entry"`
	ClientTemplates map[string]string `json:"client_templates,omitempty"`
	// ClientKinds lists which clients have a template, so the profile list can
	// show coverage without shipping every template body.
	ClientKinds []string `json:"client_kinds"`
	NodeIDs     []string `json:"node_ids"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

func (s *Server) profileView(p store.Profile, withTemplates bool) (profileView, error) {
	v := profileView{
		ID: p.ID, Name: p.Name, InboundTemplate: p.InboundTemplate, ClientEntry: p.ClientEntry,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, NodeIDs: []string{}, ClientKinds: []string{},
	}
	ids, err := s.st.ProfileNodeIDs(p.ID)
	if err != nil {
		return v, err
	}
	if ids != nil {
		v.NodeIDs = ids
	}
	kinds, err := s.st.ClientTemplateKinds(p.ID)
	if err != nil {
		return v, err
	}
	v.ClientKinds = kinds
	if withTemplates {
		t, err := s.st.ClientTemplates(p.ID)
		if err != nil {
			return v, err
		}
		v.ClientTemplates = t
	}
	return v, nil
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.st.ListProfiles()
	if err != nil {
		s.internalErr(w, "list profiles", err)
		return
	}
	views := make([]profileView, 0, len(ps))
	for _, p := range ps {
		v, err := s.profileView(p, false)
		if err != nil {
			s.internalErr(w, "list profiles", err)
			return
		}
		views = append(views, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": views})
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, `body must be JSON with a non-empty "name"`)
		return
	}
	p, err := s.st.CreateProfile(strings.TrimSpace(req.Name))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v, _ := s.profileView(p, true)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetProfile(r.PathValue("id"))
	if err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such profile")
			return
		}
		s.internalErr(w, "get profile", err)
		return
	}
	v, err := s.profileView(p, true)
	if err != nil {
		s.internalErr(w, "get profile", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.st.GetProfile(r.PathValue("id"))
	if err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such profile")
			return
		}
		s.internalErr(w, "get profile", err)
		return
	}
	var req struct {
		Name            *string `json:"name"`
		InboundTemplate *string `json:"inbound_template"`
		ClientEntry     *string `json:"client_entry"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	if req.Name != nil {
		p.Name = strings.TrimSpace(*req.Name)
	}
	if req.InboundTemplate != nil {
		p.InboundTemplate = *req.InboundTemplate
	}
	if req.ClientEntry != nil {
		p.ClientEntry = *req.ClientEntry
	}
	if err := s.st.UpdateProfile(p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v, _ := s.profileView(p, true)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteProfile(r.PathValue("id")); err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such profile")
			return
		}
		s.internalErr(w, "delete profile", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putClientTemplate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Template string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be JSON")
		return
	}
	// Validated before it is stored, against the same client context a
	// subscription renders with.
	//
	// Without this the first sign of a template reaching for a private key was
	// a subscriber's client failing to update — the operator who wrote it saw
	// a clean save, and the person who paid for the service saw nothing at all.
	// template.Context.Validate exists for exactly this and had no caller
	// outside its own tests.
	//
	// Node-scoped variables are deliberately not resolvable here: a client
	// template is written once and rendered against every bound node, so it is
	// validated against what all of them share. A reference only some nodes
	// define is a template that only sometimes works, and it is caught at
	// preview and at render.
	if reasons := s.validateClientTemplate(r.PathValue("id"), req.Template); len(reasons) > 0 {
		writeErr(w, http.StatusBadRequest, strings.Join(reasons, "; "))
		return
	}
	if err := s.st.PutClientTemplate(r.PathValue("id"), r.PathValue("client"), req.Template); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validateClientTemplate reports why a client template cannot be rendered, or
// nothing when it can.
//
// Only the checks that hold for EVERY node: a secret reference is always wrong
// in a client template, whereas an unresolved name may simply be node-scoped.
// Being strict about the second here would refuse templates that are correct.
func (s *Server) validateClientTemplate(profileID, tmpl string) []string {
	if s.profiles == nil {
		return nil
	}
	nodeIDs, err := s.st.ProfileNodeIDs(profileID)
	if err != nil || len(nodeIDs) == 0 {
		// Nothing bound yet, so there is no context to check against. Preview
		// and render still catch it later.
		return nil
	}
	ctx, err := s.profiles.ClientContext(profileID, nodeIDs[0])
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ctx.Validate(tmpl) {
		if strings.Contains(e.Error(), "secret variable") {
			out = append(out, e.Error())
		}
	}
	return out
}

func (s *Server) deleteClientTemplate(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteClientTemplate(r.PathValue("id"), r.PathValue("client")); err != nil {
		s.internalErr(w, "delete client template", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) bindNode(w http.ResponseWriter, r *http.Request) {
	if err := s.st.BindProfileNode(r.PathValue("id"), r.PathValue("nodeId")); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unbindNode(w http.ResponseWriter, r *http.Request) {
	if err := s.st.UnbindProfileNode(r.PathValue("id"), r.PathValue("nodeId")); err != nil {
		s.internalErr(w, "unbind node", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyProfile re-assembles and pushes every node bound to the profile —
// what you want after editing its template or variables.
func (s *Server) applyProfile(w http.ResponseWriter, r *http.Request) {
	results, err := s.profiles.ApplyBoundNodes(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internalErr(w, "apply profile", err)
		return
	}
	out := make(map[string]string, len(results))
	failed := 0
	for nodeID, err := range results {
		if err != nil {
			out[nodeID] = err.Error()
			failed++
		} else {
			out[nodeID] = "ok"
		}
	}
	s.audit(r, "profile.apply", "profile", r.PathValue("id"), "",
		fmt.Sprintf("%d applied, %d failed", len(results)-failed, failed))
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes": out, "applied": len(results) - failed, "failed": failed,
	})
}

// --- node config assembly ---

func (s *Server) putSkeleton(w http.ResponseWriter, r *http.Request) {
	// Decode into a map, not RawMessage: `null` is valid JSON but not a valid
	// skeleton, and storing it would break every later assembly for this node.
	var skeleton map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&skeleton); err != nil {
		writeErr(w, http.StatusBadRequest, "body must be the config skeleton as a JSON object")
		return
	}
	if skeleton == nil {
		writeErr(w, http.StatusBadRequest, "config skeleton must be a JSON object, not null")
		return
	}
	raw, err := json.Marshal(skeleton)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "config skeleton could not be re-encoded")
		return
	}
	if err := s.st.SetConfigSkeleton(r.PathValue("id"), string(raw)); err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		s.internalErr(w, "set skeleton", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) previewNodeConfig(w http.ResponseWriter, r *http.Request) {
	pv, err := s.profiles.Preview(r.Context(), r.PathValue("id"))
	if err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		// Assembly failures are template or variable mistakes: report them to
		// the operator verbatim, they are what needs fixing.
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config":       json.RawMessage(pv.Config),
		"inbound_tags": pv.InboundTags,
		"tested":       pv.Tested,
		"test_error":   pv.TestError,
		// Which kernel judged it. "Passed" means something weaker when the
		// panel does not have the node's own build, and the operator has to be
		// able to see which of the two they were told.
		"kernel_version": pv.KernelVersion,
		"kernel_exact":   pv.KernelExact,
		"kernel_note":    pv.KernelNote,
		"advisories":     pv.Advisories,
	})
}

func (s *Server) nodeConfigVersions(w http.ResponseWriter, r *http.Request) {
	// A node that does not exist has no history, which is not the same answer as
	// "this node has never had a config applied" — and 200 with an empty list
	// says the second while meaning the first. A typo in an id would read as a
	// node that simply has no versions yet.
	if _, err := s.st.GetNode(r.PathValue("id")); err != nil {
		s.notFoundOr(w, "load node", err, "no such node")
		return
	}
	versions, err := s.st.ConfigVersions(r.PathValue("id"), 20)
	if err != nil {
		s.internalErr(w, "list config versions", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"versions": versions,
		"depth":    store.ConfigHistoryDepth,
	})
}

// rollbackNodeConfig re-pushes an older version as a new one.
func (s *Server) rollbackNodeConfig(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("id")
	var req struct {
		Version int64 `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version <= 0 {
		writeErr(w, http.StatusBadRequest, `body must be JSON with a "version"`)
		return
	}
	version, err := s.profiles.Rollback(r.Context(), nodeID, req.Version)
	if err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node or version")
			return
		}
		// A version that no longer passes xray -test is the operator's to see
		// verbatim; it is usually a kernel upgrade, not a panel bug.
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.audit(r, "node.rollback", "node", nodeID, "", fmt.Sprintf("%d -> %d", req.Version, version))
	writeJSON(w, http.StatusOK, map[string]any{"version": version})
}

func (s *Server) applyNodeConfig(w http.ResponseWriter, r *http.Request) {
	version, err := s.profiles.Apply(r.Context(), r.PathValue("id"))
	if err != nil {
		if store.IsNotFound(err) {
			writeErr(w, http.StatusNotFound, "no such node")
			return
		}
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// Audited for the same reason a rollback is: this writes a new config
	// version and pushes it to a live node. Rolling one back was in the trail
	// and putting one there was not, which is the wrong half — a rollback is
	// recoverable by definition, and an apply is what changed the node.
	s.audit(r, "node.config_apply", "node", r.PathValue("id"), "", fmt.Sprintf("version %d", version))
	writeJSON(w, http.StatusAccepted, map[string]any{"version": version})
}

func nullIf(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

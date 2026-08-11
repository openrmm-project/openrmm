package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
)

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	var policies []models.Policy
	var err error
	if uid := s.scopeUserID(r); uid != "" {
		policies, err = s.store.ListPoliciesForUser(r.Context(), uid)
	} else {
		policies, err = s.store.ListPolicies(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": policies})
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "scoped users cannot create global policy definitions")
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decode(r, &in); err != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	p, err := s.store.CreatePolicy(r.Context(), in.Name, in.Description)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// updatePolicy renames a policy and rewrites its description.
//
// Both were write-once: the console could create a policy and delete it, and
// nothing in between, so a typo in the name of a policy assigned across the
// fleet could only be fixed by rebuilding it — losing its checks, tasks and
// assignments on the way. The same manage gate as delete, because renaming what
// a scope is assigned to is a change to what operators think they are applying.
func (s *Server) updatePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePolicy(w, r, id, true) {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	p, err := s.store.UpdatePolicy(r.Context(), id, strings.TrimSpace(in.Name), in.Description)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "policy.update", id, auditLabel("name", p.Name))
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePolicy(w, r, id, true) {
		return
	}
	if err := s.store.DeletePolicy(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) listPolicyAssignments(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "id")
	if !s.requirePolicy(w, r, policyID, false) {
		return
	}
	var as []models.PolicyAssignment
	var err error
	if uid := s.scopeUserID(r); uid != "" {
		as, err = s.store.ListPolicyAssignmentsForUser(r.Context(), uid, policyID)
	} else {
		as, err = s.store.ListPolicyAssignments(r.Context(), policyID)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": as})
}

func (s *Server) createPolicyAssignment(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "id")
	if !s.requirePolicy(w, r, policyID, true) {
		return
	}
	var in struct {
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
	}
	if err := decode(r, &in); err != nil || in.ScopeType == "" || in.ScopeID == "" {
		writeErr(w, http.StatusBadRequest, "scope_type and scope_id required")
		return
	}
	if !oneOf(in.ScopeType, models.ScopeClient, models.ScopeSite, models.ScopeAgent) {
		writeErr(w, http.StatusBadRequest, "invalid policy scope_type")
		return
	}
	if !s.requireScope(w, r, in.ScopeType, in.ScopeID) {
		return
	}
	a, err := s.store.AssignPolicy(r.Context(), policyID, in.ScopeType, in.ScopeID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) deletePolicyAssignment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "aid")
	a, err := s.store.GetPolicyAssignment(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if !s.requirePolicy(w, r, a.PolicyID, true) || !s.requireScope(w, r, a.ScopeType, a.ScopeID) {
		return
	}
	if err := s.store.DeleteAssignment(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

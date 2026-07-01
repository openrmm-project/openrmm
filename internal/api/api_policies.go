package api

import (
	"net/http"

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

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePolicy(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) listPolicyAssignments(w http.ResponseWriter, r *http.Request) {
	var as []models.PolicyAssignment
	var err error
	if uid := s.scopeUserID(r); uid != "" {
		as, err = s.store.ListPolicyAssignmentsForUser(r.Context(), uid, chi.URLParam(r, "id"))
	} else {
		as, err = s.store.ListPolicyAssignments(r.Context(), chi.URLParam(r, "id"))
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": as})
}

func (s *Server) createPolicyAssignment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
	}
	if err := decode(r, &in); err != nil || in.ScopeType == "" || in.ScopeID == "" {
		writeErr(w, http.StatusBadRequest, "scope_type and scope_id required")
		return
	}
	a, err := s.store.AssignPolicy(r.Context(), chi.URLParam(r, "id"), in.ScopeType, in.ScopeID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) deletePolicyAssignment(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAssignment(r.Context(), chi.URLParam(r, "aid")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

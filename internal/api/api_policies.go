package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
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
	p, err := s.store.CreatePolicy(r.Context(), in.Name, in.Description, "")
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
	var in assignmentTarget
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "scope_type and scope_id required")
		return
	}
	if !s.requireAssignmentTarget(w, r, &in, models.ScopeClient, models.ScopeSite, models.ScopeGroup, models.ScopeAgent) {
		return
	}
	a, err := s.store.AssignPolicy(r.Context(), policyID, in.ScopeType, in.ScopeID, in.OS, in.Role)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := ""
	if p, err := s.store.GetPolicy(r.Context(), policyID); err == nil {
		name = p.Name
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "policy.assign", policyID, targetAuditDetail(name, in))
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) deletePolicyAssignment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "aid")
	a, err := s.store.GetPolicyAssignment(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if !s.requirePolicy(w, r, a.PolicyID, true) || !s.requireAssignmentScope(w, r, a.ScopeType, a.ScopeID) {
		return
	}
	if err := s.store.DeleteAssignment(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "policy.unassign", a.PolicyID,
		targetAuditDetail("", assignmentTarget{ScopeType: a.ScopeType, ScopeID: a.ScopeID, OS: a.OS, Role: a.Role}))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// reviewPolicy returns everything switching the policy on puts into effect —
// scopes, tasks, checks, and which of them are AI proposals — sealed with the
// fingerprint the activation must send back.
func (s *Server) reviewPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePolicy(w, r, id, false) {
		return
	}
	review, err := s.store.PolicyReview(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	// The review shows what the caller may read and nothing more: the task and
	// check listings need tasks.view and checks.view, so does this. A review
	// with a section left out cannot seal what was not shown, so it carries no
	// fingerprint and cannot be used to activate.
	if !hasPermission(r, models.PermTasksView) {
		review.Tasks, review.Fingerprint = []models.Task{}, ""
	}
	if !hasPermission(r, models.PermChecksView) {
		review.Checks, review.Fingerprint = []models.Check{}, ""
	}
	// A restricted principal sees the scopes it controls, as the assignment
	// listing shows them, never the other clients the policy reaches. It cannot
	// activate a global policy either, so it gets no fingerprint to send back.
	if uid := s.scopeUserID(r); uid != "" {
		if review.Assignments, err = s.store.ListPolicyAssignmentsForUser(r.Context(), uid, id); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if review.Assignments == nil {
			review.Assignments = []models.PolicyAssignment{}
		}
		visible := review.Exclusions[:0]
		for _, e := range review.Exclusions {
			if s.agentAllowed(r, e.AgentID) {
				visible = append(visible, e)
			}
		}
		review.Exclusions = visible
		review.Fingerprint = ""
	}
	writeJSON(w, http.StatusOK, review)
}

// activatePolicy switches a policy on and approves every AI proposal it carries,
// in one step, provided it still matches the review the person read.
//
// Switching a policy on puts its enabled tasks and checks into effect, which is
// what assigning it already did, so it takes the same permission. Approving an
// AI-proposed task or check is a separate decision with its own permission, and
// the activation refuses to make it for someone who could not make it alone.
func (s *Server) activatePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePolicy(w, r, id, true) {
		return
	}
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Fingerprint) == "" {
		writeErr(w, http.StatusBadRequest, "fingerprint of the reviewed policy required")
		return
	}
	// Nobody switches on what they cannot read: the review a fingerprint seals
	// shows the policy's tasks and checks in full.
	if !hasPermission(r, models.PermTasksView) || !hasPermission(r, models.PermChecksView) {
		writeErr(w, http.StatusForbidden, "activating a policy needs the tasks.view and checks.view permissions to review what it switches on")
		return
	}
	out, err := s.store.ActivatePolicy(r.Context(), id, actorOf(r), in.Fingerprint, store.ActivationRights{
		ApproveTasks: hasPermission(r, models.PermTasksManage), ApproveChecks: hasPermission(r, models.PermChecksManage)})
	if errors.Is(err, store.ErrApprovalNotPermitted) {
		writeErr(w, http.StatusForbidden, err.Error())
		return
	}
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": out.Policy.Name, "approved_policy": out.ApprovedPolicy,
		"approved_tasks": out.ApprovedTasks, "approved_checks": out.ApprovedChecks})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "policy.activate", id, detail)
	writeJSON(w, http.StatusOK, out)
}

// deactivatePolicy switches a policy off: it stops applying everywhere at once,
// and its tasks and checks keep their own flags for when it is switched on again.
func (s *Server) deactivatePolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePolicy(w, r, id, true) {
		return
	}
	p, err := s.store.DeactivatePolicy(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "policy.deactivate", id, auditLabel("name", p.Name))
	writeJSON(w, http.StatusOK, p)
}

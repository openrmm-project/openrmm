package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/store"
)

// ---- per-device (scope-checked via requireAgent) ----

func (s *Server) listDevicePatches(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	ps, err := s.store.ListAgentPatches(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patches": ps})
}

func (s *Server) scanDevicePatches(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	if !s.patches.DispatchScan(id) {
		writeErr(w, http.StatusServiceUnavailable, "device is offline")
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.scan", id, s.deviceAuditLabel(r, id))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "scanning"})
}

func (s *Server) installDevicePatches(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	var in struct {
		Identifiers []string `json:"identifiers"`
	}
	_ = decode(r, &in)
	if err := s.patches.DispatchInstall(r.Context(), id, in.Identifiers); err != nil {
		if errors.Is(err, patches.ErrOffline) {
			writeErr(w, http.StatusServiceUnavailable, "device is offline")
			return
		}
		if errors.Is(err, patches.ErrNoApproved) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.install", id, s.deviceAuditLabel(r, id))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "installing"})
}

func (s *Server) rebootDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	var in struct {
		Force bool `json:"force"`
	}
	_ = decode(r, &in)
	if !in.Force {
		live, err := s.store.HasLiveNativeRemoteSession(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if live {
			writeErr(w, http.StatusConflict, "an active remote session is using this device; end it first or retry with force=true")
			return
		}
	}
	if err := s.patches.DispatchReboot(r.Context(), id, 60); err != nil {
		if errors.Is(err, patches.ErrOffline) {
			writeErr(w, http.StatusServiceUnavailable, "device is offline")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "reboot", id, s.deviceAuditLabel(r, id))
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "rebooting"})
}

// ---- catalog + approval ----

func (s *Server) listPatchCatalog(w http.ResponseWriter, r *http.Request) {
	f, ok := s.checkedReportFilter(w, r)
	if !ok {
		return
	}
	rows, err := s.store.PatchCatalog(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patches": rows})
}

func (s *Server) approvePatch(w http.ResponseWriter, r *http.Request) {
	patchID := chi.URLParam(r, "id")
	patchVisible, err := s.store.PatchAllowedForUser(r.Context(), s.scopeUserID(r), patchID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !patchVisible {
		writeErr(w, http.StatusForbidden, "patch is outside your access scope")
		return
	}
	var in struct {
		ScopeType string `json:"scope_type"` // ""=global | client|site|agent
		ScopeID   string `json:"scope_id"`
		Decision  string `json:"decision"` // approve|decline
	}
	_ = decode(r, &in)
	if in.Decision != "decline" {
		in.Decision = "approve"
	}
	switch in.ScopeType {
	case "", "client", "site", "agent":
	default:
		writeErr(w, http.StatusBadRequest, "invalid scope_type")
		return
	}
	// A restricted principal may not approve fleet-wide.
	if s.scopeUserID(r) != "" && in.ScopeType == "" {
		writeErr(w, http.StatusForbidden, "scoped users must approve within a client/site/device")
		return
	}
	if in.ScopeType != "" {
		if in.ScopeID == "" {
			writeErr(w, http.StatusBadRequest, "scope_id is required")
			return
		}
		if !s.requireScope(w, r, in.ScopeType, in.ScopeID) {
			return
		}
	} else if in.ScopeID != "" {
		writeErr(w, http.StatusBadRequest, "scope_id requires scope_type")
		return
	}
	if err := s.store.SetApproval(r.Context(), patchID, in.ScopeType, in.ScopeID, in.Decision, "manual", actorOf(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch."+in.Decision, patchID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) reportCompliance(w http.ResponseWriter, r *http.Request) {
	f, ok := s.checkedReportFilter(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ComplianceReport(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// ---- patch policies (admin) ----

func (s *Server) listPatchPolicies(w http.ResponseWriter, r *http.Request) {
	pols, err := s.store.ListPatchPolicies(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if uid := s.scopeUserID(r); uid != "" {
		filtered := pols[:0]
		for _, policy := range pols {
			ok, err := s.store.PatchPolicyAllowedForUser(r.Context(), uid, policy.ID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if ok {
				filtered = append(filtered, policy)
			}
		}
		pols = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": pols})
}

func (s *Server) requirePatchPolicy(w http.ResponseWriter, r *http.Request, policyID string, manage bool) bool {
	uid := s.scopeUserID(r)
	if manage && uid != "" {
		writeErr(w, http.StatusForbidden, "restricted principals cannot mutate global patch policies")
		return false
	}
	if uid == "" {
		ok, err := s.store.PatchPolicyExists(r.Context(), policyID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return false
		}
		if !ok {
			writeErr(w, http.StatusNotFound, "patch policy not found")
			return false
		}
		return true
	}
	var ok bool
	var err error
	if manage {
		ok, err = s.store.PatchPolicyManageAllowedForUser(r.Context(), uid, policyID)
	} else {
		ok, err = s.store.PatchPolicyAllowedForUser(r.Context(), uid, policyID)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return false
	}
	if !ok {
		writeErr(w, http.StatusForbidden, "patch policy is outside your access scope")
		return false
	}
	return true
}

func (s *Server) createPatchPolicy(w http.ResponseWriter, r *http.Request) {
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "scoped users cannot create global patch policy definitions")
		return
	}
	var p models.PatchPolicy
	if err := decode(r, &p); err != nil || p.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	// Provenance is the server's to record, never the client's to claim.
	p.ProposedBy, p.ApprovedBy = "", ""
	tz, err := models.NormalizeTimezone(p.Timezone)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p.Timezone = tz
	if err := models.ValidatePatchPolicy(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.CreatePatchPolicy(r.Context(), &p)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.create", out.ID, nil)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updatePatchPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePatchPolicy(w, r, id, true) {
		return
	}
	var p models.PatchPolicy
	if err := decode(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	p.ID = id
	tz, err := models.NormalizeTimezone(p.Timezone)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	p.Timezone = tz
	if err := models.ValidatePatchPolicy(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := s.store.GetPatchPolicy(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	// An AI proposal is switched on only through its review, so the person
	// sees the scopes and settings the click puts into effect.
	if existing.Pending && p.Enabled {
		writeErr(w, http.StatusConflict, "this patch policy is an AI proposal: review and activate it to switch it on")
		return
	}
	if err := s.store.UpdatePatchPolicy(r.Context(), &p); err != nil {
		if store.IsProposalUnreviewed(err) {
			writeErr(w, http.StatusConflict, "this patch policy is an AI proposal: review and activate it to switch it on")
			return
		}
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.update", p.ID, nil)
	updated, err := s.store.GetPatchPolicy(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// reviewPatchPolicy returns a patch policy's settings and scopes, sealed with
// the fingerprint its activation must send back.
func (s *Server) reviewPatchPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePatchPolicy(w, r, id, false) {
		return
	}
	review, err := s.store.PatchPolicyReview(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	// As in the assignment listing: a restricted principal sees only the scopes
	// that intersect its own, and cannot activate a global definition.
	if uid := s.scopeUserID(r); uid != "" {
		visible := []models.PatchPolicyAssignment{}
		for _, a := range review.Assignments {
			ok, err := s.store.ScopeIntersectsUser(r.Context(), uid, a.ScopeType, a.ScopeID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if ok {
				visible = append(visible, a)
			}
		}
		review.Assignments, review.Fingerprint = visible, ""
		excluded := review.Exclusions[:0]
		for _, e := range review.Exclusions {
			if s.agentAllowed(r, e.AgentID) {
				excluded = append(excluded, e)
			}
		}
		review.Exclusions = excluded
	}
	writeJSON(w, http.StatusOK, review)
}

// activatePatchPolicy switches a patch policy on — approving it when AI Chat or
// MCP proposed it — provided it still matches the review the person read.
func (s *Server) activatePatchPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePatchPolicy(w, r, id, true) {
		return
	}
	var in struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Fingerprint) == "" {
		writeErr(w, http.StatusBadRequest, "fingerprint of the reviewed patch policy required")
		return
	}
	p, approved, err := s.store.ActivatePatchPolicy(r.Context(), id, actorOf(r), in.Fingerprint)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": p.Name, "approved_proposal": approved})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.activate", id, detail)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deletePatchPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requirePatchPolicy(w, r, id, true) {
		return
	}
	if err := s.store.DeletePatchPolicy(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listPatchPolicyAssignments(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "id")
	if !s.requirePatchPolicy(w, r, policyID, false) {
		return
	}
	a, err := s.store.ListPatchPolicyAssignments(r.Context(), policyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if uid := s.scopeUserID(r); uid != "" {
		filtered := a[:0]
		for _, assignment := range a {
			ok, err := s.store.ScopeIntersectsUser(r.Context(), uid, assignment.ScopeType, assignment.ScopeID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			if ok {
				filtered = append(filtered, assignment)
			}
		}
		a = filtered
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": a})
}

func (s *Server) addPatchPolicyAssignment(w http.ResponseWriter, r *http.Request) {
	policyID := chi.URLParam(r, "id")
	if !s.requirePatchPolicy(w, r, policyID, true) {
		return
	}
	var in assignmentTarget
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "scope_type and scope_id are required")
		return
	}
	if !s.requireAssignmentTarget(w, r, &in, models.ScopeClient, models.ScopeSite, models.ScopeAgent) {
		return
	}
	if err := s.store.AddPatchPolicyAssignment(r.Context(), policyID, in.ScopeType, in.ScopeID, in.OS, in.Role); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := ""
	if p, err := s.store.GetPatchPolicy(r.Context(), policyID); err == nil {
		name = p.Name
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.assign", policyID, targetAuditDetail(name, in))
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) deletePatchPolicyAssignment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "aid")
	a, err := s.store.GetPatchPolicyAssignment(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if !s.requirePatchPolicy(w, r, a.PolicyID, true) || !s.requireAssignmentScope(w, r, a.ScopeType, a.ScopeID) {
		return
	}
	if err := s.store.DeletePatchPolicyAssignment(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.unassign", a.PolicyID,
		targetAuditDetail("", assignmentTarget{ScopeType: a.ScopeType, ScopeID: a.ScopeID, OS: a.OS, Role: a.Role}))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

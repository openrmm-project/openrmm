package api

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/patches"
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
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.scan", id, nil)
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
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.install", id, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "installing"})
}

func (s *Server) rebootDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	if err := s.patches.DispatchReboot(r.Context(), id, 60); err != nil {
		if errors.Is(err, patches.ErrOffline) {
			writeErr(w, http.StatusServiceUnavailable, "device is offline")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "reboot", id, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "rebooting"})
}

// ---- catalog + approval ----

func (s *Server) listPatchCatalog(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.PatchCatalog(r.Context(), s.reportFilter(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patches": rows})
}

func (s *Server) approvePatch(w http.ResponseWriter, r *http.Request) {
	patchID := chi.URLParam(r, "id")
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
	if err := s.store.SetApproval(r.Context(), patchID, in.ScopeType, in.ScopeID, in.Decision, "manual", actorOf(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch."+in.Decision, patchID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) reportCompliance(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ComplianceReport(r.Context(), s.reportFilter(r))
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
	writeJSON(w, http.StatusOK, map[string]any{"policies": pols})
}

func (s *Server) createPatchPolicy(w http.ResponseWriter, r *http.Request) {
	var p models.PatchPolicy
	if err := decode(r, &p); err != nil || p.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
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
	var p models.PatchPolicy
	if err := decode(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	p.ID = chi.URLParam(r, "id")
	if err := s.store.UpdatePatchPolicy(r.Context(), &p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.update", p.ID, nil)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deletePatchPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeletePatchPolicy(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "patch.policy.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listPatchPolicyAssignments(w http.ResponseWriter, r *http.Request) {
	a, err := s.store.ListPatchPolicyAssignments(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": a})
}

func (s *Server) addPatchPolicyAssignment(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
	}
	if err := decode(r, &in); err != nil || in.ScopeType == "" || in.ScopeID == "" {
		writeErr(w, http.StatusBadRequest, "scope_type and scope_id are required")
		return
	}
	if err := s.store.AddPatchPolicyAssignment(r.Context(), chi.URLParam(r, "id"), in.ScopeType, in.ScopeID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

func (s *Server) deletePatchPolicyAssignment(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeletePatchPolicyAssignment(r.Context(), chi.URLParam(r, "aid")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

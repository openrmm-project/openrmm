package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// Policy targeting (docs/10, RFC #258): assignment filters, the global scope,
// exceptions, and a device's role.

// assignmentTarget is the body of a policy or patch policy assignment.
type assignmentTarget struct {
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	OS        string `json:"os"`
	Role      string `json:"role"`
}

// requireAssignmentTarget validates an assignment's scope and filters for the
// caller. A global assignment reaches every device of the fleet, so only an
// unrestricted principal may make or remove one; any other scope must be one
// the caller wholly controls, as before.
func (s *Server) requireAssignmentTarget(w http.ResponseWriter, r *http.Request, in *assignmentTarget, allowed ...string) bool {
	in.ScopeType, in.ScopeID = strings.TrimSpace(in.ScopeType), strings.TrimSpace(in.ScopeID)
	in.OS, in.Role = strings.TrimSpace(in.OS), strings.TrimSpace(in.Role)
	if !models.ValidTargetFilter(in.OS, in.Role) {
		writeErr(w, http.StatusBadRequest, "os must be windows, macos or linux and role workstation or server (or empty for any)")
		return false
	}
	if in.ScopeType == models.ScopeGlobal {
		in.ScopeID = models.GlobalScopeID
		return s.requireUnrestricted(w, r, "assignments to every device")
	}
	if in.ScopeType == "" || in.ScopeID == "" {
		writeErr(w, http.StatusBadRequest, "scope_type and scope_id required")
		return false
	}
	if !oneOf(in.ScopeType, allowed...) {
		writeErr(w, http.StatusBadRequest, "invalid scope_type")
		return false
	}
	return s.requireScope(w, r, in.ScopeType, in.ScopeID)
}

// requireAssignmentScope authorizes removing an existing assignment.
func (s *Server) requireAssignmentScope(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) bool {
	if scopeType == models.ScopeGlobal {
		return s.requireUnrestricted(w, r, "assignments to every device")
	}
	return s.requireScope(w, r, scopeType, scopeID)
}

func targetAuditDetail(name string, in assignmentTarget) []byte {
	b, _ := json.Marshal(map[string]string{"name": name, "scope_type": in.ScopeType, "scope_id": in.ScopeID, "os": in.OS, "role": in.Role})
	return b
}

// ---- exceptions ----

// listPolicyExclusions lists the devices a policy leaves out. A restricted
// reader sees only devices it may see.
func (s *Server) listPolicyExclusions(w http.ResponseWriter, r *http.Request) {
	s.listExclusions(w, r, false)
}

func (s *Server) listPatchPolicyExclusions(w http.ResponseWriter, r *http.Request) {
	s.listExclusions(w, r, true)
}

func (s *Server) listExclusions(w http.ResponseWriter, r *http.Request, patch bool) {
	id := chi.URLParam(r, "id")
	if !s.requirePolicyKind(w, r, patch, id, false) {
		return
	}
	out, err := s.store.ListExclusions(r.Context(), patch, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.scopeUserID(r) != "" {
		visible := out[:0]
		for _, e := range out {
			if s.agentAllowed(r, e.AgentID) {
				visible = append(visible, e)
			}
		}
		out = visible
	}
	writeJSON(w, http.StatusOK, map[string]any{"exclusions": out})
}

// addPolicyExclusion leaves a device out of a policy, whichever assignment
// reaches it. Narrowing a policy is a policy change, so it takes the same
// permission and principal as assigning one.
func (s *Server) addPolicyExclusion(w http.ResponseWriter, r *http.Request) {
	s.changeExclusion(w, r, false, true)
}

func (s *Server) removePolicyExclusion(w http.ResponseWriter, r *http.Request) {
	s.changeExclusion(w, r, false, false)
}

func (s *Server) addPatchPolicyExclusion(w http.ResponseWriter, r *http.Request) {
	s.changeExclusion(w, r, true, true)
}

func (s *Server) removePatchPolicyExclusion(w http.ResponseWriter, r *http.Request) {
	s.changeExclusion(w, r, true, false)
}

func (s *Server) changeExclusion(w http.ResponseWriter, r *http.Request, patch, add bool) {
	id := chi.URLParam(r, "id")
	if !s.requirePolicyKind(w, r, patch, id, true) {
		return
	}
	agentID := chi.URLParam(r, "agentID")
	if add {
		var in struct {
			AgentID string `json:"agent_id"`
		}
		if err := decode(r, &in); err != nil || strings.TrimSpace(in.AgentID) == "" {
			writeErr(w, http.StatusBadRequest, "agent_id required")
			return
		}
		agentID = strings.TrimSpace(in.AgentID)
	}
	if !s.requireAgent(w, r, agentID) {
		return
	}
	var err error
	if add {
		err = s.store.AddExclusion(r.Context(), patch, id, agentID, actorOf(r))
	} else {
		var removed bool
		if removed, err = s.store.RemoveExclusion(r.Context(), patch, id, agentID); err == nil && !removed {
			writeErr(w, http.StatusNotFound, "the device is not excluded from this policy")
			return
		}
	}
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	action := "policy."
	if patch {
		action = "patch.policy."
	}
	if add {
		action += "exclude"
	} else {
		action += "include"
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), action, id, s.deviceAuditLabel(r, agentID))
	s.listExclusions(w, r, patch)
}

// requirePolicyKind applies requirePolicy or requirePatchPolicy.
func (s *Server) requirePolicyKind(w http.ResponseWriter, r *http.Request, patch bool, id string, manage bool) bool {
	if patch {
		return s.requirePatchPolicy(w, r, id, manage)
	}
	return s.requirePolicy(w, r, id, manage)
}

// ---- device role ----

// setDeviceRole sets or clears an administrator's correction of a device's
// role (workstation or server). Policies filtered by role follow it at once.
//
// The role decides which fleet-wide policies reach the device, so changing it
// is a policy decision as much as a device one: a principal confined to some
// clients could otherwise take its devices out of (or into) a policy an
// administrator aimed at servers — an exception it may not make. It takes an
// unrestricted principal, as global assignments and exceptions do.
func (s *Server) setDeviceRole(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireUnrestricted(w, r, "device roles") || !s.requireAgent(w, r, id) {
		return
	}
	var in struct {
		Role *string `json:"role"`
	}
	if err := decode(r, &in); err != nil || in.Role == nil {
		writeErr(w, http.StatusBadRequest, `role required: "workstation", "server", or "" to follow detection`)
		return
	}
	role := strings.TrimSpace(*in.Role)
	if !models.ValidTargetFilter("", role) {
		writeErr(w, http.StatusBadRequest, `role must be "workstation", "server", or "" to follow detection`)
		return
	}
	out, err := s.store.SetAgentRoleOverride(r.Context(), id, role)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]string{"hostname": out.Hostname, "role_override": role, "role": out.Role})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.role", id, detail)
	writeJSON(w, http.StatusOK, out)
}

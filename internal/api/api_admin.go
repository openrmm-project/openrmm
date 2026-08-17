package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

// auditLabel marshals a human-readable label for an audit detail. The audit
// target stays the entity's UUID (unique, immutable); this label is an extra
// display field so the audit report can name an entity even after it is deleted.
func auditLabel(key, val string) []byte {
	b, _ := json.Marshal(map[string]string{key: val})
	return b
}

// deviceAuditLabel snapshots a device's hostname into an audit detail so the
// report can name it even after the device is deleted. Best-effort; target stays
// the agent UUID.
func (s *Server) deviceAuditLabel(r *http.Request, agentID string) []byte {
	if a, err := s.store.GetAgent(r.Context(), agentID); err == nil && a != nil {
		return auditLabel("hostname", a.Hostname)
	}
	return nil
}

// ---------- API tokens (for MCP clients & integrations) ----------

func (s *Server) listAPITokens(w http.ResponseWriter, r *http.Request) {
	var toks []models.APITokenInfo
	var err error
	if uid := s.scopeUserID(r); uid != "" {
		toks, err = s.store.ListAPITokensForUser(r.Context(), uid)
	} else {
		toks, err = s.store.ListAPITokens(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": toks})
}

// createAPIToken issues a scoped, revocable token. The secret is returned ONCE.
func (s *Server) createAPIToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string `json:"name"`
		Role          string `json:"role"`
		UserID        string `json:"user_id"`         // optional: bind to a user to inherit their access scope
		ExpiresInDays int    `json:"expires_in_days"` // optional: 0/omitted = never expires
	}
	_ = decode(r, &in)
	if in.Role == "" {
		in.Role = models.RoleTech // sensible default for MCP connectors
	} else if ok, err := s.roleExists(r.Context(), in.Role); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if !ok {
		writeErr(w, http.StatusBadRequest, "invalid role")
		return
	}
	requestedPerms, err := s.permissionsForRole(r.Context(), in.Role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !permissionsSubset(requestedPerms, permissionsOf(r)) {
		writeErr(w, http.StatusForbidden, "token role exceeds the issuer's permissions")
		return
	}
	if s.scopeUserID(r) != "" && in.Role == models.RoleAdmin {
		writeErr(w, http.StatusForbidden, "restricted issuers cannot create globally privileged tokens")
		return
	}
	if uid := s.scopeUserID(r); uid != "" && in.UserID != uid {
		writeErr(w, http.StatusForbidden, "scoped issuers must bind tokens to their own access scope")
		return
	}
	var boundUser *string
	if in.UserID != "" {
		boundUser = &in.UserID
	}
	var expiresAt *time.Time
	if in.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}
	token := auth.NewToken()
	id, err := s.store.CreateAPIToken(r.Context(), auth.Hash(token), in.Name, in.Role, boundUser, expiresAt)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "apitoken.create", id, auditLabel("name", in.Name))
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "name": in.Name, "role": in.Role, "expires_at": expiresAt,
		"token": token, "note": "store this now — it will not be shown again",
	})
}

func permissionsSubset(requested []string, issuer map[string]bool) bool {
	for _, permission := range requested {
		if !issuer[permission] {
			return false
		}
	}
	return true
}

func (s *Server) deleteAPIToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if uid := s.scopeUserID(r); uid != "" {
		deleted, err := s.store.DeleteAPITokenForUser(r.Context(), id, uid)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !deleted {
			writeErr(w, http.StatusForbidden, "token is outside your access scope")
			return
		}
	} else if err := s.store.DeleteAPIToken(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "apitoken.revoke", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// ---------- Device groups ----------

func (s *Server) listGroups(w http.ResponseWriter, r *http.Request) {
	var groups []models.DeviceGroup
	var err error
	if uid := s.scopeUserID(r); uid != "" {
		groups, err = s.store.ListGroupsForUser(r.Context(), uid)
	} else {
		groups, err = s.store.ListGroups(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string `json:"name"`
		SiteID string `json:"site_id"`
	}
	if err := decode(r, &in); err != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	// A department belongs to a site (Empresa > Site > Departamento).
	if in.SiteID == "" {
		writeErr(w, http.StatusBadRequest, "site_id required (a department belongs to a site)")
		return
	}
	if !s.requireScope(w, r, models.ScopeSite, in.SiteID) {
		return
	}
	site := in.SiteID
	g, err := s.store.CreateGroup(r.Context(), in.Name, &site)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// deleteGroup removes a group. Destructive and permission-gated; never AI/MCP.
func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireScope(w, r, models.ScopeGroup, id) {
		return
	}
	label := ""
	if g, err := s.store.GetGroup(r.Context(), id); err == nil && g != nil {
		label = g.Name
	}
	if err := s.store.DeleteGroup(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "group.delete", id, auditLabel("name", label))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) setDeviceGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GroupID *string `json:"group_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	agentID := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, agentID) {
		return
	}
	if s.scopeUserID(r) != "" && (in.GroupID == nil || *in.GroupID == "") {
		writeErr(w, http.StatusForbidden, "scoped users cannot remove a device from its department")
		return
	}
	// A department lives in one site, so a device can only join a department of
	// its own site (keeps Empresa > Site > Departamento coherent).
	if in.GroupID != nil && *in.GroupID != "" {
		if !s.requireScope(w, r, models.ScopeGroup, *in.GroupID) {
			return
		}
		ag, err := s.store.GetAgent(r.Context(), agentID)
		if err != nil {
			writeErr(w, httpStatusFor(err), err.Error())
			return
		}
		g, err := s.store.GetGroup(r.Context(), *in.GroupID)
		if err != nil {
			writeErr(w, httpStatusFor(err), err.Error())
			return
		}
		if g.SiteID == nil || ag.SiteID == nil || *g.SiteID != *ag.SiteID {
			writeErr(w, http.StatusBadRequest, "the department and the device must belong to the same site")
			return
		}
	}
	if err := s.store.SetAgentGroup(r.Context(), agentID, in.GroupID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "the department and device no longer belong to the same site")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// setDeviceRemoteIDs maps a device to its MeshCentral node id so the
// MeshCentral remote provider can target it.
func (s *Server) setDeviceRemoteIDs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	var in struct {
		MeshID *string `json:"mesh_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetRemoteIDs(r.Context(), id, in.MeshID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// revokeDevice immediately invalidates the endpoint credential and disconnects
// its live control channel. The inventory and audit history are deliberately
// retained; restoring the endpoint requires an explicit new enrollment.
func (s *Server) revokeDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	label := s.deviceAuditLabel(r, id)
	unlock := s.lockAgentLifecycle(id)
	defer unlock()
	if err := s.store.RevokeAgent(r.Context(), id); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	s.closeRetiredDeviceMonitoring(r, id, "revoked")
	s.terminateAgentTransport(r.Context(), id)
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.revoke", id, label)
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// rotateDeviceCredential delivers a replacement only through the authenticated
// live control channel. For suspected compromise, revoke the endpoint and enroll
// it again instead of rotating a credential an attacker may also possess.
func (s *Server) rotateDeviceCredential(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	unlock := s.lockAgentLifecycle(id)
	defer unlock()
	if !s.hub.IsOnline(id) {
		writeErr(w, http.StatusConflict, "device must be online for credential rotation")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if !supportsCredentialRotation(agent.Version) {
		writeErr(w, http.StatusConflict, "update the agent to 0.4.5 or later before rotating its credential")
		return
	}
	rotationID, token := auth.NewToken(), auth.NewToken()
	if err := s.store.BeginAgentCredentialRotation(r.Context(), id, rotationID, auth.Hash(token)); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "a credential rotation is already pending for this device")
			return
		}
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	data, _ := json.Marshal(protocol.CredentialRotateMsg{RotationID: rotationID, AgentToken: token})
	frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgCredentialRotate, Data: data})
	if !s.hub.Send(id, frame) {
		_ = s.store.CancelAgentCredentialRotation(r.Context(), id, rotationID)
		writeErr(w, http.StatusConflict, "device went offline before rotation could be delivered")
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.credential_rotate", id, s.deviceAuditLabel(r, id))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
}

// Credential rotation landed in 0.4.5. Shares one version comparison with the
// other agent-capability gates so the two cannot drift on what a malformed or
// missing version means — it means "too old", here as everywhere.
func supportsCredentialRotation(version string) bool {
	return models.AgentVersionAtLeast(version, "0.4.5")
}

// cancelDeviceCredentialRotation asks the live agent to restore its prior token.
// The pending replacement remains valid until the agent confirms restoration, so
// a delayed rotate frame cannot strand the endpoint with a discarded credential.
func (s *Server) cancelDeviceCredentialRotation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	unlock := s.lockAgentLifecycle(id)
	defer unlock()
	if !s.hub.IsOnline(id) {
		writeErr(w, http.StatusConflict, "device must be online to safely cancel credential rotation")
		return
	}
	agent, err := s.store.GetAgent(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if !supportsCredentialRotation(agent.Version) {
		writeErr(w, http.StatusConflict, "update the agent to 0.4.5 or later before canceling its credential rotation")
		return
	}
	rotationID, err := s.store.RequestAgentCredentialRotationCancellation(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	data, _ := json.Marshal(protocol.CredentialRotationCancelMsg{RotationID: rotationID})
	frame, _ := json.Marshal(protocol.Envelope{Type: protocol.MsgCredentialRotationCancel, Data: data})
	// The durable cancellation fence is also delivered in a reconnect response,
	// so a full queue or closing socket cannot let the replacement be promoted.
	_ = s.hub.Send(id, frame)
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.credential_rotation_cancel_requested", id, s.deviceAuditLabel(r, id))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "canceling"})
}

// deleteDevice removes an agent. Destructive and permission-gated; never exposed
// to the AI/MCP surface.
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.requireAgent(w, r, id) {
		return
	}
	label := ""
	if a, err := s.store.GetAgent(r.Context(), id); err == nil && a != nil {
		label = a.Hostname
	}
	unlock := s.lockAgentLifecycle(id)
	defer unlock()
	if err := s.store.DeleteAgent(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.closeRetiredDeviceMonitoring(r, id, "deleted")
	// The durable delete succeeds before the in-memory relay is permanently
	// fenced. A failed database operation must not leave an active device blocked.
	s.terminateAgentTransport(r.Context(), id)
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.delete", id, auditLabel("hostname", label))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) closeRetiredDeviceMonitoring(r *http.Request, id, kind string) {
	if _, err := s.store.CancelAgentCheckExecutions(r.Context(), id); err != nil {
		log.Printf("cancel checks for %s device %s: %v", kind, id, err)
	}
	if _, err := s.store.ResolveAlertsForRetiredAgent(r.Context(), id, actorOf(r), kind, time.Now()); err != nil {
		log.Printf("resolve alerts for %s device %s: %v", kind, id, err)
	}
}

// terminateAgentTransport fences an endpoint from every in-memory control and
// relay path. Callers hold the per-agent lifecycle lock so an authenticated socket cannot be
// attached between the durable state change and this teardown.
func (s *Server) terminateAgentTransport(ctx context.Context, id string) {
	s.hub.Disconnect(id)
	s.relay.CloseAgent(id)
	_ = s.store.EndRemoteSessionsForAgent(ctx, id)
}

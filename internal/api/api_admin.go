package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
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
	toks, err := s.store.ListAPITokens(r.Context())
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

func (s *Server) deleteAPIToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAPIToken(r.Context(), id); err != nil {
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
	// A department lives in one site, so a device can only join a department of
	// its own site (keeps Empresa > Site > Departamento coherent).
	if in.GroupID != nil && *in.GroupID != "" {
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
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// setDeviceRemoteIDs maps a device to its MeshCentral node id so the
// MeshCentral remote provider can target it.
func (s *Server) setDeviceRemoteIDs(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MeshID *string `json:"mesh_id"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := s.store.SetRemoteIDs(r.Context(), chi.URLParam(r, "id"), in.MeshID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// deleteDevice removes an agent. Destructive and permission-gated; never exposed
// to the AI/MCP surface.
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	label := ""
	if a, err := s.store.GetAgent(r.Context(), id); err == nil && a != nil {
		label = a.Hostname
	}
	if err := s.store.DeleteAgent(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.delete", id, auditLabel("hostname", label))
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

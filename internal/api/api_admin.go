package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
)

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
		Name   string `json:"name"`
		Role   string `json:"role"`
		UserID string `json:"user_id"` // optional: bind to a user to inherit their access scope
	}
	_ = decode(r, &in)
	if roleLevel(in.Role) == 0 {
		in.Role = models.RoleTech // sensible default for MCP connectors
	}
	var boundUser *string
	if in.UserID != "" {
		boundUser = &in.UserID
	}
	token := auth.NewToken()
	id, err := s.store.CreateAPIToken(r.Context(), auth.Hash(token), in.Name, in.Role, boundUser)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "apitoken.create", id, nil)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "name": in.Name, "role": in.Role,
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
	groups, err := s.store.ListGroups(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *Server) createGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	g, err := s.store.CreateGroup(r.Context(), in.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, g)
}

// deleteGroup removes a group. Destructive → admin-only, human action in the tool.
func (s *Server) deleteGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteGroup(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "group.delete", id, nil)
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
	if err := s.store.SetAgentGroup(r.Context(), chi.URLParam(r, "id"), in.GroupID); err != nil {
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

// deleteDevice removes an agent. Destructive → admin-only, human action in the
// tool; never exposed to the AI/MCP surface.
func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAgent(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "device.delete", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
)

func (s *Server) listPermissions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"permissions": models.PermissionCatalog()})
}

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.store.ListRoles(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (s *Server) createRole(w http.ResponseWriter, r *http.Request) {
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "scoped users cannot create global roles")
		return
	}
	var in models.Role
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Key = strings.TrimSpace(in.Key)
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if !validRoleKey(in.Key) {
		writeErr(w, http.StatusBadRequest, "invalid role key")
		return
	}
	if _, _, ok := models.DefaultRoleInfo(in.Key); ok {
		writeErr(w, http.StatusBadRequest, "system role keys are reserved")
		return
	}
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	perms, ok := cleanPermissions(in.Permissions)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown permission")
		return
	}
	in.Permissions = perms
	if !permissionsSubset(in.Permissions, permissionsOf(r)) {
		writeErr(w, http.StatusForbidden, "role exceeds the issuer's permissions")
		return
	}
	out, err := s.store.CreateRole(r.Context(), &in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "role.create", out.Key, nil)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateRole(w http.ResponseWriter, r *http.Request) {
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "scoped users cannot update global roles")
		return
	}
	var in models.Role
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Key = chi.URLParam(r, "key")
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	perms, ok := cleanPermissions(in.Permissions)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown permission")
		return
	}
	in.Permissions = perms
	if !permissionsSubset(in.Permissions, permissionsOf(r)) {
		writeErr(w, http.StatusForbidden, "role exceeds the issuer's permissions")
		return
	}
	out, err := s.store.UpdateRole(r.Context(), &in)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "role.update", out.Key, nil)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	if s.scopeUserID(r) != "" {
		writeErr(w, http.StatusForbidden, "scoped users cannot delete global roles")
		return
	}
	key := chi.URLParam(r, "key")
	if err := s.store.DeleteRole(r.Context(), key); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "role.delete", key, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func cleanPermissions(in []string) ([]string, bool) {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if !models.ValidPermission(p) {
			return nil, false
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, true
}

func validRoleKey(key string) bool {
	if len(key) < 2 || len(key) > 64 {
		return false
	}
	for i, r := range key {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
		if !ok {
			return false
		}
		if i == 0 && !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (s *Server) roleExists(ctx context.Context, key string) (bool, error) {
	if _, _, ok := models.DefaultRoleInfo(key); ok {
		return true, nil
	}
	return s.store.RoleExists(ctx, key)
}

package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
)

const sessionTTL = 7 * 24 * time.Hour

// bootstrapAdmin creates the very first admin user, only when no users exist.
func (s *Server) bootstrapAdmin(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if n > 0 {
		writeErr(w, http.StatusForbidden, "users already exist; use an admin to create more")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil || in.Username == "" || in.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u, err := s.store.CreateUser(r.Context(), in.Username, hash, models.RoleAdmin)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Issue a session immediately so the caller is logged in (matches login shape).
	token := auth.NewToken()
	if err := s.store.CreateSession(r.Context(), u.ID, auth.Hash(token), time.Now().Add(sessionTTL)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "role": u.Role, "user": u})
}

// login verifies credentials and returns a session bearer token.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	id, role, hash, err := s.store.GetUserAuth(r.Context(), in.Username)
	if err != nil {
		// Run a dummy bcrypt compare so response time doesn't reveal whether the
		// username exists (mitigates user enumeration).
		auth.CheckPassword("$2a$10$invalidinvalidinvalidinvalidinvalidinvalidinvalidinvalidin", in.Password)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if !auth.CheckPassword(hash, in.Password) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token := auth.NewToken()
	if err := s.store.CreateSession(r.Context(), id, auth.Hash(token), time.Now().Add(sessionTTL)); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	user, _ := s.store.GetUserByID(r.Context(), id)
	if user == nil {
		user = &models.User{ID: id, Username: in.Username, Role: role}
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "role": role, "user": user})
}

// logout revokes the caller's session token.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.store.DeleteSession(r.Context(), auth.Hash(bearer(r)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// me returns the current principal as a User (resolves real users; synthesizes
// one for the bootstrap admin token / API tokens).
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	actor, role := actorOf(r), roleOf(r)
	if u, err := s.store.GetUserByID(r.Context(), actor); err == nil {
		writeJSON(w, http.StatusOK, u)
		return
	}
	name := actor
	if actor == "admin" {
		name = "admin"
	} else if actor == "api" {
		name = "api-token"
	}
	writeJSON(w, http.StatusOK, models.User{ID: actor, Username: name, Role: role})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string             `json:"username"`
		Password string             `json:"password"`
		Role     string             `json:"role"`
		Scopes   []models.UserScope `json:"scopes"` // empty = full access
	}
	if err := decode(r, &in); err != nil || in.Username == "" || in.Password == "" {
		writeErr(w, http.StatusBadRequest, "username and password required")
		return
	}
	if roleLevel(in.Role) == 0 {
		in.Role = models.RoleTech
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	u, err := s.store.CreateUser(r.Context(), in.Username, hash, in.Role)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(in.Scopes) > 0 {
		if err := s.store.ReplaceUserScopes(r.Context(), u.ID, in.Scopes); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusCreated, u)
}

// getUserScopes returns a user's access scopes (empty = full access).
func (s *Server) getUserScopes(w http.ResponseWriter, r *http.Request) {
	scopes, err := s.store.ListUserScopes(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": scopes})
}

// setUserScopes replaces a user's access scopes.
func (s *Server) setUserScopes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scopes []models.UserScope `json:"scopes"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.ReplaceUserScopes(r.Context(), chi.URLParam(r, "id"), in.Scopes); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scopes": in.Scopes})
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteUser(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

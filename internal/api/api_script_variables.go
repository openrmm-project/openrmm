package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/scriptvars"
	"github.com/p1ngu/rmm/internal/store"
)

func (s *Server) listScriptVariables(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListScriptVariables(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []store.ScriptVariable{}
	}
	if !hasPermission(r, models.PermScriptsVariables) {
		for i := range items {
			items[i].Values = []store.ScriptVariableValue{}
		}
	}
	uid := s.scopeUserID(r)
	if uid != "" {
		for i := range items {
			visible := items[i].Values[:0]
			for _, value := range items[i].Values {
				if value.ScopeType == "global" {
					visible = append(visible, value)
					continue
				}
				ok, err := s.store.ScopeAllowedForUser(r.Context(), uid, value.ScopeType, value.ScopeID)
				if err != nil {
					writeErr(w, http.StatusInternalServerError, err.Error())
					return
				}
				if ok {
					visible = append(visible, value)
				}
			}
			items[i].Values = visible
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"variables": items})
}

func (s *Server) createScriptVariable(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "script variables") {
		return
	}
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Secret      bool   `json:"secret"`
	}
	if err := decode(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	created, err := s.store.CreateScriptVariable(r.Context(), in.Name, in.Description, in.Secret)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": created.Name, "secret": created.Secret})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script_variable.create", created.ID, detail)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateScriptVariable(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "script variables") {
		return
	}
	var in struct {
		Description string `json:"description"`
		Secret      bool   `json:"secret"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	id := chi.URLParam(r, "id")
	name, secretBefore, err := s.store.ScriptVariableName(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if err := s.store.UpdateScriptVariable(r.Context(), s.cfg, id, in.Description, in.Secret); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": name, "secret": in.Secret, "was_secret": secretBefore})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script_variable.update", id, detail)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "name": name, "secret": in.Secret})
}

func (s *Server) deleteScriptVariable(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "script variables") {
		return
	}
	id := chi.URLParam(r, "id")
	name, err := s.store.DeleteScriptVariable(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": name})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script_variable.delete", id, detail)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (s *Server) setScriptVariableValue(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ScopeType string `json:"scope_type"`
		ScopeID   string `json:"scope_id"`
		Value     string `json:"value"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.ScopeType = strings.TrimSpace(in.ScopeType)
	in.ScopeID = strings.TrimSpace(in.ScopeID)
	if in.ScopeType == "global" {
		if !s.requireUnrestricted(w, r, "the global script variable") {
			return
		}
		in.ScopeID = ""
	} else if !s.requireScope(w, r, in.ScopeType, in.ScopeID) {
		return
	}
	id := chi.URLParam(r, "id")
	name, secret, err := s.store.ScriptVariableName(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if err := s.store.SetScriptVariableValue(r.Context(), s.cfg, id, in.ScopeType, in.ScopeID, in.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": name, "scope_type": in.ScopeType, "scope_id": in.ScopeID, "secret": secret})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script_variable.set", id, detail)
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "scope_type": in.ScopeType, "scope_id": in.ScopeID, "set": true})
}

func (s *Server) clearScriptVariableValue(w http.ResponseWriter, r *http.Request) {
	scopeType := r.URL.Query().Get("scope_type")
	scopeID := r.URL.Query().Get("scope_id")
	if scopeType == "global" {
		if !s.requireUnrestricted(w, r, "the global script variable") {
			return
		}
		scopeID = ""
	} else if !s.requireScope(w, r, scopeType, scopeID) {
		return
	}
	id := chi.URLParam(r, "id")
	name, secret, err := s.store.ScriptVariableName(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if err := s.store.ClearScriptVariableValue(r.Context(), id, scopeType, scopeID); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": name, "scope_type": scopeType, "scope_id": scopeID, "secret": secret})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script_variable.clear", id, detail)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// prepareScriptVariables normalizes the names a script asks for and rejects
// unknown ones. Nil means "not sent" and is left for the caller.
func (s *Server) prepareScriptVariables(r *http.Request, shell string, names []string) ([]string, error) {
	if names == nil {
		return nil, nil
	}
	normalized, err := scriptvars.Normalize(names)
	if err != nil {
		return nil, err
	}
	if err := scriptvars.CheckShell(shell, len(normalized)); err != nil {
		return nil, err
	}
	missing, err := s.store.UnknownScriptVariables(r.Context(), normalized)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("unknown script variable %s", strings.Join(missing, ", "))
	}
	return normalized, nil
}

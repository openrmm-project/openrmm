package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/autonomy"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func (s *Server) listScripts(w http.ResponseWriter, r *http.Request) {
	scripts, err := s.store.ListScripts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": scripts})
}

// listScriptCategories returns the categories in use with their script counts.
//
// The console needs them for two things: offering what already exists when a
// script is filed, so a category is picked rather than retyped, and listing them
// for renaming. Free text with no such list is how one category becomes two that
// differ by a keystroke.
func (s *Server) listScriptCategories(w http.ResponseWriter, r *http.Request) {
	categories, err := s.store.ListScriptCategories(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": categories})
}

// createScriptCategory registers a category before any script is filed under it,
// which is what lets an operator name the filing scheme first and sort into it
// afterwards.
func (s *Server) createScriptCategory(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "the global script library") {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	// A name already in use is a conflict even when only a script carries it and
	// the registry has not adopted it yet — otherwise "creating" it would look
	// like it worked and quietly change nothing.
	if exists, err := s.store.ScriptCategoryExists(r.Context(), name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if exists {
		writeErr(w, http.StatusConflict, "that category already exists")
		return
	}
	if err := s.store.CreateScriptCategory(r.Context(), name); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script.category.create", name, nil)
	writeJSON(w, http.StatusCreated, models.ScriptCategory{Name: name})
}

// renameScriptCategory renames a category and every script filed under it.
//
// Renaming onto a name already in use merges the two. That is not an accident to
// guard against — it is the repair for a category that a typo split in half —
// but the console confirms it, because the operator has to know the two are
// about to become one.
func (s *Server) renameScriptCategory(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "the global script library") {
		return
	}
	from := chi.URLParam(r, "name")
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to := strings.TrimSpace(in.Name)
	if to == "" {
		// Emptying a category is its own operation, because it also unfiles the
		// scripts. DELETE says that; a blank rename would not.
		writeErr(w, http.StatusBadRequest, "name required — use DELETE to remove a category")
		return
	}
	if to == from {
		writeErr(w, http.StatusBadRequest, "the category already has that name")
		return
	}
	if exists, err := s.store.ScriptCategoryExists(r.Context(), from); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if !exists {
		writeErr(w, http.StatusNotFound, "no such category: "+from)
		return
	}
	moved, err := s.store.RenameScriptCategory(r.Context(), from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"from": from, "to": to, "scripts": moved})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script.category.rename", from, detail)
	writeJSON(w, http.StatusOK, map[string]any{"name": to, "scripts": moved})
}

// deleteScriptCategory removes a category and returns its scripts to
// uncategorized. The scripts survive — this is a filing decision, not a way to
// delete work.
func (s *Server) deleteScriptCategory(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "the global script library") {
		return
	}
	name := chi.URLParam(r, "name")
	if exists, err := s.store.ScriptCategoryExists(r.Context(), name); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if !exists {
		writeErr(w, http.StatusNotFound, "no such category: "+name)
		return
	}
	unfiled, err := s.store.DeleteScriptCategory(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": name, "scripts": unfiled})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script.category.delete", name, detail)
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "scripts": unfiled})
}

func (s *Server) getScript(w http.ResponseWriter, r *http.Request) {
	sc, err := s.store.GetScript(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) createScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "the global script library") {
		return
	}
	var sc models.Script
	if err := decode(r, &sc); err != nil || sc.Name == "" {
		writeErr(w, http.StatusBadRequest, "name required")
		return
	}
	if sc.Shell == "" {
		sc.Shell = models.ShellBash
	}
	if err := autonomy.ValidateParamSchema(sc.Shell, sc.Params); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.CreateScript(r.Context(), &sc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.store.RegisterScriptCategory(r.Context(), out.Category)
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "the global script library") {
		return
	}
	var sc models.Script
	if err := decode(r, &sc); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	sc.ID = chi.URLParam(r, "id")
	if err := autonomy.ValidateParamSchema(sc.Shell, sc.Params); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	revoked, err := s.store.UpdateScript(r.Context(), &sc)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if revoked {
		// The edit changed what runs, so the AI can no longer run it on the old
		// release: an administrator has to look at the new content and release it.
		detail, _ := json.Marshal(map[string]any{"reason": "edited", "name": sc.Name})
		_ = s.store.AddAudit(r.Context(), actorOf(r), "script.ai_release.revoked", sc.ID, detail)
	}
	out, _ := s.store.GetScript(r.Context(), sc.ID)
	s.store.RegisterScriptCategory(r.Context(), out.Category)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "the global script library") {
		return
	}
	if err := s.store.DeleteScript(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// setScriptAIRelease releases a library script for the AI to run without
// approval, or withdraws the release (docs/23-ai-autonomy.md). Releasing needs
// scripts.manage and ai.autonomy.manage; the server's rules (deny list,
// parameter schema) apply whatever the caller marks. The release is bound to
// the hash of the content the caller just read: an edit in between is refused.
func (s *Server) setScriptAIRelease(w http.ResponseWriter, r *http.Request) {
	if !s.requireUnrestricted(w, r, "the global script library") {
		return
	}
	var in struct {
		Release *bool `json:"release"`
	}
	if err := decode(r, &in); err != nil || in.Release == nil {
		writeErr(w, http.StatusBadRequest, "release (true or false) required")
		return
	}
	id := chi.URLParam(r, "id")
	sc, err := s.store.GetScript(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	if !*in.Release {
		had, err := s.store.ClearScriptAIRelease(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if had {
			detail, _ := json.Marshal(map[string]any{"reason": "withdrawn", "name": sc.Name})
			_ = s.store.AddAudit(r.Context(), actorOf(r), "script.ai_release.revoked", id, detail)
		}
		out, _ := s.store.GetScript(r.Context(), id)
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err := autonomy.CheckRelease(sc); err != nil {
		writeCodedErr(w, http.StatusUnprocessableEntity, "release_refused", err.Error())
		return
	}
	hash := models.ScriptContentHash(sc.Shell, sc.Body, sc.Params)
	if err := s.store.SetScriptAIRelease(r.Context(), id, actorOf(r), hash, sc.UpdatedAt); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, http.StatusConflict, "the script was edited meanwhile; review it again before releasing")
			return
		}
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	detail, _ := json.Marshal(map[string]any{"name": sc.Name, "hash": hash, "shell": sc.Shell, "params": len(sc.Params)})
	_ = s.store.AddAudit(r.Context(), actorOf(r), "script.ai_release", id, detail)
	out, _ := s.store.GetScript(r.Context(), id)
	writeJSON(w, http.StatusOK, out)
}

// runLibraryScript runs a stored script on a device, creating an approval-gated
// job (or pre-approved when require_approval=false and the caller can manage jobs).
func (s *Server) runLibraryScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	var in struct {
		ScriptID        string         `json:"script_id"`
		RequireApproval *bool          `json:"require_approval"`
		Params          map[string]any `json:"params"`
	}
	if err := decode(r, &in); err != nil || in.ScriptID == "" {
		writeErr(w, http.StatusBadRequest, "script_id required")
		return
	}
	sc, err := s.store.GetScript(r.Context(), in.ScriptID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	// A script with declared parameters takes values only from their accepted
	// sets, delivered as environment variables. It always waits for approval:
	// the values are part of what the approver is agreeing to.
	if len(sc.Params) > 0 || len(in.Params) > 0 {
		params, err := autonomy.ValidateParamValues(sc.Params, in.Params)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		job, err := s.jobs.ProposeScriptWithParams(r.Context(), chi.URLParam(r, "id"), sc, params, "library: "+sc.Name, actorOf(r))
		if err != nil {
			writeErr(w, httpStatusFor(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, job)
		return
	}
	// Approval mandatory unless the deployer opted into unapproved jobs and the
	// caller can also manage jobs. jobs.create alone may only create approval-gated jobs.
	require := true
	if in.RequireApproval != nil && s.cfg.AllowUnapprovedJobs && hasPermission(r, models.PermJobsManage) {
		require = *in.RequireApproval
	}
	job, err := s.jobs.Propose(r.Context(), chi.URLParam(r, "id"), sc.Shell, sc.Body, "library: "+sc.Name, actorOf(r), require)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, job)
}

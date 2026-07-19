package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
)

func (s *Server) listScripts(w http.ResponseWriter, r *http.Request) {
	scripts, err := s.store.ListScripts(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scripts": scripts})
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
	out, err := s.store.CreateScript(r.Context(), &sc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
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
	if err := s.store.UpdateScript(r.Context(), &sc); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, _ := s.store.GetScript(r.Context(), sc.ID)
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

// runLibraryScript runs a stored script on a device, creating an approval-gated
// job (or pre-approved when require_approval=false and the caller can manage jobs).
func (s *Server) runLibraryScript(w http.ResponseWriter, r *http.Request) {
	if !s.requireAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	var in struct {
		ScriptID        string `json:"script_id"`
		RequireApproval *bool  `json:"require_approval"`
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

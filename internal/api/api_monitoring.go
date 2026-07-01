package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// enabledDefaultTrue reports the desired enabled flag: true unless the JSON body
// explicitly set "enabled": false.
func enabledDefaultTrue(body []byte) bool {
	var probe struct {
		Enabled *bool `json:"enabled"`
	}
	_ = json.Unmarshal(body, &probe)
	return probe.Enabled == nil || *probe.Enabled
}

// ---------- checks ----------

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	policyID := r.URL.Query().Get("policy_id")
	checks, err := s.store.ListChecks(r.Context(), r.URL.Query().Get("agent_id"), policyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped := s.scopedAgentSet(r); scoped {
		policyVisible := false
		if policyID != "" {
			policyVisible, err = s.store.PolicyAllowedForUser(r.Context(), s.scopeUserID(r), policyID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		f := checks[:0]
		for _, c := range checks {
			if inScope(set, c.AgentID) || (policyVisible && c.AgentID == nil && c.PolicyID != nil && *c.PolicyID == policyID) {
				f = append(f, c)
			}
		}
		checks = f
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

func (s *Server) createCheck(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var c models.Check
	if err := json.Unmarshal(body, &c); err != nil || c.Type == "" {
		writeErr(w, http.StatusBadRequest, "type required")
		return
	}
	if c.AgentID == nil && c.PolicyID == nil {
		writeErr(w, http.StatusBadRequest, "agent_id or policy_id required")
		return
	}
	c.Enabled = enabledDefaultTrue(body)
	out, err := s.store.CreateCheck(r.Context(), &c)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	var c models.Check
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	c.ID = chi.URLParam(r, "id")
	if err := s.store.UpdateCheck(r.Context(), &c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, _ := s.store.GetCheck(r.Context(), c.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteCheck(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteCheck(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) listCheckResults(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	results, err := s.store.ListCheckResults(r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("agent_id"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped := s.scopedAgentSet(r); scoped {
		f := results[:0]
		for _, rr := range results {
			if set[rr.AgentID] {
				f = append(f, rr)
			}
		}
		results = f
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// ---------- tasks ----------

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	policyID := r.URL.Query().Get("policy_id")
	tasks, err := s.store.ListTasks(r.Context(), r.URL.Query().Get("agent_id"), policyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped := s.scopedAgentSet(r); scoped {
		policyVisible := false
		if policyID != "" {
			policyVisible, err = s.store.PolicyAllowedForUser(r.Context(), s.scopeUserID(r), policyID)
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		f := tasks[:0]
		for _, t := range tasks {
			if inScope(set, t.AgentID) || (policyVisible && t.AgentID == nil && t.PolicyID != nil && *t.PolicyID == policyID) {
				f = append(f, t)
			}
		}
		tasks = f
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var t models.Task
	if err := json.Unmarshal(body, &t); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if t.AgentID == nil && t.PolicyID == nil {
		writeErr(w, http.StatusBadRequest, "agent_id or policy_id required")
		return
	}
	if t.ScheduleKind == "" {
		t.ScheduleKind = models.ScheduleInterval
	}
	if t.Shell == "" {
		t.Shell = models.ShellBash
	}
	t.Enabled = enabledDefaultTrue(body)
	out, err := s.store.CreateTask(r.Context(), &t)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	var t models.Task
	if err := decode(r, &t); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	t.ID = chi.URLParam(r, "id")
	if err := s.store.UpdateTask(r.Context(), &t); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, _ := s.store.GetTask(r.Context(), t.ID)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteTask(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---------- alerts ----------

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	alerts, err := s.store.ListAlerts(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped := s.scopedAgentSet(r); scoped {
		f := alerts[:0]
		for _, a := range alerts {
			if inScope(set, a.AgentID) {
				f = append(f, a)
			}
		}
		alerts = f
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

// requireAlertAgent enforces scope on an alert by its target agent. Returns
// false when it has written the response.
func (s *Server) requireAlertAgent(w http.ResponseWriter, r *http.Request, alertID string) bool {
	if s.scopeUserID(r) == "" {
		return true
	}
	agentID, err := s.store.AlertAgent(r.Context(), alertID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return false
	}
	if agentID == nil || !s.agentAllowed(r, *agentID) {
		writeErr(w, http.StatusForbidden, "alert is outside your access scope")
		return false
	}
	return true
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlertAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	if err := s.store.SetAlertStatus(r.Context(), chi.URLParam(r, "id"), models.AlertAcked); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "acked"})
}

func (s *Server) resolveAlert(w http.ResponseWriter, r *http.Request) {
	if !s.requireAlertAgent(w, r, chi.URLParam(r, "id")) {
		return
	}
	if err := s.store.SetAlertStatus(r.Context(), chi.URLParam(r, "id"), models.AlertResolved); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

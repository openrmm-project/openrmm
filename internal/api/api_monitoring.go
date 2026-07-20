package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// validateSchedule normalizes and validates a task's schedule fields for its
// kind. It mutates t (clamping the weekday bitmask) and returns a user-facing
// error for anything the scheduler could not act on.
func validateSchedule(t *models.Task) error {
	switch t.ScheduleKind {
	case models.ScheduleInterval:
		if t.IntervalSec <= 0 {
			return fmt.Errorf("interval_sec must be positive")
		}
	case models.ScheduleDaily:
		if t.RunAtMinute < 0 || t.RunAtMinute > 1439 {
			return fmt.Errorf("run_at_minute out of range")
		}
	case models.ScheduleWeekly:
		t.DaysOfWeek &= 0x7F // keep the 7 weekday bits (Sun..Sat)
		if t.DaysOfWeek == 0 {
			return fmt.Errorf("weekly schedule needs at least one day of week")
		}
		if len(t.RunAtMinutes) == 0 {
			return fmt.Errorf("weekly schedule needs at least one time of day")
		}
		for _, m := range t.RunAtMinutes {
			if m < 0 || m > 1439 {
				return fmt.Errorf("run_at_minutes out of range")
			}
		}
	default:
		return fmt.Errorf("unknown schedule_kind %q", t.ScheduleKind)
	}
	return nil
}

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

func (s *Server) requireCheck(w http.ResponseWriter, r *http.Request, id string, manage bool) (*models.Check, bool) {
	c, err := s.store.GetCheck(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return nil, false
	}
	if c.AgentID != nil {
		if !s.requireAgent(w, r, *c.AgentID) {
			return nil, false
		}
	} else if c.PolicyID == nil || !s.requirePolicy(w, r, *c.PolicyID, manage) {
		return nil, false
	}
	return c, true
}

func (s *Server) requireTask(w http.ResponseWriter, r *http.Request, id string, manage bool) (*models.Task, bool) {
	t, err := s.store.GetTask(r.Context(), id)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return nil, false
	}
	if t.AgentID != nil {
		if !s.requireAgent(w, r, *t.AgentID) {
			return nil, false
		}
	} else if t.PolicyID == nil || !s.requirePolicy(w, r, *t.PolicyID, manage) {
		return nil, false
	}
	return t, true
}

func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	policyID := r.URL.Query().Get("policy_id")
	if agentID != "" && !s.requireAgent(w, r, agentID) {
		return
	}
	if policyID != "" && !s.requirePolicy(w, r, policyID, false) {
		return
	}
	checks, err := s.store.ListChecks(r.Context(), agentID, policyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
		visiblePolicies := map[string]bool{}
		f := checks[:0]
		for _, c := range checks {
			policyVisible := false
			if c.PolicyID != nil {
				var found bool
				policyVisible, found = visiblePolicies[*c.PolicyID]
				if !found {
					policyVisible, err = s.store.PolicyAllowedForUser(r.Context(), s.scopeUserID(r), *c.PolicyID)
					if err != nil {
						writeErr(w, http.StatusInternalServerError, err.Error())
						return
					}
					visiblePolicies[*c.PolicyID] = policyVisible
				}
			}
			if inScope(set, c.AgentID) || policyVisible {
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
	if (c.AgentID == nil) == (c.PolicyID == nil) {
		writeErr(w, http.StatusBadRequest, "exactly one of agent_id or policy_id is required")
		return
	}
	if c.AgentID != nil && !s.requireAgent(w, r, *c.AgentID) {
		return
	}
	if c.PolicyID != nil && !s.requirePolicy(w, r, *c.PolicyID, true) {
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
	id := chi.URLParam(r, "id")
	existing, ok := s.requireCheck(w, r, id, true)
	if !ok {
		return
	}
	var c models.Check
	if err := decode(r, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if (c.AgentID != nil && (existing.AgentID == nil || *c.AgentID != *existing.AgentID)) ||
		(c.PolicyID != nil && (existing.PolicyID == nil || *c.PolicyID != *existing.PolicyID)) {
		writeErr(w, http.StatusBadRequest, "check ownership cannot be changed")
		return
	}
	c.ID = id
	if err := s.store.UpdateCheck(r.Context(), &c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := s.store.GetCheck(r.Context(), c.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteCheck(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := s.requireCheck(w, r, id, true); !ok {
		return
	}
	if err := s.store.DeleteCheck(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) listCheckResults(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireCheck(w, r, chi.URLParam(r, "id"), false); !ok {
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID != "" && !s.requireAgent(w, r, agentID) {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	results, err := s.store.ListCheckResults(r.Context(), chi.URLParam(r, "id"), agentID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
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
	agentID := r.URL.Query().Get("agent_id")
	policyID := r.URL.Query().Get("policy_id")
	if agentID != "" && !s.requireAgent(w, r, agentID) {
		return
	}
	if policyID != "" && !s.requirePolicy(w, r, policyID, false) {
		return
	}
	tasks, err := s.store.ListTasks(r.Context(), agentID, policyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
		visiblePolicies := map[string]bool{}
		f := tasks[:0]
		for _, t := range tasks {
			policyVisible := false
			if t.PolicyID != nil {
				var found bool
				policyVisible, found = visiblePolicies[*t.PolicyID]
				if !found {
					policyVisible, err = s.store.PolicyAllowedForUser(r.Context(), s.scopeUserID(r), *t.PolicyID)
					if err != nil {
						writeErr(w, http.StatusInternalServerError, err.Error())
						return
					}
					visiblePolicies[*t.PolicyID] = policyVisible
				}
			}
			if inScope(set, t.AgentID) || policyVisible {
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
	if (t.AgentID == nil) == (t.PolicyID == nil) {
		writeErr(w, http.StatusBadRequest, "exactly one of agent_id or policy_id is required")
		return
	}
	if t.AgentID != nil && !s.requireAgent(w, r, *t.AgentID) {
		return
	}
	if t.PolicyID != nil && !s.requirePolicy(w, r, *t.PolicyID, true) {
		return
	}
	if t.ScheduleKind == "" {
		t.ScheduleKind = models.ScheduleInterval
	}
	if t.Shell == "" {
		t.Shell = models.ShellBash
	}
	if err := validateSchedule(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
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
	id := chi.URLParam(r, "id")
	existing, ok := s.requireTask(w, r, id, true)
	if !ok {
		return
	}
	var t models.Task
	if err := decode(r, &t); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if (t.AgentID != nil && (existing.AgentID == nil || *t.AgentID != *existing.AgentID)) ||
		(t.PolicyID != nil && (existing.PolicyID == nil || *t.PolicyID != *existing.PolicyID)) {
		writeErr(w, http.StatusBadRequest, "task ownership cannot be changed")
		return
	}
	t.ID = id
	if t.ScheduleKind == "" {
		t.ScheduleKind = models.ScheduleInterval
	}
	if err := validateSchedule(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateTask(r.Context(), &t); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out, err := s.store.GetTask(r.Context(), t.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := s.requireTask(w, r, id, true); !ok {
		return
	}
	if err := s.store.DeleteTask(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// taskTargetAgents resolves the agents a task runs on (its own agent, or every
// agent its policy is assigned to), narrowed to the caller's access scope.
func (s *Server) taskTargetAgents(r *http.Request, t *models.Task) ([]string, error) {
	var ids []string
	switch {
	case t.AgentID != nil:
		ids = []string{*t.AgentID}
	case t.PolicyID != nil:
		var err error
		if ids, err = s.store.AgentIDsForPolicy(r.Context(), *t.PolicyID); err != nil {
			return nil, err
		}
	}
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		return nil, err
	} else if scoped {
		f := ids[:0]
		for _, id := range ids {
			id := id
			if inScope(set, &id) {
				f = append(f, id)
			}
		}
		ids = f
	}
	return ids, nil
}

// taskTargets reports how many agents a "run now" would hit, so the UI can
// confirm ("Run on N agents?") before firing.
func (s *Server) taskTargets(w http.ResponseWriter, r *http.Request) {
	t, ok := s.requireTask(w, r, chi.URLParam(r, "id"), false)
	if !ok {
		return
	}
	ids, err := s.taskTargetAgents(r, t)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(ids)})
}

// runTask executes a scheduled task on demand, enqueuing its script on every
// target agent right now (outside the schedule), the same way the scheduler
// would. Offline agents receive it on reconnect. Read-only tasks aside, this is
// gated by tasks.manage.
func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.requireTask(w, r, chi.URLParam(r, "id"), false)
	if !ok {
		return
	}
	ids, err := s.taskTargetAgents(r, t)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	shell, script := t.Shell, t.Script
	if t.ScriptID != nil {
		if sc, err := s.store.GetScript(r.Context(), *t.ScriptID); err == nil {
			shell, script = sc.Shell, sc.Body
		}
	}
	tid := t.ID
	queued := 0
	for _, id := range ids {
		if _, err := s.jobs.RunAutomated(r.Context(), id, shell, script, "task: "+t.Name+" (manual run)", &tid); err != nil {
			log.Printf("run task %s on %s: %v", t.ID, id, err)
			continue
		}
		queued++
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued})
}

// ---------- alerts ----------

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	alerts, err := s.store.ListAlerts(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if set, scoped, err := s.scopedAgentSet(r); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	} else if scoped {
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
	agentID, err := s.store.AlertAgent(r.Context(), alertID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return false
	}
	if s.scopeUserID(r) == "" {
		return true
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

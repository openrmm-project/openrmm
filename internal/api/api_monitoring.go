package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func supportedCheckType(checkType string) bool {
	switch checkType {
	case models.CheckDiskSpace, models.CheckMemory, models.CheckCPULoad,
		models.CheckPing, models.CheckWinService, models.CheckScript:
		return true
	default:
		return false
	}
}

func finitePercent(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 100
}

// validateCheck is shared by create/update so API clients cannot create a
// definition the agent cannot run or the server would silently treat healthy.
func validateCheck(c *models.Check) error {
	c.Type = strings.TrimSpace(c.Type)
	c.Name = strings.TrimSpace(c.Name)
	if !supportedCheckType(c.Type) {
		return fmt.Errorf("unsupported check type %q", c.Type)
	}
	if c.Name == "" || len(c.Name) > 200 {
		return fmt.Errorf("name is required and must be at most 200 characters")
	}
	if c.IntervalSec < 30 || c.IntervalSec > 86400 {
		return fmt.Errorf("interval_sec must be between 30 and 86400")
	}
	if c.TimeoutSec < 5 || c.TimeoutSec > 86400 {
		return fmt.Errorf("timeout_sec must be between 5 and 86400")
	}
	if c.FailureCount < 1 || c.FailureCount > 100 || c.RecoveryCount < 1 || c.RecoveryCount > 100 {
		return fmt.Errorf("failure_count and recovery_count must be between 1 and 100")
	}
	if math.IsNaN(c.Hysteresis) || math.IsInf(c.Hysteresis, 0) || c.Hysteresis < 0 || c.Hysteresis > 100 {
		return fmt.Errorf("hysteresis must be between 0 and 100")
	}
	if c.CooldownSec < 0 || c.CooldownSec > 604800 {
		return fmt.Errorf("cooldown_sec must be between 0 and 604800")
	}
	if len(c.Config) == 0 {
		c.Config = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(c.Config, &object); err != nil || object == nil {
		return fmt.Errorf("config must be a JSON object")
	}

	switch c.Type {
	case models.CheckDiskSpace, models.CheckMemory:
		if !finitePercent(c.WarnThreshold) || !finitePercent(c.ErrThreshold) || *c.ErrThreshold >= *c.WarnThreshold {
			return fmt.Errorf("low-water checks require 0-100 thresholds with err_threshold below warn_threshold")
		}
	case models.CheckCPULoad:
		if !finitePercent(c.WarnThreshold) || !finitePercent(c.ErrThreshold) || *c.ErrThreshold <= *c.WarnThreshold {
			return fmt.Errorf("CPU checks require 0-100 thresholds with err_threshold above warn_threshold")
		}
	case models.CheckPing:
		var cfg struct {
			Host string `json:"host"`
		}
		if err := json.Unmarshal(c.Config, &cfg); err != nil || strings.TrimSpace(cfg.Host) == "" {
			return fmt.Errorf("ping config.host is required")
		}
		c.WarnThreshold, c.ErrThreshold = nil, nil
	case models.CheckWinService:
		var cfg struct {
			Service string `json:"service"`
		}
		if err := json.Unmarshal(c.Config, &cfg); err != nil || strings.TrimSpace(cfg.Service) == "" {
			return fmt.Errorf("winservice config.service is required")
		}
		c.WarnThreshold, c.ErrThreshold = nil, nil
	case models.CheckScript:
		var cfg struct {
			Shell  string `json:"shell"`
			Script string `json:"script"`
		}
		if err := json.Unmarshal(c.Config, &cfg); err != nil || !models.SupportedShell(cfg.Shell) {
			return fmt.Errorf("script config.shell is required and must be supported")
		}
		if strings.TrimSpace(cfg.Script) == "" {
			return fmt.Errorf("script config.script is required")
		}
		c.WarnThreshold, c.ErrThreshold = nil, nil
	}
	return nil
}

func applyCheckDefaults(body []byte, c *models.Check, existing *models.Check) {
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(body, &raw)
	setInt := func(key string, target *int, createDefault int, old int) {
		if _, ok := raw[key]; ok {
			return
		}
		if existing != nil {
			*target = old
		} else {
			*target = createDefault
		}
	}
	setFloat := func(key string, target *float64, createDefault float64, old float64) {
		if _, ok := raw[key]; ok {
			return
		}
		if existing != nil {
			*target = old
		} else {
			*target = createDefault
		}
	}
	setInt("interval_sec", &c.IntervalSec, 300, valueOrZero(existing, func(x *models.Check) int { return x.IntervalSec }))
	setInt("failure_count", &c.FailureCount, 2, valueOrZero(existing, func(x *models.Check) int { return x.FailureCount }))
	setInt("recovery_count", &c.RecoveryCount, 2, valueOrZero(existing, func(x *models.Check) int { return x.RecoveryCount }))
	setInt("cooldown_sec", &c.CooldownSec, 300, valueOrZero(existing, func(x *models.Check) int { return x.CooldownSec }))
	setInt("timeout_sec", &c.TimeoutSec, 180, valueOrZero(existing, func(x *models.Check) int { return x.TimeoutSec }))
	oldHysteresis := 0.0
	if existing != nil {
		oldHysteresis = existing.Hysteresis
	}
	setFloat("hysteresis", &c.Hysteresis, 2, oldHysteresis)
	if existing != nil {
		if _, ok := raw["enabled"]; !ok {
			c.Enabled = existing.Enabled
		}
		if _, ok := raw["config"]; !ok {
			c.Config = existing.Config
		}
		if _, ok := raw["warn_threshold"]; !ok {
			c.WarnThreshold = existing.WarnThreshold
		}
		if _, ok := raw["err_threshold"]; !ok {
			c.ErrThreshold = existing.ErrThreshold
		}
	}
}

func valueOrZero[T any](v *models.Check, f func(*models.Check) T) (zero T) {
	if v != nil {
		return f(v)
	}
	return zero
}

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
	uid := s.scopeUserID(r)
	for i := range checks {
		instances, err := s.store.CheckInstances(r.Context(), checks[i].ID, uid, uid != "")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		checks[i].Instances = instances
		checks[i].Summary = summarizeCheckInstances(instances)
	}
	writeJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

func summarizeCheckInstances(instances []models.CheckInstance) *models.CheckSummary {
	var out models.CheckSummary
	out.Total = len(instances)
	for i := range instances {
		switch instances[i].Status {
		case models.CheckPassing:
			out.Passing++
		case models.CheckWarning:
			out.Warning++
		case models.CheckFailing:
			out.Failing++
		default:
			out.Unknown++
		}
	}
	return &out
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
	applyCheckDefaults(body, &c, nil)
	if err := validateCheck(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
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
	body, _ := io.ReadAll(r.Body)
	var c models.Check
	if err := json.Unmarshal(body, &c); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if (c.AgentID != nil && (existing.AgentID == nil || *c.AgentID != *existing.AgentID)) ||
		(c.PolicyID != nil && (existing.PolicyID == nil || *c.PolicyID != *existing.PolicyID)) {
		writeErr(w, http.StatusBadRequest, "check ownership cannot be changed")
		return
	}
	c.ID = id
	applyCheckDefaults(body, &c, existing)
	if err := validateCheck(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateCheck(r.Context(), &c); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing.Enabled && !c.Enabled {
		s.cleanupCheckTargets(r, existing, "disabled", "Check disabled by an operator.")
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
	check, ok := s.requireCheck(w, r, id, true)
	if !ok {
		return
	}
	s.cleanupCheckTargets(r, check, "removed", "Check removed by an operator.")
	if err := s.store.DeleteCheck(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) cleanupCheckTargets(r *http.Request, check *models.Check, kind, note string) {
	instances, err := s.store.CheckInstances(r.Context(), check.ID, "", false)
	if err != nil {
		log.Printf("cleanup check %s targets: %v", check.ID, err)
		return
	}
	checkID := check.ID
	for i := range instances {
		agentID := instances[i].AgentID
		if _, err := s.store.CancelPendingCheckExecutions(r.Context(), check.ID, &agentID); err != nil {
			log.Printf("cancel check %s execution for %s: %v", check.ID, agentID, err)
		}
		if _, err := s.store.ResolveAlertSource(r.Context(), models.AlertSourceCheck,
			&checkID, &agentID, actorOf(r), kind, note, time.Now()); err != nil {
			log.Printf("resolve check %s alert for %s: %v", check.ID, agentID, err)
		}
	}
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
	uid := s.scopeUserID(r)
	results, err := s.store.ListCheckResultsForUser(r.Context(), chi.URLParam(r, "id"),
		agentID, uid, uid != "", limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) listCheckInstances(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := s.requireCheck(w, r, id, false); !ok {
		return
	}
	uid := s.scopeUserID(r)
	instances, err := s.store.CheckInstances(r.Context(), id, uid, uid != "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"instances": instances,
		"summary":   summarizeCheckInstances(instances),
	})
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

func parseAlertCursor(raw string) (*time.Time, string, error) {
	if raw == "" {
		return nil, "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, "", fmt.Errorf("invalid cursor")
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return nil, "", fmt.Errorf("invalid cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, "", fmt.Errorf("invalid cursor")
	}
	if _, err := uuid.Parse(parts[1]); err != nil {
		return nil, "", fmt.Errorf("invalid cursor")
	}
	return &at, parts[1], nil
}

func alertCursor(a *models.Alert) string {
	return base64.RawURLEncoding.EncodeToString([]byte(a.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + a.ID))
}

func parseAlertTime(raw, field string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, fmt.Errorf("%s must use RFC3339", field)
	}
	return &at, nil
}

func alertAuditDetail(value map[string]any) []byte {
	b, _ := json.Marshal(value)
	return b
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status, severity, sourceType := q.Get("status"), q.Get("severity"), q.Get("source_type")
	if status != "" && !oneOf(status, "open", models.AlertNew, models.AlertAcked, models.AlertSuppressed, models.AlertResolved) {
		writeErr(w, http.StatusBadRequest, "invalid alert status")
		return
	}
	if severity != "" && !oneOf(severity, models.SeverityInfo, models.SeverityWarning, models.SeverityError) {
		writeErr(w, http.StatusBadRequest, "invalid alert severity")
		return
	}
	if sourceType != "" && !oneOf(sourceType, models.AlertSourceCheck, models.AlertSourceTask,
		models.AlertSourceAgent, models.AlertSourceSystem, "job", "patch", "update", "backup", "security") {
		writeErr(w, http.StatusBadRequest, "invalid alert source_type")
		return
	}
	for name, value := range map[string]string{
		"agent_id": q.Get("agent_id"), "site_id": q.Get("site_id"),
		"client_id": q.Get("client_id"), "group_id": q.Get("group_id"),
	} {
		if value != "" {
			if _, err := uuid.Parse(value); err != nil {
				writeErr(w, http.StatusBadRequest, name+" must be a UUID")
				return
			}
		}
	}
	if len(strings.TrimSpace(q.Get("q"))) > 200 {
		writeErr(w, http.StatusBadRequest, "q must be at most 200 characters")
		return
	}
	limit, err := strconv.Atoi(q.Get("limit"))
	if q.Get("limit") == "" {
		limit = 50
	} else if err != nil || limit < 1 || limit > 200 {
		writeErr(w, http.StatusBadRequest, "limit must be between 1 and 200")
		return
	}
	beforeTime, beforeID, err := parseAlertCursor(q.Get("cursor"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	from, err := parseAlertTime(q.Get("from"), "from")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	to, err := parseAlertTime(q.Get("to"), "to")
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if from != nil && to != nil && from.After(*to) {
		writeErr(w, http.StatusBadRequest, "from must not be after to")
		return
	}
	uid := s.scopeUserID(r)
	opts := store.AlertListOptions{
		Limit: limit, BeforeTime: beforeTime, BeforeID: beforeID,
		Status: status, Severity: severity, SourceType: sourceType,
		AgentID: q.Get("agent_id"), SiteID: q.Get("site_id"),
		ClientID: q.Get("client_id"), GroupID: q.Get("group_id"),
		Query: q.Get("q"), From: from, To: to, UserID: uid, Scoped: uid != "",
	}
	alerts, err := s.store.ListAlertsPage(r.Context(), opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := s.store.AlertCounts(r.Context(), uid, uid != "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	next := ""
	if len(alerts) > limit {
		alerts = alerts[:limit]
		next = alertCursor(&alerts[len(alerts)-1])
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts, "counts": counts, "next_cursor": next})
}

func (s *Server) requireAlertAccess(w http.ResponseWriter, r *http.Request, alertID string) (*models.Alert, bool) {
	if _, err := uuid.Parse(alertID); err != nil {
		writeErr(w, http.StatusNotFound, "alert not found")
		return nil, false
	}
	if uid := s.scopeUserID(r); uid != "" {
		allowed, err := s.store.AlertAllowedForUser(r.Context(), uid, alertID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return nil, false
		}
		if !allowed {
			writeErr(w, http.StatusForbidden, "alert is outside your access scope")
			return nil, false
		}
	}
	alert, err := s.store.GetAlert(r.Context(), alertID)
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return nil, false
	}
	return alert, true
}

func (s *Server) getAlert(w http.ResponseWriter, r *http.Request) {
	alert, ok := s.requireAlertAccess(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	events, err := s.store.ListAlertEvents(r.Context(), alert.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert": alert, "events": events})
}

func (s *Server) listAlertEvents(w http.ResponseWriter, r *http.Request) {
	alert, ok := s.requireAlertAccess(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	events, err := s.store.ListAlertEvents(r.Context(), alert.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

type alertActionRequest struct {
	Note            string     `json:"note"`
	ExpectedVersion *int64     `json:"expected_version"`
	SilenceUntil    *time.Time `json:"silence_until"`
}

func decodeAlertAction(r *http.Request) (alertActionRequest, error) {
	var in alertActionRequest
	err := decode(r, &in)
	if errors.Is(err, io.EOF) {
		err = nil
	}
	in.Note = strings.TrimSpace(in.Note)
	if len(in.Note) > 1000 {
		return in, fmt.Errorf("note must be at most 1000 characters")
	}
	if in.ExpectedVersion != nil && *in.ExpectedVersion < 1 {
		return in, fmt.Errorf("expected_version must be positive")
	}
	return in, err
}

func (s *Server) transitionAlert(w http.ResponseWriter, r *http.Request, action string) {
	id := chi.URLParam(r, "id")
	if _, ok := s.requireAlertAccess(w, r, id); !ok {
		return
	}
	in, err := decodeAlertAction(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if action == "suppress" {
		if in.SilenceUntil == nil || !in.SilenceUntil.After(time.Now()) || in.SilenceUntil.After(time.Now().AddDate(1, 0, 0)) {
			writeErr(w, http.StatusBadRequest, "silence_until must be in the future and at most one year away")
			return
		}
	}
	alert, err := s.store.TransitionAlert(r.Context(), id, store.AlertAction{
		Action: action, Actor: actorOf(r), Note: in.Note,
		ExpectedVersion: in.ExpectedVersion, SilenceUntil: in.SilenceUntil,
	})
	if err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "alert."+action, id,
		alertAuditDetail(map[string]any{"note": in.Note, "version": alert.Version}))
	writeJSON(w, http.StatusOK, alert)
}

func (s *Server) ackAlert(w http.ResponseWriter, r *http.Request) {
	s.transitionAlert(w, r, "ack")
}

func (s *Server) resolveAlert(w http.ResponseWriter, r *http.Request) {
	s.transitionAlert(w, r, "resolve")
}

func (s *Server) suppressAlert(w http.ResponseWriter, r *http.Request) {
	s.transitionAlert(w, r, "suppress")
}

func (s *Server) bulkAlertAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs              []string         `json:"ids"`
		Action           string           `json:"action"`
		Note             string           `json:"note"`
		SilenceUntil     *time.Time       `json:"silence_until"`
		ExpectedVersions map[string]int64 `json:"expected_versions"`
	}
	if err := decode(r, &in); err != nil || len(in.IDs) == 0 || len(in.IDs) > 100 ||
		!oneOf(in.Action, "ack", "resolve", "suppress") {
		writeErr(w, http.StatusBadRequest, "ids (1-100) and a supported action are required")
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if len(in.Note) > 1000 {
		writeErr(w, http.StatusBadRequest, "note must be at most 1000 characters")
		return
	}
	for id, version := range in.ExpectedVersions {
		if version < 1 {
			writeErr(w, http.StatusBadRequest, "expected version for "+id+" must be positive")
			return
		}
	}
	if in.Action == "suppress" && (in.SilenceUntil == nil || !in.SilenceUntil.After(time.Now()) || in.SilenceUntil.After(time.Now().AddDate(1, 0, 0))) {
		writeErr(w, http.StatusBadRequest, "silence_until must be in the future and at most one year away")
		return
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(in.IDs))
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		if _, ok := s.requireAlertAccess(w, r, id); !ok {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	updated := make([]models.Alert, 0, len(ids))
	failures := make([]map[string]string, 0)
	for _, id := range ids {
		var expected *int64
		if value, ok := in.ExpectedVersions[id]; ok {
			v := value
			expected = &v
		}
		alert, err := s.store.TransitionAlert(r.Context(), id, store.AlertAction{
			Action: in.Action, Actor: actorOf(r), Note: in.Note,
			ExpectedVersion: expected, SilenceUntil: in.SilenceUntil,
		})
		if err != nil {
			failures = append(failures, map[string]string{"id": id, "error": err.Error()})
			continue
		}
		updated = append(updated, *alert)
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "alert.bulk."+in.Action, "alerts",
		alertAuditDetail(map[string]any{"requested": len(ids), "updated": len(updated), "failed": len(failures)}))
	code := http.StatusOK
	if len(failures) > 0 {
		code = http.StatusMultiStatus
	}
	writeJSON(w, code, map[string]any{"alerts": updated, "errors": failures})
}

func validSuppressionScope(scopeType string) bool {
	return oneOf(scopeType, models.ScopeAgent, models.ScopeSite, models.ScopeClient,
		models.ScopeGroup, "check", "policy")
}

func (s *Server) requireSuppressionScope(w http.ResponseWriter, r *http.Request, scopeType, scopeID string) bool {
	if _, err := uuid.Parse(scopeID); err != nil || !validSuppressionScope(scopeType) {
		writeErr(w, http.StatusBadRequest, "invalid suppression scope")
		return false
	}
	switch scopeType {
	case models.ScopeAgent:
		return s.requireAgent(w, r, scopeID)
	case models.ScopeSite, models.ScopeClient, models.ScopeGroup:
		return s.requireScope(w, r, scopeType, scopeID)
	case "check":
		_, ok := s.requireCheck(w, r, scopeID, true)
		return ok
	case "policy":
		return s.requirePolicy(w, r, scopeID, true)
	default:
		return false
	}
}

func (s *Server) listAlertSuppressions(w http.ResponseWriter, r *http.Request) {
	uid := s.scopeUserID(r)
	activeOnly := r.URL.Query().Get("active") != "false"
	rows, err := s.store.ListAlertSuppressions(r.Context(), uid, uid != "", activeOnly)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suppressions": rows})
}

func (s *Server) createAlertSuppression(w http.ResponseWriter, r *http.Request) {
	var sup models.AlertSuppression
	if err := decode(r, &sup); err != nil || !validSuppressionScope(sup.ScopeType) ||
		!oneOf(sup.Kind, models.SuppressionSilence, models.SuppressionMaintenance) {
		writeErr(w, http.StatusBadRequest, "kind and scope are required")
		return
	}
	if !s.requireSuppressionScope(w, r, sup.ScopeType, sup.ScopeID) {
		return
	}
	now := time.Now()
	if sup.StartsAt.IsZero() {
		sup.StartsAt = now
	}
	sup.Reason = strings.TrimSpace(sup.Reason)
	if !sup.EndsAt.After(now) || !sup.EndsAt.After(sup.StartsAt) ||
		sup.EndsAt.After(now.AddDate(1, 0, 0)) || len(sup.Reason) > 1000 {
		writeErr(w, http.StatusBadRequest, "ends_at must be in the future, follow starts_at, be at most one year away, and reason at most 1000 characters")
		return
	}
	sup.CreatedBy = actorOf(r)
	if err := s.store.CreateAlertSuppression(r.Context(), &sup); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "alert.suppression.create", sup.ID,
		alertAuditDetail(map[string]any{"kind": sup.Kind, "scope_type": sup.ScopeType, "scope_id": sup.ScopeID, "ends_at": sup.EndsAt}))
	writeJSON(w, http.StatusCreated, &sup)
}

func (s *Server) cancelAlertSuppression(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	uid := s.scopeUserID(r)
	rows, err := s.store.ListAlertSuppressions(r.Context(), uid, uid != "", false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var found *models.AlertSuppression
	for i := range rows {
		if rows[i].ID == id {
			found = &rows[i]
			break
		}
	}
	if found == nil {
		writeErr(w, http.StatusNotFound, "suppression not found")
		return
	}
	// An unrestricted administrator may also clean up a stale rule whose target
	// was deleted. Restricted users still need control of the complete scope,
	// not merely visibility of one affected device.
	if s.scopeUserID(r) != "" && !s.requireSuppressionScope(w, r, found.ScopeType, found.ScopeID) {
		return
	}
	if err := s.store.CancelAlertSuppression(r.Context(), id, actorOf(r)); err != nil {
		writeErr(w, httpStatusFor(err), err.Error())
		return
	}
	_ = s.store.AddAudit(r.Context(), actorOf(r), "alert.suppression.cancel", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "canceled"})
}

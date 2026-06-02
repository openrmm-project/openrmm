// Package scheduler drives recurring work: it dispatches due monitoring checks
// and scheduled tasks to online agents, processes check results into statuses,
// and raises/resolves alerts.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

// offlineAfter is how long since last_seen before an agent is alerted offline.
const offlineAfter = 3 * time.Minute

// Scheduler evaluates checks/tasks and manages alerts.
type Scheduler struct {
	store *store.Store
	hub   *hub.Hub
	jobs  *jobs.Service
}

// New builds the scheduler.
func New(s *store.Store, h *hub.Hub, j *jobs.Service) *Scheduler {
	return &Scheduler{store: s, hub: h, jobs: j}
}

// Run ticks until the context is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		log.Printf("scheduler: list agents: %v", err)
		return
	}
	now := time.Now()
	for i := range agents {
		a := &agents[i]
		s.evaluateOffline(ctx, a, now)
		if !s.hub.IsOnline(a.ID) {
			continue // only dispatch to live agents
		}
		siteID, clientID, err := s.store.AgentScope(ctx, a.ID)
		if err != nil {
			continue
		}
		s.dispatchChecks(ctx, a.ID, siteID, clientID, now)
		s.dispatchTasks(ctx, a.ID, siteID, clientID, now)
	}
}

func (s *Scheduler) evaluateOffline(ctx context.Context, a *models.Agent, now time.Time) {
	online := s.hub.IsOnline(a.ID)
	id := a.ID
	if online {
		_ = s.store.ResolveAlertsForSource(ctx, "agent", &id, &id)
		return
	}
	if a.LastSeen != nil && now.Sub(*a.LastSeen) > offlineAfter {
		_ = s.store.RaiseAlert(ctx, &id, "agent", &id, models.SeverityError,
			fmt.Sprintf("Agent %s is offline (last seen %s)", a.Hostname, a.LastSeen.Format(time.RFC3339)))
	}
}

func (s *Scheduler) dispatchChecks(ctx context.Context, agentID string, siteID, clientID *string, now time.Time) {
	checks, err := s.store.ChecksForAgent(ctx, agentID, siteID, clientID)
	if err != nil {
		return
	}
	for i := range checks {
		c := &checks[i]
		last, _ := s.store.LastCheckResultTime(ctx, c.ID, agentID)
		if !last.IsZero() && now.Sub(last) < time.Duration(c.IntervalSec)*time.Second {
			continue
		}
		msg := protocol.RunCheckMsg{CheckID: c.ID, Type: c.Type, Config: c.Config}
		if frame, err := encode(protocol.MsgRunCheck, msg); err == nil {
			s.hub.Send(agentID, frame)
		}
	}
}

func (s *Scheduler) dispatchTasks(ctx context.Context, agentID string, siteID, clientID *string, now time.Time) {
	tasks, err := s.store.TasksForAgent(ctx, agentID, siteID, clientID)
	if err != nil {
		return
	}
	for i := range tasks {
		t := &tasks[i]
		last, _ := s.store.LastTaskRunTime(ctx, t.ID, agentID)
		if !s.taskDue(t, last, now) {
			continue
		}
		shell, script := t.Shell, t.Script
		if t.ScriptID != nil {
			if sc, err := s.store.GetScript(ctx, *t.ScriptID); err == nil {
				shell, script = sc.Shell, sc.Body
			}
		}
		tid := t.ID
		if _, err := s.jobs.RunAutomated(ctx, agentID, shell, script, "task: "+t.Name, &tid); err != nil {
			log.Printf("scheduler: run task %s: %v", t.ID, err)
		}
	}
}

func (s *Scheduler) taskDue(t *models.Task, last, now time.Time) bool {
	switch t.ScheduleKind {
	case models.ScheduleDaily:
		minuteOfDay := now.UTC().Hour()*60 + now.UTC().Minute()
		if minuteOfDay < t.RunAtMinute {
			return false
		}
		// Due once per UTC day.
		return last.IsZero() || last.UTC().YearDay() != now.UTC().YearDay() || last.UTC().Year() != now.UTC().Year()
	default: // interval
		if t.IntervalSec <= 0 {
			return false
		}
		return last.IsZero() || now.Sub(last) >= time.Duration(t.IntervalSec)*time.Second
	}
}

// OnCheckResult processes a raw check outcome from an agent: it computes the
// status from the check's thresholds, stores the result, and raises or resolves
// the corresponding alert.
func (s *Scheduler) OnCheckResult(ctx context.Context, agentID string, res protocol.CheckResultMsg) error {
	check, err := s.store.GetCheck(ctx, res.CheckID)
	if err != nil {
		return err
	}
	status := computeStatus(check, res)
	if err := s.store.AddCheckResult(ctx, &models.CheckResult{
		CheckID: res.CheckID, AgentID: agentID, Status: status, Value: res.Value, Output: res.Output,
	}); err != nil {
		return err
	}

	cid := res.CheckID
	aid := agentID
	switch status {
	case models.CheckFailing:
		return s.store.RaiseAlert(ctx, &aid, "check", &cid, models.SeverityError,
			fmt.Sprintf("Check %q is failing on the agent", check.Name))
	case models.CheckWarning:
		return s.store.RaiseAlert(ctx, &aid, "check", &cid, models.SeverityWarning,
			fmt.Sprintf("Check %q is in warning on the agent", check.Name))
	default: // passing
		return s.store.ResolveAlertsForSource(ctx, "check", &cid, &aid)
	}
}

// computeStatus applies the check's threshold semantics to a raw value.
func computeStatus(c *models.Check, res protocol.CheckResultMsg) string {
	switch c.Type {
	case models.CheckScript:
		if res.Value == nil {
			return models.CheckFailing
		}
		switch int(*res.Value) {
		case 0:
			return models.CheckPassing
		case 1:
			return models.CheckWarning
		default:
			return models.CheckFailing
		}
	case models.CheckPing, models.CheckWinService:
		if res.Success {
			return models.CheckPassing
		}
		return models.CheckFailing
	case models.CheckDiskSpace, models.CheckMemory:
		// value = free percent; alert when LOW.
		if res.Value == nil {
			return models.CheckFailing
		}
		v := *res.Value
		if c.ErrThreshold != nil && v <= *c.ErrThreshold {
			return models.CheckFailing
		}
		if c.WarnThreshold != nil && v <= *c.WarnThreshold {
			return models.CheckWarning
		}
		return models.CheckPassing
	case models.CheckCPULoad:
		// value = usage percent; alert when HIGH.
		if res.Value == nil {
			return models.CheckFailing
		}
		v := *res.Value
		if c.ErrThreshold != nil && v >= *c.ErrThreshold {
			return models.CheckFailing
		}
		if c.WarnThreshold != nil && v >= *c.WarnThreshold {
			return models.CheckWarning
		}
		return models.CheckPassing
	default:
		return models.CheckPassing
	}
}

func encode(typ string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protocol.Envelope{Type: typ, Data: data})
}

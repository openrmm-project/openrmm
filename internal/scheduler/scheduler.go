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
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

// offlineAfter is how long since last_seen before an agent is alerted offline.
const offlineAfter = 3 * time.Minute

// Patch cadence: how often the patch routine runs, and how stale a scan may be.
const (
	patchTickEvery    = 2 * time.Minute
	patchScanInterval = 6 * time.Hour
)

// oauthPurgeEvery is how often expired OAuth codes/tokens are swept.
const oauthPurgeEvery = time.Hour

// Scheduler evaluates checks/tasks and manages alerts.
type Scheduler struct {
	store          *store.Store
	hub            *hub.Hub
	jobs           *jobs.Service
	patches        *patches.Service
	lastPatchTick  time.Time
	lastOAuthPurge time.Time
}

// New builds the scheduler.
func New(s *store.Store, h *hub.Hub, j *jobs.Service, p *patches.Service) *Scheduler {
	return &Scheduler{store: s, hub: h, jobs: j, patches: p}
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
		if a.Status == models.AgentRevoked {
			continue
		}
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
	if now.Sub(s.lastPatchTick) >= patchTickEvery {
		s.lastPatchTick = now
		s.patchTick(ctx, agents, now)
	}
	if now.Sub(s.lastOAuthPurge) >= oauthPurgeEvery {
		s.lastOAuthPurge = now
		if n, err := s.store.PurgeExpiredOAuth(ctx); err != nil {
			log.Printf("scheduler: purge expired oauth: %v", err)
		} else if n > 0 {
			log.Printf("scheduler: purged %d expired oauth code(s)/token(s)", n)
		}
	}
}

// patchTick drives patch management: it requests scans from due agents,
// auto-approves per policy, and — inside a policy's maintenance window —
// installs approved patches and reboots when one is pending.
func (s *Scheduler) patchTick(ctx context.Context, agents []models.Agent, now time.Time) {
	due, _ := s.store.PatchScanDue(ctx, patchScanInterval)
	for _, id := range due {
		if s.hub.IsOnline(id) {
			s.patches.DispatchScan(id)
		}
	}
	pendingReboot := map[string]bool{}
	if ids, err := s.store.RebootPendingAgents(ctx); err == nil {
		for _, id := range ids {
			pendingReboot[id] = true
		}
	}
	for i := range agents {
		a := &agents[i]
		if a.Status == models.AgentRevoked {
			continue
		}
		pols, err := s.store.PatchPoliciesForAgent(ctx, a.ID)
		if err != nil || len(pols) == 0 {
			continue
		}
		for j := range pols {
			if _, err := s.store.AutoApproveForAgent(ctx, a.ID, &pols[j]); err != nil {
				log.Printf("patch auto-approve %s: %v", a.ID, err)
			}
		}
		if !s.hub.IsOnline(a.ID) {
			continue
		}
		open, rebootAllowed := windowState(pols, now)
		if !open {
			continue
		}
		if ids, _ := s.store.ApprovedIdentifiers(ctx, a.ID); len(ids) > 0 {
			if err := s.patches.DispatchInstall(ctx, a.ID, nil); err != nil {
				log.Printf("patch install %s: %v", a.ID, err)
			}
		} else if rebootAllowed && pendingReboot[a.ID] {
			// Nothing left to install; clear a pending reboot inside the window.
			live, err := s.store.HasLiveNativeRemoteSession(ctx, a.ID)
			if err != nil || live {
				continue // never interrupt an operator's active remote session automatically
			}
			if err := s.patches.DispatchReboot(ctx, a.ID, 60); err != nil {
				log.Printf("patch reboot %s: %v", a.ID, err)
			}
		}
	}
}

// windowState reports whether any policy's maintenance window is open now, and
// whether reboots are allowed by a policy whose window is open.
func windowState(pols []models.PatchPolicy, now time.Time) (open, rebootAllowed bool) {
	mins := now.Hour()*60 + now.Minute()
	wd := int(now.Weekday()) // 0=Sun..6=Sat
	for i := range pols {
		p := &pols[i]
		if len(p.WindowDays) > 0 && !containsInt(p.WindowDays, wd) {
			continue
		}
		if mins < p.WindowStartMin || mins > p.WindowEndMin {
			continue
		}
		open = true
		if p.Reboot != models.RebootNever {
			rebootAllowed = true
		}
	}
	return open, rebootAllowed
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func (s *Scheduler) evaluateOffline(ctx context.Context, a *models.Agent, now time.Time) {
	online := s.hub.IsOnline(a.ID)
	id := a.ID
	if online {
		_ = s.store.ResolveAlertsForSource(ctx, "agent", &id, &id)
		return
	}
	// The agent has no live connection: reconcile a row still marked online (an
	// unclean disconnect never ran detach, or a server restart left it stale) so
	// the console does not report an offline device as online.
	if a.Status == models.AgentOnline {
		_ = s.store.MarkAgentOffline(ctx, id)
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
	case models.ScheduleWeekly:
		// Find the most recent scheduled occurrence at or before now across
		// (active weekday × run_at_minutes), scanning back up to 8 days. Due when
		// that occurrence is newer than the last run — so each day/time slot fires
		// once and a missed slot still runs on the next tick.
		var recent time.Time
		for d := 0; d < 8; d++ {
			day := now.UTC().AddDate(0, 0, -d)
			if t.DaysOfWeek&(1<<uint(day.Weekday())) == 0 {
				continue
			}
			for _, m := range t.RunAtMinutes {
				occ := time.Date(day.Year(), day.Month(), day.Day(), m/60, m%60, 0, 0, time.UTC)
				if occ.After(now) {
					continue
				}
				if occ.After(recent) {
					recent = occ
				}
			}
		}
		if recent.IsZero() {
			return false
		}
		return last.Before(recent)
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

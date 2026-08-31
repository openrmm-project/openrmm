// Package scheduler drives recurring work: it dispatches due monitoring checks
// and scheduled tasks to online agents, processes check results into statuses,
// and raises/resolves alerts.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify"
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

const alertMaintenanceEvery = time.Minute
const monitoringRetentionEvery = 24 * time.Hour

// Scheduler evaluates checks/tasks and manages alerts.
type Scheduler struct {
	store                     *store.Store
	hub                       *hub.Hub
	jobs                      *jobs.Service
	patches                   *patches.Service
	notifier                  *notify.Service
	consoleURL                string
	lastPatchTick             time.Time
	lastOAuthPurge            time.Time
	lastAlertMaintenance      time.Time
	lastRetention             time.Time
	processingErrors          atomic.Uint64
	dispatchFailures          atomic.Uint64
	timedOutChecks            atomic.Uint64
	alertRetentionDays        int
	checkResultRetentionDays  int
	notificationRetentionDays int
	auditRetentionDays        int
}

type Stats struct {
	ProcessingErrors uint64
	DispatchFailures uint64
	TimedOutChecks   uint64
}

func (s *Scheduler) Stats() Stats {
	return Stats{
		ProcessingErrors: s.processingErrors.Load(),
		DispatchFailures: s.dispatchFailures.Load(),
		TimedOutChecks:   s.timedOutChecks.Load(),
	}
}

// New builds the scheduler.
func New(s *store.Store, h *hub.Hub, j *jobs.Service, p *patches.Service) *Scheduler {
	return &Scheduler{store: s, hub: h, jobs: j, patches: p,
		alertRetentionDays: 90, checkResultRetentionDays: 90, notificationRetentionDays: 90,
		auditRetentionDays: 90}
}

// WithAuditRetention bounds the machine-written audit entries. Unlike the
// monitoring knobs, zero is honoured rather than replaced with a default: an
// audit log that deletes nothing is a reasonable choice and must be reachable.
func (s *Scheduler) WithAuditRetention(days int) *Scheduler {
	s.auditRetentionDays = days
	return s
}

func (s *Scheduler) WithRetention(alertDays, checkResultDays, notificationDays int) *Scheduler {
	if alertDays > 0 {
		s.alertRetentionDays = alertDays
	}
	if checkResultDays > 0 {
		s.checkResultRetentionDays = checkResultDays
	}
	if notificationDays > 0 {
		s.notificationRetentionDays = notificationDays
	}
	return s
}

// WithNotifier enables email on alert transitions. Optional by design: with no
// notifier configured the scheduler behaves exactly as before, so email is
// additive rather than something the monitoring loop depends on.
func (s *Scheduler) WithNotifier(n *notify.Service, consoleURL string) *Scheduler {
	s.notifier = n
	s.consoleURL = consoleURL
	return s
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
	s.evaluateTimedOutChecks(ctx, now)
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
	if now.Sub(s.lastAlertMaintenance) >= alertMaintenanceEvery {
		s.lastAlertMaintenance = now
		s.reconcileCheckStates(ctx)
		if n, err := s.store.ResolveInapplicableCheckAlerts(ctx, now); err != nil {
			s.recordError("resolve inapplicable alerts", err)
		} else if n > 0 {
			log.Printf("scheduler: resolved %d inapplicable check alert(s)", n)
		}
		if n, err := s.store.ResolveRetiredAgentAlerts(ctx, now); err != nil {
			s.recordError("resolve retired device alerts", err)
		} else if n > 0 {
			log.Printf("scheduler: resolved %d retired device alert(s)", n)
		}
		if _, err := s.store.ApplyActiveAlertSuppressions(ctx, now, 500); err != nil {
			s.recordError("apply scheduled alert suppressions", err)
		}
		if alerts, err := s.store.ReactivateExpiredSuppressedAlerts(ctx, now, 100); err != nil {
			s.recordError("reactivate suppressed alerts", err)
		} else {
			for i := range alerts {
				if err := s.queueAlertNotification(ctx, &alerts[i], "Suppression expired while the condition is still active."); err != nil {
					s.recordError("enqueue unsuppressed alert "+alerts[i].ID, err)
				}
			}
		}
		s.deliverPendingAlertNotifications(ctx)
	}
	if now.Sub(s.lastRetention) >= monitoringRetentionEvery {
		s.lastRetention = now
		if purged, err := s.store.PurgeMonitoringData(ctx, now, s.alertRetentionDays,
			s.checkResultRetentionDays, s.notificationRetentionDays); err != nil {
			s.recordError("monitoring retention", err)
		} else if purged.Alerts+purged.Results+purged.Executions+purged.Notifications+purged.Suppressions > 0 {
			log.Printf("scheduler: retention purged alerts=%d results=%d executions=%d notifications=%d suppressions=%d",
				purged.Alerts, purged.Results, purged.Executions, purged.Notifications, purged.Suppressions)
		}
		// Only the machine-written entries. What a person did is kept whatever
		// its age — that record is why the log exists, and it is small.
		if purged, err := s.store.PurgeAuditLog(ctx, now, s.auditRetentionDays); err != nil {
			s.recordError("audit retention", err)
		} else if purged > 0 {
			log.Printf("scheduler: retention purged %d machine-written audit entries", purged)
		}
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

func (s *Scheduler) recordError(operation string, err error) {
	if err == nil {
		return
	}
	s.processingErrors.Add(1)
	log.Printf("scheduler: %s: %v", operation, err)
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
		resolved, err := s.store.ResolveAlertSource(ctx, models.AlertSourceAgent, &id, &id,
			"system", "automatic", "Agent reconnected.", now)
		if err != nil {
			s.recordError("resolve offline alert for "+id, err)
		} else if len(resolved) > 0 {
			if err := s.queueAlertNotification(ctx, &resolved[0], fmt.Sprintf("Agent %s is back online.", a.Hostname)); err != nil {
				s.recordError("enqueue agent recovery "+id, err)
			}
		}
		return
	}
	// The agent has no live connection: reconcile a row still marked online (an
	// unclean disconnect never ran detach, or a server restart left it stale) so
	// the console does not report an offline device as online.
	if a.Status == models.AgentOnline {
		if err := s.store.MarkAgentOffline(ctx, id); err != nil {
			s.recordError("mark agent offline "+id, err)
		}
	}
	if a.LastSeen != nil && now.Sub(*a.LastSeen) > offlineAfter {
		transition, err := s.store.ObserveAlert(ctx, store.AlertObservation{
			AgentID: &id, SourceType: models.AlertSourceAgent, SourceID: &id,
			ConditionKey: "connectivity", Severity: models.SeverityError,
			Message: fmt.Sprintf("Agent %s is offline (last seen %s)", a.Hostname, a.LastSeen.Format(time.RFC3339)),
			Output:  a.LastSeen.Format(time.RFC3339), Cooldown: 5 * time.Minute, ObservedAt: now,
		})
		if err != nil {
			s.recordError("raise offline alert for "+id, err)
			return
		}
		// Keep dependency alerts closed for the whole outage, including after a
		// server restart where the connectivity incident was already open.
		if _, err := s.store.ResolveCheckAlertsForOfflineAgent(ctx, id, now); err != nil {
			s.recordError("suppress dependent checks for "+id, err)
		}
		if transition.Notify {
			if err := s.queueAlertNotification(ctx, &transition.Alert,
				fmt.Sprintf("Agent %s stopped reporting (last seen %s).",
					a.Hostname, a.LastSeen.Format(time.RFC3339))); err != nil {
				s.recordError("enqueue offline alert "+id, err)
			}
		}
	}
}

func (s *Scheduler) dispatchChecks(ctx context.Context, agentID string, siteID, clientID *string, now time.Time) {
	checks, err := s.store.ChecksForAgent(ctx, agentID, siteID, clientID)
	if err != nil {
		s.recordError("list checks for "+agentID, err)
		return
	}
	for i := range checks {
		c := &checks[i]
		last, err := s.store.LastCheckActivity(ctx, c.ID, agentID)
		if err != nil {
			s.recordError("read last check result "+c.ID, err)
			continue
		}
		if !last.IsZero() && now.Sub(last) < time.Duration(c.IntervalSec)*time.Second {
			continue
		}
		execution, created, err := s.store.CreateCheckExecution(ctx, c.ID, agentID, now,
			time.Duration(c.TimeoutSec)*time.Second)
		if err != nil {
			s.recordError("create check execution "+c.ID, err)
			continue
		}
		if !created {
			continue
		}
		msg := protocol.RunCheckMsg{
			CheckID: c.ID, ExecutionID: execution.ID, Type: c.Type,
			Config: c.Config, TimeoutSec: c.TimeoutSec,
		}
		if frame, err := encode(protocol.MsgRunCheck, msg); err == nil {
			if !s.hub.Send(agentID, frame) {
				s.dispatchFailures.Add(1)
				log.Printf("scheduler: dispatch check %s to %s: agent queue unavailable", c.ID, agentID)
				if _, cancelErr := s.store.CancelPendingCheckExecutions(ctx, c.ID, &agentID); cancelErr != nil {
					s.recordError("cancel undelivered check execution "+c.ID, cancelErr)
				}
			}
		} else {
			s.recordError("encode check dispatch "+c.ID, err)
			if _, cancelErr := s.store.CancelPendingCheckExecutions(ctx, c.ID, &agentID); cancelErr != nil {
				s.recordError("cancel unencodable check execution "+c.ID, cancelErr)
			}
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

// OnCheckResult returns whether a durable agent result may be acknowledged. A
// transient database failure returns false so the agent keeps it in its outbox;
// duplicates and permanently stale definitions return true.
func (s *Scheduler) OnCheckResult(ctx context.Context, agentID string, res protocol.CheckResultMsg) (bool, error) {
	if _, err := uuid.Parse(res.CheckID); err != nil {
		return true, fmt.Errorf("check result has invalid check_id")
	}
	if res.ExecutionID != "" {
		if _, err := uuid.Parse(res.ExecutionID); err != nil {
			return true, fmt.Errorf("check result has invalid execution_id")
		}
	}
	if res.Value != nil && (math.IsNaN(*res.Value) || math.IsInf(*res.Value, 0)) {
		// PostgreSQL accepts non-finite doubles, but JSON does not. Do not let a
		// compromised endpoint poison history/detail responses with such a value.
		res.Value = nil
	}
	res.Output = truncateCheckOutput(res.Output)
	check, err := s.store.GetCheck(ctx, res.CheckID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return true, fmt.Errorf("discard result for removed check %s", res.CheckID)
		}
		return false, err
	}
	applies, err := s.store.CheckAppliesToAgent(ctx, res.CheckID, agentID)
	if err != nil {
		return false, err
	}
	if !applies {
		return true, fmt.Errorf("discard result for check %s outside device %s scope", res.CheckID, agentID)
	}
	previous := models.CheckUnknown
	if state, err := s.store.GetCheckState(ctx, res.CheckID, agentID); err == nil {
		previous = state.Status
	} else if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	status := computeStatusWithPrevious(check, res, previous)
	var executionID *string
	if res.ExecutionID != "" {
		executionID = &res.ExecutionID
	}
	state, disposition, err := s.store.RecordCheckResult(ctx, &models.CheckResult{
		CheckID: res.CheckID, AgentID: agentID, ExecutionID: executionID,
		Status: status, Value: res.Value, Output: res.Output,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrConflict) {
			return true, err
		}
		return false, err
	}
	if disposition == store.CheckResultStale {
		return true, nil
	}
	if err := s.reconcileCheckState(ctx, check, state); err != nil {
		return false, err
	}
	return true, nil
}

// checkStateObservationID identifies the check state an alert observation came
// from. Stable across retries of the same state and distinct between states,
// which is what lets a retry be recognised without guessing from content.
func checkStateObservationID(checkID, agentID string, updatedAt time.Time) string {
	return "check-state:" + checkID + ":" + agentID + ":" + strconv.FormatInt(updatedAt.UnixNano(), 10)
}

func (s *Scheduler) applyCheckState(ctx context.Context, check *models.Check, agentID string, state *models.CheckState, value *float64, output string, now time.Time) error {
	cid, aid := check.ID, agentID
	switch state.Status {
	case models.CheckFailing, models.CheckWarning:
		if state.ConsecutiveFailures < check.FailureCount {
			return nil
		}
		severity := models.SeverityWarning
		if state.Status == models.CheckFailing {
			severity = models.SeverityError
		}
		threshold := check.WarnThreshold
		if state.Status == models.CheckFailing {
			threshold = check.ErrThreshold
		}
		message := checkAlertMessage(check, state.Status, value, threshold)
		transition, err := s.store.ObserveAlert(ctx, store.AlertObservation{
			AgentID: &aid, SourceType: models.AlertSourceCheck, SourceID: &cid,
			ConditionKey: "health", Severity: severity, Message: message,
			ObservedValue: value, Threshold: threshold, Output: output,
			Cooldown: time.Duration(check.CooldownSec) * time.Second, ObservedAt: now,
			// Applying a check state can be retried when marking it reconciled
			// fails, and the retry must not add an occurrence. The state's own
			// identity says so — the same triple TryMarkCheckStateReconciled
			// uses to decide it is looking at the same state.
			ObservationID: checkStateObservationID(cid, aid, state.UpdatedAt),
		})
		if err != nil {
			return err
		}
		if transition.Notify {
			detail := message
			if transition.Escalated {
				detail = "Escalated: " + message
			}
			if err := s.queueAlertNotification(ctx, &transition.Alert, detail); err != nil {
				return err
			}
		}
		return nil
	case models.CheckPassing:
		if state.ConsecutiveSuccesses < check.RecoveryCount {
			return nil
		}
		resolved, err := s.store.ResolveAlertSource(ctx, models.AlertSourceCheck, &cid, &aid,
			"system", "automatic", "Check recovered after consecutive successful results.", now)
		if err != nil {
			return err
		}
		for i := range resolved {
			if err := s.queueAlertNotification(ctx, &resolved[i],
				fmt.Sprintf("Check %q is passing again.", check.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkAlertMessage(check *models.Check, status string, value, threshold *float64) string {
	label := "in warning"
	if status == models.CheckFailing {
		label = "failing"
	}
	message := fmt.Sprintf("Check %q is %s", check.Name, label)
	if value != nil && threshold != nil {
		message += fmt.Sprintf(": observed %.1f%%, threshold %.1f%%", *value, *threshold)
	}
	return message
}

func (s *Scheduler) evaluateTimedOutChecks(ctx context.Context, now time.Time) {
	for {
		executions, err := s.store.ExpireCheckExecutions(ctx, now, 100)
		if err != nil {
			s.recordError("expire check executions", err)
			return
		}
		if len(executions) == 0 {
			return
		}
		for i := range executions {
			e := &executions[i]
			s.timedOutChecks.Add(1)
			if !s.hub.IsOnline(e.AgentID) {
				if _, err := s.store.MarkCheckUnknown(ctx, e.CheckID, e.AgentID,
					"No result received because the device is offline.", now); err != nil {
					s.recordError("mark offline check unknown "+e.CheckID, err)
				}
				continue
			}
			applies, err := s.store.CheckAppliesToAgent(ctx, e.CheckID, e.AgentID)
			if err != nil {
				s.recordError("validate timed out check "+e.CheckID, err)
				continue
			}
			if !applies {
				continue
			}
			check, err := s.store.GetCheck(ctx, e.CheckID)
			if err != nil {
				s.recordError("load timed out check "+e.CheckID, err)
				continue
			}
			output := fmt.Sprintf("No result before the %s deadline.", time.Duration(check.TimeoutSec)*time.Second)
			state, applied, err := s.store.RecordCheckTimeout(ctx, e, output)
			if err != nil {
				s.recordError("record check timeout "+e.CheckID, err)
				continue
			}
			if !applied {
				continue
			}
			if err := s.reconcileCheckState(ctx, check, state); err != nil {
				s.recordError("apply check timeout "+e.CheckID, err)
			}
		}
		if len(executions) < 100 {
			return
		}
	}
}

func (s *Scheduler) reconcileCheckState(ctx context.Context, check *models.Check, state *models.CheckState) error {
	if state == nil {
		return nil
	}
	candidate := state
	force := false
	for attempt := 0; attempt < 5; attempt++ {
		latest, err := s.store.GetCheckState(ctx, candidate.CheckID, candidate.AgentID)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !latest.UpdatedAt.Equal(candidate.UpdatedAt) {
			candidate = latest
		}
		if !force && candidate.ReconciledAt != nil && !candidate.ReconciledAt.Before(candidate.UpdatedAt) {
			return nil
		}
		when := candidate.UpdatedAt
		if when.IsZero() {
			when = time.Now()
		}
		if err := s.applyCheckState(ctx, check, candidate.AgentID, candidate,
			candidate.LastValue, candidate.LastOutput, when); err != nil {
			return err
		}
		marked, err := s.store.TryMarkCheckStateReconciled(ctx, candidate.CheckID,
			candidate.AgentID, candidate.UpdatedAt)
		if err != nil {
			return err
		}
		if marked {
			return nil
		}
		// A newer result landed after the freshness read but before completion.
		// Its alert transition must run even if another worker already marked the
		// newer state reconciled, because the stale side effect above happened last.
		force = true
	}
	return fmt.Errorf("check state kept changing during alert reconciliation")
}

func (s *Scheduler) reconcileCheckStates(ctx context.Context) {
	for {
		states, err := s.store.ListUnreconciledCheckStates(ctx, 100)
		if err != nil {
			s.recordError("list unreconciled check states", err)
			return
		}
		if len(states) == 0 {
			return
		}
		progress := 0
		for i := range states {
			state := &states[i]
			check, err := s.store.GetCheck(ctx, state.CheckID)
			if errors.Is(err, store.ErrNotFound) {
				_, _ = s.store.TryMarkCheckStateReconciled(ctx, state.CheckID, state.AgentID, state.UpdatedAt)
				progress++
				continue
			}
			if err != nil {
				s.recordError("load unreconciled check "+state.CheckID, err)
				continue
			}
			applies, err := s.store.CheckAppliesToAgent(ctx, state.CheckID, state.AgentID)
			if err != nil {
				s.recordError("validate unreconciled check "+state.CheckID, err)
				continue
			}
			if !applies {
				_, _ = s.store.TryMarkCheckStateReconciled(ctx, state.CheckID, state.AgentID, state.UpdatedAt)
				progress++
				continue
			}
			if err := s.reconcileCheckState(ctx, check, state); err != nil {
				s.recordError("reconcile check state "+state.CheckID, err)
				continue
			}
			progress++
		}
		if len(states) < 100 || progress == 0 {
			return
		}
	}
}

// queueAlertNotification copies one durable transition into the per-recipient
// outbox and only then clears the alert's pending marker. Its sequence makes a
// retry idempotent even if the first attempt queued only some recipients.
func (s *Scheduler) queueAlertNotification(ctx context.Context, alert *models.Alert, detail string) error {
	if alert == nil || !alert.NotificationPending {
		return nil
	}
	if alert.AgentSnapshotID == nil {
		return s.store.MarkAlertNotificationQueued(ctx, alert.ID, alert.NotificationSeq, time.Now())
	}
	agentID := *alert.AgentSnapshotID
	hostname := alert.AgentHostname
	if hostname == "" {
		hostname, _ = s.agentLabel(ctx, agentID)
	}
	if detail == "" {
		detail = alert.Message
		if alert.NotificationKind == models.AlertEventResolved && alert.ResolutionNote != "" {
			detail = alert.ResolutionNote
		}
	}
	if s.notifier != nil {
		var event notify.Event
		if alert.NotificationKind == models.AlertEventResolved {
			eventType := store.NotifyCheckRecovered
			if alert.SourceType == models.AlertSourceAgent {
				eventType = store.NotifyAgentRecovered
			}
			event = notify.AlertResolvedEvent(eventType, agentID, hostname, alert.ID, detail, s.consoleURL)
		} else {
			eventType := store.NotifyCheckFailed
			if alert.SourceType == models.AlertSourceAgent {
				eventType = store.NotifyAgentOffline
			}
			event = notify.AlertOpenedEvent(eventType, agentID, hostname, alert.ID, detail, s.consoleURL)
		}
		event.DedupeKey = fmt.Sprintf("alert:%s:%d", alert.ID, alert.NotificationSeq)
		if err := s.notifier.Queue(ctx, event); err != nil {
			return err
		}
	}
	return s.store.MarkAlertNotificationQueued(ctx, alert.ID, alert.NotificationSeq, time.Now())
}

func (s *Scheduler) deliverPendingAlertNotifications(ctx context.Context) {
	alerts, err := s.store.PendingAlertNotifications(ctx, 100)
	if err != nil {
		s.recordError("list pending alert notifications", err)
		return
	}
	for i := range alerts {
		if err := s.queueAlertNotification(ctx, &alerts[i], ""); err != nil {
			s.recordError("enqueue alert transition "+alerts[i].ID, err)
		}
	}
}

// agentLabel resolves the human-readable device name for the message body. A
// lookup failure degrades to the id rather than dropping the notification.
func (s *Scheduler) agentLabel(ctx context.Context, agentID string) (hostname, consoleURL string) {
	hostname = agentID
	if a, err := s.store.GetAgent(ctx, agentID); err == nil && a.Hostname != "" {
		hostname = a.Hostname
	}
	return hostname, s.consoleURL
}

// computeStatus applies the check's threshold semantics to a raw value.
func computeStatus(c *models.Check, res protocol.CheckResultMsg) string {
	return computeStatusWithPrevious(c, res, models.CheckUnknown)
}

func computeStatusWithPrevious(c *models.Check, res protocol.CheckResultMsg, previous string) string {
	switch c.Type {
	case models.CheckScript:
		if res.Value == nil || math.IsNaN(*res.Value) || math.IsInf(*res.Value, 0) || math.Trunc(*res.Value) != *res.Value {
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
		if !res.Success || !validCheckPercent(res.Value) ||
			!validCheckPercent(c.ErrThreshold) || !validCheckPercent(c.WarnThreshold) ||
			*c.ErrThreshold >= *c.WarnThreshold || math.IsNaN(c.Hysteresis) || math.IsInf(c.Hysteresis, 0) {
			return models.CheckFailing
		}
		v := *res.Value
		if previous == models.CheckFailing && v <= *c.ErrThreshold+c.Hysteresis {
			return models.CheckFailing
		}
		if v <= *c.ErrThreshold {
			return models.CheckFailing
		}
		if previous == models.CheckWarning && v <= *c.WarnThreshold+c.Hysteresis {
			return models.CheckWarning
		}
		if v <= *c.WarnThreshold {
			return models.CheckWarning
		}
		return models.CheckPassing
	case models.CheckCPULoad:
		// value = usage percent; alert when HIGH.
		if !res.Success || !validCheckPercent(res.Value) ||
			!validCheckPercent(c.ErrThreshold) || !validCheckPercent(c.WarnThreshold) ||
			*c.ErrThreshold <= *c.WarnThreshold || math.IsNaN(c.Hysteresis) || math.IsInf(c.Hysteresis, 0) {
			return models.CheckFailing
		}
		v := *res.Value
		if previous == models.CheckFailing && v >= *c.ErrThreshold-c.Hysteresis {
			return models.CheckFailing
		}
		if v >= *c.ErrThreshold {
			return models.CheckFailing
		}
		if previous == models.CheckWarning && v >= *c.WarnThreshold-c.Hysteresis {
			return models.CheckWarning
		}
		if v >= *c.WarnThreshold {
			return models.CheckWarning
		}
		return models.CheckPassing
	default:
		// An unknown type is a configuration failure, never a healthy result.
		return models.CheckFailing
	}
}

func validCheckPercent(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 100
}

const maxCheckOutputRunes = 4000

func truncateCheckOutput(output string) string {
	runes := []rune(output)
	if len(runes) <= maxCheckOutputRunes {
		return output
	}
	return string(runes[:maxCheckOutputRunes]) + "\n[output truncated]"
}

func encode(typ string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protocol.Envelope{Type: typ, Data: data})
}

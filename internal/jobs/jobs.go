// Package jobs orchestrates the job lifecycle: proposal -> approval -> dispatch
// -> result, with an audit trail at every state change.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

// Store is the persistence surface the job service needs. *store.Store satisfies
// it; tests use an in-memory fake. Defining it here (consumer side) keeps the
// service decoupled from the concrete DB and unit-testable without Postgres.
type Store interface {
	GetAgent(ctx context.Context, id string) (*models.Agent, error)
	CreateJob(ctx context.Context, j *models.Job) (*models.Job, error)
	GetJob(ctx context.Context, id string) (*models.Job, error)
	SetJobStatus(ctx context.Context, id, status string, approvedBy *string) error
	TransitionJobStatus(ctx context.Context, id, from, to string, actor *string) (bool, error)
	SetJobResult(ctx context.Context, id, status string, exitCode int, stdout, stderr string) error
	ListDispatchableJobs(ctx context.Context, agentID string) ([]models.Job, error)
	ListJobsByBatch(ctx context.Context, batchID string) ([]models.Job, error)
	ClientForAgents(ctx context.Context, agentIDs []string) (map[string]string, error)
	AgentVersions(ctx context.Context, agentIDs []string) (map[string]store.AgentVersionInfo, error)
	AddAudit(ctx context.Context, actor, action, target string, detail []byte) error
}

// Sender pushes a frame to a connected agent, reporting whether it was online.
// *hub.Hub satisfies it.
type Sender interface {
	Send(agentID string, msg []byte) bool
}

// Notifier queues an operator email for a failed job. Declared here as a narrow
// consumer-side interface for the same reason as Store: the job service should
// not grow a dependency on the mail stack to report one event.
type Notifier interface {
	QueueJobFailed(ctx context.Context, agentID, hostname, jobID, description string)
}

// AutonomyStore is what jobs the AI starts without approval need beyond Store
// (docs/23-ai-autonomy.md). *store.Store satisfies it; a store without it
// refuses autonomous jobs rather than running them unlimited.
type AutonomyStore interface {
	CreateAutonomousJob(ctx context.Context, j *models.Job, limits store.AutonomyLimits) (*models.Job, error)
	RecordAIScriptOutcome(ctx context.Context, id string, success bool, limit int) (bool, error)
	GetScript(ctx context.Context, id string) (*models.Script, error)
}

// ReleaseNotifier tells people when a script's release for the AI was
// withdrawn after repeated failures. Optional, like Notifier.
type ReleaseNotifier interface {
	QueueAIReleaseRevoked(ctx context.Context, agentID, hostname, scriptID, scriptName string, failures int)
}

// ReleaseFailureLimit is how many autonomous runs of a released script may fail
// in a row before its release is withdrawn.
const ReleaseFailureLimit = 3

// Service coordinates jobs across the store and the live-agent hub.
type Service struct {
	store    Store
	hub      Sender
	notifier Notifier
}

// New builds a job service.
func New(s Store, h Sender) *Service {
	return &Service{store: s, hub: h}
}

// WithNotifier enables email on job failure. Optional: with no notifier the
// service behaves exactly as before, so mail is additive to job handling rather
// than something the lifecycle depends on.
func (s *Service) WithNotifier(n Notifier) *Service {
	s.notifier = n
	return s
}

// agentCanRunShell refuses a shell the target agent is too old to execute.
//
// Checked before the job exists rather than left to the agent, which answers
// "unsupported shell" only after dispatch — so the operator waits for a result
// to learn something the server already knew. The message names the version to
// install, because "not supported" without a remedy is a dead end.
func agentCanRunShell(a *models.Agent, shell string) error {
	minimum := models.MinAgentVersionFor(shell)
	if minimum == "" || models.AgentVersionAtLeast(a.Version, minimum) {
		return nil
	}
	reported := a.Version
	if strings.TrimSpace(reported) == "" {
		reported = "unknown"
	}
	return fmt.Errorf("%s runs %s scripts only from agent %s or later; it reports %s",
		a.Hostname, shell, minimum, reported)
}

// agentAcceptsAutonomy refuses a device too old for device queries or for a
// script's parameters. An older agent would answer a query with "unsupported
// shell", and would run a parameterised script without its parameters.
func agentAcceptsAutonomy(a *models.Agent) error {
	if models.AgentVersionAtLeast(a.Version, models.MinAgentVersionAutonomy) {
		return nil
	}
	reported := a.Version
	if strings.TrimSpace(reported) == "" {
		reported = "unknown"
	}
	return fmt.Errorf("%s runs device queries and parameterised scripts only from agent %s or later; it reports %s",
		a.Hostname, models.MinAgentVersionAutonomy, reported)
}

// usableAgent loads a job's target and refuses a revoked one.
func (s *Service) usableAgent(ctx context.Context, agentID string) (*models.Agent, error) {
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if a.Status == models.AgentRevoked {
		return nil, fmt.Errorf("agent is revoked")
	}
	return a, nil
}

// ProposeQuery creates an approval-gated device query: what the AI does when
// the caller's role does not allow it to query on its own. Once approved it
// runs exactly like an autonomous one.
func (s *Service) ProposeQuery(ctx context.Context, agentID string, req protocol.DeviceQueryRequest, description, createdBy string) (*models.Job, error) {
	a, err := s.usableAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if err := agentAcceptsAutonomy(a); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	j, err := s.store.CreateJob(ctx, &models.Job{
		AgentID: agentID, Type: models.JobDeviceQuery, Description: description, Script: string(raw),
		Status: models.JobAwaitingApproval, RequiresApproval: true, CreatedBy: createdBy,
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, createdBy, "job.propose", j.ID, map[string]any{"agent": agentID, "host": a.Hostname, "query": req.Query, "requires_approval": true})
	return s.store.GetJob(ctx, j.ID)
}

// ProposeScriptWithParams creates an approval-gated run of a library script
// with declared parameters: what the AI does with a released script when its
// role may not run releases on its own.
func (s *Service) ProposeScriptWithParams(ctx context.Context, agentID string, sc *models.Script, params map[string]string, description, createdBy string) (*models.Job, error) {
	a, err := s.usableAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if err := agentCanRunShell(a, sc.Shell); err != nil {
		return nil, err
	}
	if len(params) > 0 {
		if err := agentAcceptsAutonomy(a); err != nil {
			return nil, err
		}
	}
	scriptID := sc.ID
	j, err := s.store.CreateJob(ctx, &models.Job{
		AgentID: agentID, Type: models.JobRunScript, Description: description, Shell: sc.Shell, Script: sc.Body,
		Status: models.JobAwaitingApproval, RequiresApproval: true, CreatedBy: createdBy,
		ScriptID: &scriptID, Params: params,
	})
	if err != nil {
		return nil, err
	}
	s.audit(ctx, createdBy, "job.propose", j.ID, map[string]any{"agent": agentID, "host": a.Hostname, "shell": sc.Shell, "script_id": sc.ID, "requires_approval": true})
	return s.store.GetJob(ctx, j.ID)
}

// StartAutonomous creates and dispatches a job the AI runs without approval at
// the time: a device query its creator's role allows, or a script an
// administrator released (docs/23-ai-autonomy.md). basis is recorded as the
// job's approver and in the audit trail, so the device history says why it ran.
//
// The caller has already checked the permission, the scope and, for a script,
// the release. This re-checks the device and applies limits atomically.
func (s *Service) StartAutonomous(ctx context.Context, j *models.Job, basis string, limits store.AutonomyLimits) (*models.Job, error) {
	as, ok := s.store.(AutonomyStore)
	if !ok {
		return nil, fmt.Errorf("autonomous jobs are not available")
	}
	a, err := s.usableAgent(ctx, j.AgentID)
	if err != nil {
		return nil, err
	}
	if err := agentAcceptsAutonomy(a); err != nil {
		return nil, err
	}
	switch j.Autonomy {
	case models.AutonomyQuery:
		if j.Type != models.JobDeviceQuery {
			return nil, fmt.Errorf("an autonomous query must be a device query")
		}
	case models.AutonomyPreapproved:
		if j.Type != models.JobRunScript || j.ScriptID == nil {
			return nil, fmt.Errorf("a pre-approved action must run a library script")
		}
		if err := agentCanRunShell(a, j.Shell); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown autonomy %q", j.Autonomy)
	}
	j.Status = models.JobPending
	j.RequiresApproval = false
	j.ApprovedBy = &basis
	created, err := as.CreateAutonomousJob(ctx, j, limits)
	if err != nil {
		return nil, err
	}
	detail := map[string]any{"agent": created.AgentID, "host": a.Hostname, "autonomy": created.Autonomy, "basis": basis}
	if created.ScriptID != nil {
		detail["script_id"] = *created.ScriptID
	}
	if len(created.Params) > 0 {
		detail["params"] = created.Params
	}
	if created.Type == models.JobDeviceQuery {
		var req protocol.DeviceQueryRequest
		if json.Unmarshal([]byte(created.Script), &req) == nil {
			detail["query"] = req.Query
			if len(req.Params) > 0 {
				detail["params"] = req.Params
			}
		}
	}
	s.audit(ctx, created.CreatedBy, "job.autonomous", created.ID, detail)
	// An autonomous job runs now or not at all. Left pending for a device that
	// is offline, it would run whenever the device came back — after autonomy
	// was paused, or the release withdrawn, perhaps hours later.
	msg := protocol.JobMsg{JobID: created.ID, Type: created.Type, Shell: created.Shell, Script: created.Script, Params: created.Params}
	frame, _ := encode(protocol.MsgJob, msg)
	if !s.hub.Send(created.AgentID, frame) {
		_ = s.store.SetJobResult(ctx, created.ID, models.JobFailed, -1, "",
			"the device was offline: an action that runs without approval does not wait for it to come back")
		return s.store.GetJob(ctx, created.ID)
	}
	_ = s.store.SetJobStatus(ctx, created.ID, models.JobSent, nil)
	return s.store.GetJob(ctx, created.ID)
}

// AutonomousRedeliveryWindow is how long after creation an autonomous job may
// still be re-sent to an agent that reconnects. Past it, the job is not
// delivered: whoever allowed it at that moment may no longer allow it.
const AutonomousRedeliveryWindow = 5 * time.Minute

// Propose creates a job. If requireApproval is true the job lands in
// awaiting_approval and is NOT dispatched until a human approves it.
func (s *Service) Propose(ctx context.Context, agentID, shell, script, description, createdBy string, requireApproval bool) (*models.Job, error) {
	if !models.SupportedShell(shell) {
		return nil, fmt.Errorf("unsupported shell %q", shell)
	}
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if a.Status == models.AgentRevoked {
		return nil, fmt.Errorf("agent is revoked")
	}
	if err := agentCanRunShell(a, shell); err != nil {
		return nil, err
	}
	status := models.JobAwaitingApproval
	if !requireApproval {
		status = models.JobPending
	}
	j := &models.Job{
		AgentID:          agentID,
		Type:             models.JobRunScript,
		Description:      description,
		Shell:            shell,
		Script:           script,
		Status:           status,
		RequiresApproval: requireApproval,
		CreatedBy:        createdBy,
	}
	j, err = s.store.CreateJob(ctx, j)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, createdBy, "job.propose", j.ID, map[string]any{"agent": agentID, "host": s.hostname(ctx, agentID), "shell": shell, "requires_approval": requireApproval})
	if !requireApproval {
		s.dispatch(ctx, j)
	}
	return s.store.GetJob(ctx, j.ID)
}

// RunAutomated creates a pre-approved job (from a scheduled task or trusted
// automation) and dispatches it immediately. No human approval — use only for
// admin-defined automation, never for AI-authored scripts.
func (s *Service) RunAutomated(ctx context.Context, agentID, shell, script, description string, taskID *string) (*models.Job, error) {
	if !models.SupportedShell(shell) {
		return nil, fmt.Errorf("unsupported shell %q", shell)
	}
	a, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	if a.Status == models.AgentRevoked {
		return nil, fmt.Errorf("agent is revoked")
	}
	if err := agentCanRunShell(a, shell); err != nil {
		return nil, err
	}
	j := &models.Job{
		AgentID:          agentID,
		Type:             models.JobRunScript,
		Description:      description,
		Shell:            shell,
		Script:           script,
		Status:           models.JobPending,
		RequiresApproval: false,
		CreatedBy:        "scheduler",
		TaskID:           taskID,
	}
	j, err = s.store.CreateJob(ctx, j)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "scheduler", "job.automated", j.ID, map[string]any{"agent": agentID, "host": s.hostname(ctx, agentID), "task": taskID, "shell": shell})
	s.dispatch(ctx, j)
	return s.store.GetJob(ctx, j.ID)
}

// Approve transitions an awaiting_approval job to pending and dispatches it.
func (s *Service) Approve(ctx context.Context, jobID, approver string) (*models.Job, error) {
	j, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if j.Status != models.JobAwaitingApproval {
		return nil, fmt.Errorf("job is %s, cannot approve", j.Status)
	}
	// The check above is for the message; this conditional write is the guard.
	// A reject landing between the two must win, not be overwritten.
	if err := s.decide(ctx, jobID, models.JobPending, approver, "approve"); err != nil {
		return nil, err
	}
	s.audit(ctx, approver, "job.approve", jobID, map[string]any{"agent": j.AgentID, "host": s.hostname(ctx, j.AgentID)})
	j, _ = s.store.GetJob(ctx, jobID)
	s.dispatch(ctx, j)
	return s.store.GetJob(ctx, jobID)
}

// Reject declines a proposed job.
func (s *Service) Reject(ctx context.Context, jobID, actor string) (*models.Job, error) {
	j, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if j.Status != models.JobAwaitingApproval {
		return nil, fmt.Errorf("job is %s, cannot reject", j.Status)
	}
	if err := s.decide(ctx, jobID, models.JobRejected, actor, "reject"); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "job.reject", jobID, map[string]any{"agent": j.AgentID, "host": s.hostname(ctx, j.AgentID)})
	return s.store.GetJob(ctx, jobID)
}

// decide records an approval decision only while the job still awaits one.
func (s *Service) decide(ctx context.Context, jobID, to, actor, verb string) error {
	ok, err := s.store.TransitionJobStatus(ctx, jobID, models.JobAwaitingApproval, to, &actor)
	if err != nil {
		return err
	}
	if !ok {
		status := "no longer awaiting approval"
		if j, err := s.store.GetJob(ctx, jobID); err == nil {
			status = j.Status
		}
		return fmt.Errorf("job is %s, cannot %s", status, verb)
	}
	return nil
}

// checkFleetCanRunShell refuses the deployment if any target is too old for the
// shell, naming the devices rather than a count: "3 devices are outdated" sends
// an operator hunting through a selection of two hundred.
func (s *Service) checkFleetCanRunShell(ctx context.Context, agentIDs []string, shell string) error {
	minimum := models.MinAgentVersionFor(shell)
	if minimum == "" || len(agentIDs) == 0 {
		return nil
	}
	versions, err := s.store.AgentVersions(ctx, agentIDs)
	if err != nil {
		return err
	}
	var outdated []string
	for _, id := range agentIDs {
		info, known := versions[id]
		if !known || !models.AgentVersionAtLeast(info.Version, minimum) {
			label := id
			if known && info.Hostname != "" {
				label = info.Hostname
			}
			reported := "unknown"
			if known && strings.TrimSpace(info.Version) != "" {
				reported = info.Version
			}
			outdated = append(outdated, label+" ("+reported+")")
		}
	}
	if len(outdated) == 0 {
		return nil
	}
	shown := outdated
	suffix := ""
	// A deployment can target hundreds; an error listing all of them is not read.
	if len(shown) > 10 {
		shown, suffix = shown[:10], fmt.Sprintf(" and %d more", len(outdated)-10)
	}
	return fmt.Errorf("%s scripts need agent %s or later: update %s%s",
		shell, minimum, strings.Join(shown, ", "), suffix)
}

// ProposeBatch creates one approval-gated job per agent, all sharing a fresh
// batch_id, for a bulk/mass deployment. Nothing is dispatched until the batch is
// approved (ApproveBatch) — the same human-in-the-loop guardrail, once for many.
func (s *Service) ProposeBatch(ctx context.Context, agentIDs []string, shell, script, description, createdBy string) ([]models.Job, string, error) {
	if !models.SupportedShell(shell) {
		return nil, "", fmt.Errorf("unsupported shell %q", shell)
	}
	batchID := uuid.NewString()
	out := make([]models.Job, 0, len(agentIDs))
	for _, agentID := range agentIDs {
		j := &models.Job{
			AgentID:          agentID,
			Type:             models.JobRunScript,
			Description:      description,
			Shell:            shell,
			Script:           script,
			Status:           models.JobAwaitingApproval,
			RequiresApproval: true,
			CreatedBy:        createdBy,
			BatchID:          &batchID,
		}
		created, err := s.store.CreateJob(ctx, j)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *created)
	}
	s.audit(ctx, createdBy, "job.batch.propose", batchID, map[string]any{"agents": len(out), "shell": shell})
	return out, batchID, nil
}

// BatchInfo describes one per-company batch produced by ProposeBulk.
type BatchInfo struct {
	ClientID string   `json:"client_id,omitempty"`
	BatchID  string   `json:"batch_id"`
	JobIDs   []string `json:"job_ids"`
}

// ProposeBulk is the single engine for mass deployment: it splits the target
// agents BY COMPANY (a deployment never spans companies) and creates one
// approval-gated batch per company. Agents with no company (no site) form their
// own batch. Callers pass already-resolved + access-scoped agent ids. Shared by
// the REST /jobs/bulk handler and the AI propose_bulk_deploy tool.
func (s *Service) ProposeBulk(ctx context.Context, agentIDs []string, shell, script, description, createdBy string) ([]BatchInfo, error) {
	// Checked for the whole target set before anything is created, and the whole
	// deployment is refused if any device is too old. Creating the compatible
	// jobs and dropping the rest would leave a half-targeted deployment behind
	// an approval the operator granted for a different set of machines — so the
	// answer names the devices to update, and they decide whether to update them
	// or narrow the selection.
	if err := s.checkFleetCanRunShell(ctx, agentIDs, shell); err != nil {
		return nil, err
	}
	clientOf, err := s.store.ClientForAgents(ctx, agentIDs)
	if err != nil {
		return nil, err
	}
	byCompany := map[string][]string{}
	var order []string
	for _, id := range agentIDs {
		c := clientOf[id] // "" = no company (unassigned)
		if _, seen := byCompany[c]; !seen {
			order = append(order, c)
		}
		byCompany[c] = append(byCompany[c], id)
	}
	out := make([]BatchInfo, 0, len(order))
	for _, c := range order {
		jobsCreated, batchID, err := s.ProposeBatch(ctx, byCompany[c], shell, script, description, createdBy)
		if err != nil {
			return nil, err
		}
		ids := make([]string, len(jobsCreated))
		for i := range jobsCreated {
			ids[i] = jobsCreated[i].ID
		}
		out = append(out, BatchInfo{ClientID: c, BatchID: batchID, JobIDs: ids})
	}
	return out, nil
}

// ApproveBatch approves every still-awaiting job in a batch and dispatches each.
// Targets already rejected/removed are skipped. Returns how many were approved.
func (s *Service) ApproveBatch(ctx context.Context, batchID, approver string) (int, error) {
	jobs, err := s.store.ListJobsByBatch(ctx, batchID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range jobs {
		if jobs[i].Status != models.JobAwaitingApproval {
			continue
		}
		if _, err := s.Approve(ctx, jobs[i].ID, approver); err == nil {
			n++
		}
	}
	s.audit(ctx, approver, "job.batch.approve", batchID, map[string]any{"approved": n})
	return n, nil
}

// RejectBatch rejects every still-awaiting job in a batch (cancels the deployment
// before anything runs). Returns how many were rejected.
func (s *Service) RejectBatch(ctx context.Context, batchID, actor string) (int, error) {
	jobs, err := s.store.ListJobsByBatch(ctx, batchID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range jobs {
		if jobs[i].Status != models.JobAwaitingApproval {
			continue
		}
		if _, err := s.Reject(ctx, jobs[i].ID, actor); err == nil {
			n++
		}
	}
	s.audit(ctx, actor, "job.batch.reject", batchID, map[string]any{"rejected": n})
	return n, nil
}

// Cancel stops a job. Proposals (not yet dispatched) are rejected; in-flight
// jobs get a job_cancel pushed to the agent, which kills the process and reports
// the result. If the agent is offline the job is finalized as failed here.
func (s *Service) Cancel(ctx context.Context, jobID, actor string) (*models.Job, error) {
	j, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	switch j.Status {
	case models.JobCompleted, models.JobFailed, models.JobRejected:
		return nil, fmt.Errorf("job is %s, cannot cancel", j.Status)
	case models.JobAwaitingApproval:
		return s.Reject(ctx, jobID, actor)
	}
	frame, _ := encode(protocol.MsgJobCancel, protocol.JobCancelMsg{JobID: jobID})
	delivered := s.hub.Send(j.AgentID, frame)
	s.audit(ctx, actor, "job.cancel", jobID, map[string]any{"agent": j.AgentID, "host": s.hostname(ctx, j.AgentID), "delivered": delivered})
	if !delivered {
		_ = s.store.SetJobResult(ctx, jobID, models.JobFailed, -1, "", "canceled (agent offline)")
	}
	return s.store.GetJob(ctx, jobID)
}

// CompleteManual force-finalizes an in-flight job as completed (or failed) when
// the agent's result never arrived — e.g. the job_result frame was lost on a
// disconnect, stranding the job in 'sent'. Only in-flight jobs qualify; terminal
// and not-yet-approved jobs are rejected. Recorded in the audit trail as a manual
// action so the override is never silent.
func (s *Service) CompleteManual(ctx context.Context, jobID, actor string, success bool) (*models.Job, error) {
	j, err := s.store.GetJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	switch j.Status {
	case models.JobPending, models.JobSent, models.JobRunning:
		// in-flight — allowed
	default:
		return nil, fmt.Errorf("job is %s, cannot complete manually", j.Status)
	}
	status := models.JobCompleted
	note := "marked complete manually by " + actor
	if !success {
		status = models.JobFailed
		note = "marked failed manually by " + actor
	}
	if err := s.store.SetJobResult(ctx, jobID, status, 0, note, ""); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "job.complete_manual", jobID, map[string]any{"agent": j.AgentID, "host": s.hostname(ctx, j.AgentID), "success": success})
	return s.store.GetJob(ctx, jobID)
}

// dispatch attempts to push a pending job to a connected agent.
func (s *Service) dispatch(ctx context.Context, j *models.Job) {
	msg := protocol.JobMsg{JobID: j.ID, Type: j.Type, Shell: j.Shell, Script: j.Script, Params: j.Params}
	frame, _ := encode(protocol.MsgJob, msg)
	if s.hub.Send(j.AgentID, frame) {
		_ = s.store.SetJobStatus(ctx, j.ID, models.JobSent, nil)
	}
	// If offline, the job stays 'pending' and is delivered by DispatchPending
	// when the agent reconnects.
}

// DispatchPending re-sends all approved-but-undelivered jobs to a freshly
// connected agent.
func (s *Service) DispatchPending(ctx context.Context, agentID string) {
	jobs, err := s.store.ListDispatchableJobs(ctx, agentID)
	if err != nil {
		return
	}
	for i := range jobs {
		s.dispatch(ctx, &jobs[i])
	}
}

// OnResult records the outcome reported by an agent. It is idempotent: the agent
// now retries unacked results (at-least-once delivery), so a result for a job
// that is already terminal — finished earlier, or closed via manual complete —
// is accepted without re-writing state or the audit trail. A nil return means
// "durably handled", which is the caller's signal to ack so the agent drops it.
func (s *Service) OnResult(ctx context.Context, agentID string, res protocol.JobResultMsg) error {
	j, err := s.store.GetJob(ctx, res.JobID)
	if err != nil {
		return err
	}
	if j.AgentID != agentID {
		return fmt.Errorf("job result agent does not match job target")
	}
	switch j.Status {
	case models.JobCompleted, models.JobFailed, models.JobRejected:
		return nil // already finalized — idempotent no-op, still ack
	}
	status := models.JobCompleted
	if !res.Success {
		status = models.JobFailed
	}
	if err := s.store.SetJobResult(ctx, res.JobID, status, res.ExitCode, res.Stdout, res.Stderr); err != nil {
		return err
	}
	host := s.hostname(ctx, j.AgentID)
	// The shell travels with the result so the audit log can answer "which devices
	// could not run this interpreter" on its own. Exit 127 from a python job means
	// the device has none installed, and needing a join back to the job row to
	// learn that weakens the point of giving it a distinct code.
	s.audit(ctx, "agent", "job.result", res.JobID, map[string]any{"host": host, "exit_code": res.ExitCode, "success": res.Success, "shell": j.Shell})
	// The idempotency guard above means a retried result cannot mail twice: a job
	// that is already terminal returned before reaching this point.
	if status == models.JobFailed && s.notifier != nil {
		s.notifier.QueueJobFailed(ctx, j.AgentID, host, j.ID, j.Description)
	}
	if j.Autonomy == models.AutonomyPreapproved && j.ScriptID != nil {
		s.recordReleaseOutcome(ctx, j, host, status == models.JobCompleted)
	}
	return nil
}

// recordReleaseOutcome is the circuit breaker on released scripts: enough
// failed autonomous runs in a row withdraw the release, and the people who can
// see the device are told.
func (s *Service) recordReleaseOutcome(ctx context.Context, j *models.Job, host string, success bool) {
	as, ok := s.store.(AutonomyStore)
	if !ok {
		return
	}
	revoked, err := as.RecordAIScriptOutcome(ctx, *j.ScriptID, success, ReleaseFailureLimit)
	if err != nil || !revoked {
		return
	}
	name := *j.ScriptID
	if sc, err := as.GetScript(ctx, *j.ScriptID); err == nil {
		name = sc.Name
	}
	s.audit(ctx, "system", "script.ai_release.revoked", *j.ScriptID, map[string]any{
		"reason": "failed autonomous runs in a row", "failures": ReleaseFailureLimit, "last_job": j.ID, "host": host,
	})
	if rn, ok := s.notifier.(ReleaseNotifier); ok {
		rn.QueueAIReleaseRevoked(ctx, j.AgentID, host, *j.ScriptID, name, ReleaseFailureLimit)
	}
}

// hostname resolves an agent's hostname for audit labels (best-effort). The
// audit target stays the UUID; this only adds a human-readable label into detail
// so the audit report can name the device even after it is deleted or renamed.
func (s *Service) hostname(ctx context.Context, agentID string) string {
	if agentID == "" {
		return ""
	}
	if a, err := s.store.GetAgent(ctx, agentID); err == nil && a != nil {
		return a.Hostname
	}
	return ""
}

func (s *Service) audit(ctx context.Context, actor, action, target string, detail map[string]any) {
	var raw []byte
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	_ = s.store.AddAudit(ctx, actor, action, target, raw)
}

// encode wraps a payload in a protocol envelope.
func encode(typ string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(protocol.Envelope{Type: typ, Data: data})
}

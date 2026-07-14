// Package jobs orchestrates the job lifecycle: proposal -> approval -> dispatch
// -> result, with an audit trail at every state change.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// Store is the persistence surface the job service needs. *store.Store satisfies
// it; tests use an in-memory fake. Defining it here (consumer side) keeps the
// service decoupled from the concrete DB and unit-testable without Postgres.
type Store interface {
	GetAgent(ctx context.Context, id string) (*models.Agent, error)
	CreateJob(ctx context.Context, j *models.Job) (*models.Job, error)
	GetJob(ctx context.Context, id string) (*models.Job, error)
	SetJobStatus(ctx context.Context, id, status string, approvedBy *string) error
	SetJobResult(ctx context.Context, id, status string, exitCode int, stdout, stderr string) error
	ListDispatchableJobs(ctx context.Context, agentID string) ([]models.Job, error)
	ListJobsByBatch(ctx context.Context, batchID string) ([]models.Job, error)
	ClientForAgents(ctx context.Context, agentIDs []string) (map[string]string, error)
	AddAudit(ctx context.Context, actor, action, target string, detail []byte) error
}

// Sender pushes a frame to a connected agent, reporting whether it was online.
// *hub.Hub satisfies it.
type Sender interface {
	Send(agentID string, msg []byte) bool
}

// Service coordinates jobs across the store and the live-agent hub.
type Service struct {
	store Store
	hub   Sender
}

// New builds a job service.
func New(s Store, h Sender) *Service {
	return &Service{store: s, hub: h}
}

// Propose creates a job. If requireApproval is true the job lands in
// awaiting_approval and is NOT dispatched until a human approves it.
func (s *Service) Propose(ctx context.Context, agentID, shell, script, description, createdBy string, requireApproval bool) (*models.Job, error) {
	if _, err := s.store.GetAgent(ctx, agentID); err != nil {
		return nil, fmt.Errorf("agent: %w", err)
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
	j, err := s.store.CreateJob(ctx, j)
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
	j, err := s.store.CreateJob(ctx, j)
	if err != nil {
		return nil, err
	}
	s.audit(ctx, "scheduler", "job.automated", j.ID, map[string]any{"agent": agentID, "host": s.hostname(ctx, agentID), "task": taskID})
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
	if err := s.store.SetJobStatus(ctx, jobID, models.JobPending, &approver); err != nil {
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
	if err := s.store.SetJobStatus(ctx, jobID, models.JobRejected, &actor); err != nil {
		return nil, err
	}
	s.audit(ctx, actor, "job.reject", jobID, map[string]any{"agent": j.AgentID, "host": s.hostname(ctx, j.AgentID)})
	return s.store.GetJob(ctx, jobID)
}

// ProposeBatch creates one approval-gated job per agent, all sharing a fresh
// batch_id, for a bulk/mass deployment. Nothing is dispatched until the batch is
// approved (ApproveBatch) — the same human-in-the-loop guardrail, once for many.
func (s *Service) ProposeBatch(ctx context.Context, agentIDs []string, shell, script, description, createdBy string) ([]models.Job, string, error) {
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
	msg := protocol.JobMsg{JobID: j.ID, Type: j.Type, Shell: j.Shell, Script: j.Script}
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
func (s *Service) OnResult(ctx context.Context, res protocol.JobResultMsg) error {
	j, err := s.store.GetJob(ctx, res.JobID)
	if err != nil {
		return err
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
	s.audit(ctx, "agent", "job.result", res.JobID, map[string]any{"host": s.hostname(ctx, j.AgentID), "exit_code": res.ExitCode, "success": res.Success})
	return nil
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

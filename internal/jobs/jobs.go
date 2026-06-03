// Package jobs orchestrates the job lifecycle: proposal -> approval -> dispatch
// -> result, with an audit trail at every state change.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

// Service coordinates jobs across the store and the live-agent hub.
type Service struct {
	store *store.Store
	hub   *hub.Hub
}

// New builds a job service.
func New(s *store.Store, h *hub.Hub) *Service {
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
	s.audit(ctx, createdBy, "job.propose", j.ID, map[string]any{"agent": agentID, "shell": shell, "requires_approval": requireApproval})
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
	s.audit(ctx, "scheduler", "job.automated", j.ID, map[string]any{"agent": agentID, "task": taskID})
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
	s.audit(ctx, approver, "job.approve", jobID, nil)
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
	s.audit(ctx, actor, "job.reject", jobID, nil)
	return s.store.GetJob(ctx, jobID)
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
	s.audit(ctx, actor, "job.cancel", jobID, map[string]any{"delivered": delivered})
	if !delivered {
		_ = s.store.SetJobResult(ctx, jobID, models.JobFailed, -1, "", "canceled (agent offline)")
	}
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

// OnResult records the outcome reported by an agent.
func (s *Service) OnResult(ctx context.Context, res protocol.JobResultMsg) error {
	status := models.JobCompleted
	if !res.Success {
		status = models.JobFailed
	}
	if err := s.store.SetJobResult(ctx, res.JobID, status, res.ExitCode, res.Stdout, res.Stderr); err != nil {
		return err
	}
	s.audit(ctx, "agent", "job.result", res.JobID, map[string]any{"exit_code": res.ExitCode, "success": res.Success})
	return nil
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

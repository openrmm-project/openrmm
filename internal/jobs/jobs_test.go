package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
)

// ---- in-memory fakes ----

type fakeStore struct {
	jobs   map[string]*models.Job
	agents map[string]bool
	audits []string
	seq    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{jobs: map[string]*models.Job{}, agents: map[string]bool{"a1": true}}
}

func (f *fakeStore) GetAgent(_ context.Context, id string) (*models.Agent, error) {
	if !f.agents[id] {
		return nil, errors.New("agent not found")
	}
	return &models.Agent{ID: id, OS: "linux"}, nil
}

func (f *fakeStore) CreateJob(_ context.Context, j *models.Job) (*models.Job, error) {
	f.seq++
	j.ID = fmt.Sprintf("job-%d", f.seq)
	cp := *j
	f.jobs[j.ID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeStore) GetJob(_ context.Context, id string) (*models.Job, error) {
	j, ok := f.jobs[id]
	if !ok {
		return nil, errors.New("job not found")
	}
	cp := *j
	return &cp, nil
}

func (f *fakeStore) SetJobStatus(_ context.Context, id, status string, approvedBy *string) error {
	j, ok := f.jobs[id]
	if !ok {
		return errors.New("job not found")
	}
	j.Status = status
	if approvedBy != nil {
		j.ApprovedBy = approvedBy
	}
	return nil
}

func (f *fakeStore) SetJobResult(_ context.Context, id, status string, exitCode int, stdout, stderr string) error {
	j, ok := f.jobs[id]
	if !ok {
		return errors.New("job not found")
	}
	j.Status, j.ExitCode, j.Stdout, j.Stderr = status, &exitCode, stdout, stderr
	return nil
}

func (f *fakeStore) ListDispatchableJobs(_ context.Context, agentID string) ([]models.Job, error) {
	var out []models.Job
	for _, j := range f.jobs {
		if j.AgentID == agentID && j.Status == models.JobPending {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (f *fakeStore) ListJobsByBatch(_ context.Context, batchID string) ([]models.Job, error) {
	var out []models.Job
	for _, j := range f.jobs {
		if j.BatchID != nil && *j.BatchID == batchID {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (f *fakeStore) ClientForAgents(_ context.Context, agentIDs []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f *fakeStore) AddAudit(_ context.Context, actor, action, target string, detail []byte) error {
	f.audits = append(f.audits, action)
	return nil
}

func (f *fakeStore) hasAudit(action string) bool {
	for _, a := range f.audits {
		if a == action {
			return true
		}
	}
	return false
}

type fakeSender struct {
	online bool
	calls  int
}

func (s *fakeSender) Send(_ string, _ []byte) bool { s.calls++; return s.online }

func newSvc() (*Service, *fakeStore, *fakeSender) {
	fs, fk := newFakeStore(), &fakeSender{online: true}
	return New(fs, fk), fs, fk
}

// ---- the guardrail: AI proposes, a human approves ----

func TestProposeRequiresApprovalNeverDispatches(t *testing.T) {
	svc, fs, fk := newSvc()
	j, err := svc.Propose(context.Background(), "a1", "bash", "echo hi", "d", "ai:claude", true)
	if err != nil {
		t.Fatalf("Propose error: %v", err)
	}
	if j.Status != models.JobAwaitingApproval {
		t.Errorf("status = %q, want awaiting_approval", j.Status)
	}
	if !j.RequiresApproval {
		t.Error("RequiresApproval should be true")
	}
	if fk.calls != 0 {
		t.Errorf("a proposed job was dispatched (%d sends) — guardrail broken", fk.calls)
	}
	if !fs.hasAudit("job.propose") {
		t.Error("missing job.propose audit")
	}
}

func TestProposeUnapprovedDispatches(t *testing.T) {
	svc, _, fk := newSvc()
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "admin", false)
	if j.Status != models.JobSent {
		t.Errorf("status = %q, want sent (online dispatch)", j.Status)
	}
	if fk.calls != 1 {
		t.Errorf("sends = %d, want 1", fk.calls)
	}
}

func TestProposeUnknownAgent(t *testing.T) {
	svc, _, _ := newSvc()
	if _, err := svc.Propose(context.Background(), "ghost", "bash", "x", "d", "admin", true); err == nil {
		t.Error("Propose for an unknown agent should error")
	}
}

func TestApproveDispatchesOnce(t *testing.T) {
	svc, fs, fk := newSvc()
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "ai:claude", true)
	if fk.calls != 0 {
		t.Fatal("precondition: proposal must not dispatch")
	}
	got, err := svc.Approve(context.Background(), j.ID, "admin")
	if err != nil {
		t.Fatalf("Approve error: %v", err)
	}
	if got.Status != models.JobSent {
		t.Errorf("status after approve = %q, want sent", got.Status)
	}
	if got.ApprovedBy == nil || *got.ApprovedBy != "admin" {
		t.Error("ApprovedBy not recorded")
	}
	if fk.calls != 1 {
		t.Errorf("sends = %d, want exactly 1 after approve", fk.calls)
	}
	if !fs.hasAudit("job.approve") {
		t.Error("missing job.approve audit")
	}
	// approving again must fail (no double-dispatch)
	if _, err := svc.Approve(context.Background(), j.ID, "admin"); err == nil {
		t.Error("approving an already-approved job should error")
	}
}

func TestRejectStopsDispatch(t *testing.T) {
	svc, fs, fk := newSvc()
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "ai:claude", true)
	got, err := svc.Reject(context.Background(), j.ID, "admin")
	if err != nil || got.Status != models.JobRejected {
		t.Fatalf("Reject = (%v, %v), want rejected", got.Status, err)
	}
	if fk.calls != 0 {
		t.Error("a rejected job must never dispatch")
	}
	if !fs.hasAudit("job.reject") {
		t.Error("missing job.reject audit")
	}
	if _, err := svc.Reject(context.Background(), j.ID, "admin"); err == nil {
		t.Error("rejecting a non-pending job should error")
	}
}

func TestCancelAwaitingRejects(t *testing.T) {
	svc, _, fk := newSvc()
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "ai:claude", true)
	got, _ := svc.Cancel(context.Background(), j.ID, "admin")
	if got.Status != models.JobRejected {
		t.Errorf("cancel of awaiting job = %q, want rejected", got.Status)
	}
	if fk.calls != 0 {
		t.Error("canceling a proposal must not contact the agent")
	}
}

func TestCancelInFlightOfflineFails(t *testing.T) {
	svc, _, fk := newSvc()
	fk.online = false // agent offline: proposal-less pending job stays pending
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "admin", false)
	if j.Status != models.JobPending {
		t.Fatalf("precondition: offline job should be pending, got %q", j.Status)
	}
	got, _ := svc.Cancel(context.Background(), j.ID, "admin")
	if got.Status != models.JobFailed {
		t.Errorf("cancel of in-flight job with offline agent = %q, want failed", got.Status)
	}
}

func TestRunAutomatedIsPreApproved(t *testing.T) {
	svc, fs, fk := newSvc()
	j, err := svc.RunAutomated(context.Background(), "a1", "bash", "echo", "task", nil)
	if err != nil {
		t.Fatalf("RunAutomated error: %v", err)
	}
	if j.RequiresApproval {
		t.Error("automated job should not require approval")
	}
	if j.Status != models.JobSent || fk.calls != 1 {
		t.Errorf("automated job should dispatch immediately: status=%q sends=%d", j.Status, fk.calls)
	}
	if !fs.hasAudit("job.automated") {
		t.Error("missing job.automated audit")
	}
}

func TestOnResult(t *testing.T) {
	svc, _, _ := newSvc()
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "admin", false)
	if err := svc.OnResult(context.Background(), protocol.JobResultMsg{JobID: j.ID, Success: true, ExitCode: 0}); err != nil {
		t.Fatalf("OnResult error: %v", err)
	}
	got, _ := svc.store.GetJob(context.Background(), j.ID)
	if got.Status != models.JobCompleted {
		t.Errorf("status after success = %q, want completed", got.Status)
	}
	// Idempotent: a re-sent result for an already-finalized job (the agent retries
	// unacked results at-least-once) is a no-op and never flips a terminal status.
	if err := svc.OnResult(context.Background(), protocol.JobResultMsg{JobID: j.ID, Success: false, ExitCode: 1}); err != nil {
		t.Fatalf("duplicate OnResult error: %v", err)
	}
	got, _ = svc.store.GetJob(context.Background(), j.ID)
	if got.Status != models.JobCompleted {
		t.Errorf("status after duplicate = %q, want completed (idempotent)", got.Status)
	}
}

func TestOnResultFailure(t *testing.T) {
	svc, _, _ := newSvc()
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "admin", false)
	if err := svc.OnResult(context.Background(), protocol.JobResultMsg{JobID: j.ID, Success: false, ExitCode: 1}); err != nil {
		t.Fatalf("OnResult error: %v", err)
	}
	got, _ := svc.store.GetJob(context.Background(), j.ID)
	if got.Status != models.JobFailed {
		t.Errorf("status after failure = %q, want failed", got.Status)
	}
}

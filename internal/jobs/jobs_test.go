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
	jobs     map[string]*models.Job
	agents   map[string]bool
	clientOf map[string]string
	audits   []string
	seq      int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		jobs:     map[string]*models.Job{},
		agents:   map[string]bool{"a1": true},
		clientOf: map[string]string{},
	}
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
	out := make(map[string]string, len(agentIDs))
	for _, id := range agentIDs {
		out[id] = f.clientOf[id]
	}
	return out, nil
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

func TestProposeRejectsUnknownShell(t *testing.T) {
	svc, _, _ := newSvc()
	if _, err := svc.Propose(context.Background(), "a1", "python", "print(1)", "d", "admin", true); err == nil {
		t.Fatal("unsupported shell must be rejected")
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

func TestProposeBulkGroupsByClientAndApprovesIndependently(t *testing.T) {
	svc, fs, sender := newSvc()
	fs.agents["a2"] = true
	fs.agents["a3"] = true
	fs.clientOf["a1"] = "client-a"
	fs.clientOf["a2"] = "client-a"
	fs.clientOf["a3"] = "client-b"

	batches, err := svc.ProposeBulk(context.Background(), []string{"a1", "a2", "a3"},
		"bash", "echo bulk", "bulk deploy", "admin")
	if err != nil {
		t.Fatalf("ProposeBulk error: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want one batch per client", len(batches))
	}
	if sender.calls != 0 {
		t.Fatalf("bulk proposal dispatched before approval: %d sends", sender.calls)
	}

	byClient := make(map[string]BatchInfo, len(batches))
	for _, batch := range batches {
		byClient[batch.ClientID] = batch
	}
	clientA, ok := byClient["client-a"]
	if !ok || len(clientA.JobIDs) != 2 {
		t.Fatalf("client-a batch = %#v, want two jobs", clientA)
	}
	clientB, ok := byClient["client-b"]
	if !ok || len(clientB.JobIDs) != 1 {
		t.Fatalf("client-b batch = %#v, want one job", clientB)
	}
	for _, batch := range batches {
		for _, jobID := range batch.JobIDs {
			job, err := fs.GetJob(context.Background(), jobID)
			if err != nil {
				t.Fatal(err)
			}
			if job.BatchID == nil || *job.BatchID != batch.BatchID || job.Status != models.JobAwaitingApproval {
				t.Fatalf("job %#v is not an awaiting job in batch %q", job, batch.BatchID)
			}
		}
	}

	approved, err := svc.ApproveBatch(context.Background(), clientA.BatchID, "admin")
	if err != nil {
		t.Fatalf("ApproveBatch error: %v", err)
	}
	if approved != 2 || sender.calls != 2 {
		t.Fatalf("approved=%d sends=%d, want 2 approved and dispatched", approved, sender.calls)
	}
	for _, jobID := range clientA.JobIDs {
		job, _ := fs.GetJob(context.Background(), jobID)
		if job.Status != models.JobSent {
			t.Errorf("client-a job %s status = %q, want sent", jobID, job.Status)
		}
	}
	job, _ := fs.GetJob(context.Background(), clientB.JobIDs[0])
	if job.Status != models.JobAwaitingApproval {
		t.Errorf("client-b job status = %q, want awaiting approval", job.Status)
	}
}

func TestOnResult(t *testing.T) {
	svc, _, _ := newSvc()
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "admin", false)
	if err := svc.OnResult(context.Background(), "a1", protocol.JobResultMsg{JobID: j.ID, Success: true, ExitCode: 0}); err != nil {
		t.Fatalf("OnResult error: %v", err)
	}
	got, _ := svc.store.GetJob(context.Background(), j.ID)
	if got.Status != models.JobCompleted {
		t.Errorf("status after success = %q, want completed", got.Status)
	}
	// Idempotent: a re-sent result for an already-finalized job (the agent retries
	// unacked results at-least-once) is a no-op and never flips a terminal status.
	if err := svc.OnResult(context.Background(), "a1", protocol.JobResultMsg{JobID: j.ID, Success: false, ExitCode: 1}); err != nil {
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
	if err := svc.OnResult(context.Background(), "a1", protocol.JobResultMsg{JobID: j.ID, Success: false, ExitCode: 1}); err != nil {
		t.Fatalf("OnResult error: %v", err)
	}
	got, _ := svc.store.GetJob(context.Background(), j.ID)
	if got.Status != models.JobFailed {
		t.Errorf("status after failure = %q, want failed", got.Status)
	}
}

func TestOnResultRejectsAnotherAgentsJob(t *testing.T) {
	svc, fs, _ := newSvc()
	fs.agents["a2"] = true
	j, _ := svc.Propose(context.Background(), "a1", "bash", "echo", "d", "admin", false)
	if err := svc.OnResult(context.Background(), "a2", protocol.JobResultMsg{JobID: j.ID, Success: true}); err == nil {
		t.Fatal("result from another agent must be rejected")
	}
	got, _ := fs.GetJob(context.Background(), j.ID)
	if got.Status == models.JobCompleted {
		t.Fatal("foreign result must not complete the job")
	}
}

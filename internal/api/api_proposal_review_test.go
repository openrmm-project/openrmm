package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// Accepting an AI proposal is the one path that releases AI-authored code to
// run, and it is a REST call: the console enables the row with PUT
// /api/tasks/{id}. Everything else about proposals was pinned at the store and
// in the database, but that left the handler untested — and the handler is what
// fills approved_by. Without it the CHECK constraint rejects the update and an
// operator simply cannot accept a proposal at all, with no test failing.

type proposalReviewFixture struct {
	*policyAPIFixture
	ctx   context.Context
	agent string
}

func newProposalReviewFixture(t *testing.T) proposalReviewFixture {
	t.Helper()
	f := newPolicyAPIFixture(t)
	ctx := context.Background()
	client, err := f.store.CreateClient(ctx, "acme-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	site, err := f.store.CreateSite(ctx, client.ID, "site-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.store.CreateAgent(ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	return proposalReviewFixture{policyAPIFixture: f, ctx: ctx, agent: agent.ID}
}

// proposedTask writes what the AI tools write: disabled, stamped, unreviewed.
func (f proposalReviewFixture) proposedTask(t *testing.T) *models.Task {
	t.Helper()
	task, err := f.store.CreateTask(f.ctx, &models.Task{
		AgentID: &f.agent, Name: "nightly-" + uuid.NewString(), Shell: "bash", Script: "echo hi",
		ScheduleKind: models.ScheduleInterval, IntervalSec: 3600,
		Enabled: false, ProposedBy: "ai:assistant",
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func taskBody(t *models.Task, enabled bool) string {
	return fmt.Sprintf(`{"name":%q,"shell":%q,"script":%q,"schedule_kind":%q,"interval_sec":%d,"enabled":%t}`,
		t.Name, t.Shell, t.Script, t.ScheduleKind, t.IntervalSec, enabled)
}

// The load-bearing case: a person enables a proposal through the API, and it
// becomes live configuration the scheduler can see.
func TestEnablingAProposalThroughTheAPIAcceptsItIntegration(t *testing.T) {
	f := newProposalReviewFixture(t)
	task := f.proposedTask(t)

	// Before review the scheduler cannot see it. This is the whole point of the
	// proposal being written disabled.
	due, err := f.store.TasksForAgent(f.ctx, f.agent, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.ID == task.ID {
			t.Fatal("the scheduler could see an unreviewed proposal")
		}
	}

	rec := f.do(http.MethodPut, "/api/tasks/"+task.ID, taskBody(task, true))
	if rec.Code != http.StatusOK {
		t.Fatalf("accepting the proposal: status %d, body=%s", rec.Code, rec.Body.String())
	}

	after, err := f.store.GetTask(f.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Enabled {
		t.Fatal("the accepted proposal is still disabled")
	}
	if after.ApprovedBy == "" {
		t.Fatal("approved_by is empty: the handler did not record who released it")
	}
	if after.ProposedBy != "ai:assistant" {
		t.Errorf("provenance was lost on acceptance: proposed_by = %q", after.ProposedBy)
	}

	// And now the scheduler does see it — the acceptance actually took effect.
	due, err = f.store.TasksForAgent(f.ctx, f.agent, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var visible bool
	for _, d := range due {
		if d.ID == task.ID {
			visible = true
		}
	}
	if !visible {
		t.Fatal("the accepted task never reached the scheduler")
	}
}

// Editing a proposal without enabling it is not acceptance: it stays unreviewed,
// so a person cannot half-approve one by adjusting its schedule.
func TestEditingAProposalWithoutEnablingDoesNotApproveItIntegration(t *testing.T) {
	f := newProposalReviewFixture(t)
	task := f.proposedTask(t)

	edited := *task
	edited.IntervalSec = 7200
	rec := f.do(http.MethodPut, "/api/tasks/"+task.ID, taskBody(&edited, false))
	if rec.Code != http.StatusOK {
		t.Fatalf("editing the proposal: status %d, body=%s", rec.Code, rec.Body.String())
	}

	after, err := f.store.GetTask(f.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ApprovedBy != "" {
		t.Errorf("an edit that left it disabled recorded an approval: %q", after.ApprovedBy)
	}
	if after.IntervalSec != 7200 {
		t.Errorf("the edit did not apply: interval = %d", after.IntervalSec)
	}
}

// A task a person wrote is not a proposal and must not acquire a reviewer: the
// column means "who released an AI proposal", and every ordinary edit filling it
// in would make the review state meaningless.
func TestEnablingAHumanAuthoredTaskRecordsNoApproverIntegration(t *testing.T) {
	f := newProposalReviewFixture(t)
	task, err := f.store.CreateTask(f.ctx, &models.Task{
		AgentID: &f.agent, Name: "human-" + uuid.NewString(), Shell: "bash", Script: "echo hi",
		ScheduleKind: models.ScheduleInterval, IntervalSec: 3600, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if rec := f.do(http.MethodPut, "/api/tasks/"+task.ID, taskBody(task, true)); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body=%s", rec.Code, rec.Body.String())
	}
	after, err := f.store.GetTask(f.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ApprovedBy != "" {
		t.Errorf("a human-authored task acquired an approver: %q", after.ApprovedBy)
	}
	if !after.Enabled {
		t.Error("the task was not enabled")
	}
}

// The same acceptance path for checks, which carry the same constraint.
func TestEnablingAProposedCheckThroughTheAPIAcceptsItIntegration(t *testing.T) {
	f := newProposalReviewFixture(t)
	warn, errT := 20.0, 10.0
	check, err := f.store.CreateCheck(f.ctx, &models.Check{
		AgentID: &f.agent, Type: models.CheckMemory, Name: "ram-" + uuid.NewString(),
		Config: json.RawMessage(`{}`), WarnThreshold: &warn, ErrThreshold: &errT,
		IntervalSec: 300, FailureCount: 2, RecoveryCount: 2, TimeoutSec: 180,
		Enabled: false, ProposedBy: "mcp:assistant",
	})
	if err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"type":%q,"name":%q,"config":{},"warn_threshold":20,"err_threshold":10,"interval_sec":300,"enabled":true}`,
		check.Type, check.Name)
	if rec := f.do(http.MethodPut, "/api/checks/"+check.ID, body); rec.Code != http.StatusOK {
		t.Fatalf("accepting the check: status %d, body=%s", rec.Code, rec.Body.String())
	}
	after, err := f.store.GetCheck(f.ctx, check.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Enabled || after.ApprovedBy == "" {
		t.Fatalf("the check was not accepted: enabled=%t approved_by=%q", after.Enabled, after.ApprovedBy)
	}
	if after.ProposedBy != "mcp:assistant" {
		t.Errorf("provenance lost: proposed_by = %q", after.ProposedBy)
	}
}

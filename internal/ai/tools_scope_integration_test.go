package ai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// The AI surface is the one place where an untrusted party — the model — chooses
// the arguments. Every guard here therefore has to hold against an agent_id the
// caller was never entitled to name, and nothing in this package tested that: the
// existing tests cover the circuit breaker and command construction only.
//
// These run the real tools against a real database, because the guard being
// verified (AgentAllowedForUser) is a query, not a branch.

// nilSender stands in for the agent hub. Proposed jobs sit in awaiting_approval
// and are never dispatched, so nothing is ever sent.
type nilSender struct{}

func (nilSender) Send(string, []byte) bool { return false }

type scopeFixture struct {
	ctx         context.Context
	st          *store.Store
	js          *jobs.Service
	mineAgent   string
	theirAgent  string
	mineHost    string
	theirHost   string
	scopedUser  string
	theirClient string
}

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	st, err := store.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	suffix := uuid.NewString()
	mine, err := st.CreateClient(ctx, "mine-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := st.CreateClient(ctx, "theirs-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	mineSite, err := st.CreateSite(ctx, mine.ID, "mine-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirSite, err := st.CreateSite(ctx, theirs.ID, "their-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	f := scopeFixture{ctx: ctx, st: st, js: jobs.New(st, nilSender{})}
	f.mineHost, f.theirHost = "mine-host-"+suffix, "their-host-"+suffix

	mineAgent, err := st.CreateAgent(ctx, f.mineHost, "linux", "amd64", "1.0.0", "tok-mine-"+suffix, &mineSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	theirAgent, err := st.CreateAgent(ctx, f.theirHost, "linux", "amd64", "1.0.0", "tok-theirs-"+suffix, &theirSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.mineAgent, f.theirAgent = mineAgent.ID, theirAgent.ID
	f.theirClient = theirs.ID

	scoped, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: mine.ID}})
	if err != nil {
		t.Fatal(err)
	}
	f.scopedUser = scoped.ID
	return f
}

// scopedContext is the tool context the chat builds for a restricted technician
// holding every AI-relevant permission — so a refusal below is the scope talking,
// never a missing permission.
func (f scopeFixture) scopedContext() ToolContext {
	return ToolContext{
		Store: f.st, Jobs: f.js, Actor: "tech", ScopeUserID: f.scopedUser,
		Permissions: map[string]bool{
			models.PermJobsCreate:  true,
			models.PermJobsView:    true,
			models.PermJobsManage:  true,
			models.PermDevicesView: true,
			models.PermReportsView: true,
		},
	}
}

func proposeArgs(agentID string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"agent_id": agentID, "shell": "bash", "script": "echo hi", "description": "test",
	})
	return b
}

// The load-bearing case: the model names a machine belonging to another company.
func TestProposeScriptRefusesAnOutOfScopeDeviceIntegration(t *testing.T) {
	f := newScopeFixture(t)

	res := Execute(f.ctx, f.scopedContext(), "propose_script", proposeArgs(f.theirAgent))
	if !strings.Contains(res.Output, "outside your access scope") {
		t.Fatalf("an out-of-scope device was accepted: %s", res.Output)
	}
	if len(res.CreatedJobIDs) != 0 {
		t.Fatalf("a job was created for an out-of-scope device: %v", res.CreatedJobIDs)
	}
	// Refusing in the response but writing the row anyway would be worse than
	// not refusing at all, so check the database rather than the message.
	jobsFor, err := f.st.ListJobs(f.ctx, f.theirAgent, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobsFor) != 0 {
		t.Fatalf("the refused proposal still queued %d job(s) on the out-of-scope device", len(jobsFor))
	}
}

// The control: the same call against an in-scope device must succeed, and must
// land in awaiting_approval rather than running.
func TestProposeScriptAcceptsAnInScopeDeviceAndStillWaitsForApprovalIntegration(t *testing.T) {
	f := newScopeFixture(t)

	res := Execute(f.ctx, f.scopedContext(), "propose_script", proposeArgs(f.mineAgent))
	if strings.Contains(res.Output, "outside your access scope") {
		t.Fatalf("an in-scope device was refused: %s", res.Output)
	}
	if len(res.CreatedJobIDs) != 1 {
		t.Fatalf("expected one created job, got %v (%s)", res.CreatedJobIDs, res.Output)
	}
	job, err := f.st.GetJob(f.ctx, res.CreatedJobIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != models.JobAwaitingApproval || !job.RequiresApproval {
		t.Fatalf("an AI-proposed job must wait for a human: status=%q requires_approval=%t",
			job.Status, job.RequiresApproval)
	}
}

// list_devices and fleet_summary are the reconnaissance step: if they enumerate
// the whole fleet, the model learns machine ids it can then name directly.
func TestDeviceToolsEnumerateOnlyTheCallersScopeIntegration(t *testing.T) {
	f := newScopeFixture(t)
	tc := f.scopedContext()

	devices := Execute(f.ctx, tc, "list_devices", json.RawMessage(`{}`))
	if !strings.Contains(devices.Output, f.mineHost) {
		t.Errorf("list_devices hid an in-scope machine: %s", devices.Output)
	}
	if strings.Contains(devices.Output, f.theirHost) || strings.Contains(devices.Output, f.theirAgent) {
		t.Errorf("list_devices leaked an out-of-scope machine: %s", devices.Output)
	}

	summary := Execute(f.ctx, tc, "fleet_summary", json.RawMessage(`{}`))
	if strings.Contains(summary.Output, f.theirHost) {
		t.Errorf("fleet_summary leaked an out-of-scope machine: %s", summary.Output)
	}
}

// get_inventory takes an id straight from the model, which is exactly the shape
// of an IDOR: knowing (or guessing) an id must not be enough.
func TestGetInventoryRefusesAnOutOfScopeDeviceIntegration(t *testing.T) {
	f := newScopeFixture(t)
	args, _ := json.Marshal(map[string]any{"agent_id": f.theirAgent})

	res := Execute(f.ctx, f.scopedContext(), "get_inventory", json.RawMessage(args))
	if !strings.Contains(res.Output, "outside your access scope") {
		t.Fatalf("get_inventory served an out-of-scope device: %s", res.Output)
	}
}

// inventory_report is the tool the operator actually asks for by name ("report on
// the machines"), and it takes a scope_type/scope_id pair from the model. Naming
// another tenant's client must not widen the answer.
func TestInventoryReportCannotBeAimedOutsideTheScopeIntegration(t *testing.T) {
	f := newScopeFixture(t)

	tc := f.scopedContext()

	// Unfiltered: the caller's own fleet, and only that.
	res := Execute(f.ctx, tc, "inventory_report", json.RawMessage(`{"report_type":"machines"}`))
	if strings.Contains(res.Output, f.theirHost) {
		t.Fatalf("inventory_report leaked an out-of-scope machine: %s", res.Output)
	}
	if !strings.Contains(res.Output, f.mineHost) {
		t.Fatalf("inventory_report hid an in-scope machine: %s", res.Output)
	}

	// Aimed straight at the other tenant, which is what a jailbroken model would
	// try once it has a client id. The access scope and the requested filter are
	// ANDed, so the answer is empty rather than someone else's fleet.
	aimed, _ := json.Marshal(map[string]any{
		"report_type": "machines", "scope_type": "client", "scope_id": f.theirClient,
	})
	res = Execute(f.ctx, tc, "inventory_report", json.RawMessage(aimed))
	if strings.Contains(res.Output, f.theirHost) {
		t.Fatalf("a cross-tenant inventory_report was answered: %s", res.Output)
	}
}

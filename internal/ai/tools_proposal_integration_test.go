package ai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/store"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/exports"
	"github.com/p1ngu/rmm/internal/models"
)

// The guarantee these pin: an AI cannot produce configuration that runs.
//
// It matters because a task is a script with a schedule, and the scheduler runs
// one through jobs.RunAutomated — pre-approved, dispatched at once, repeating.
// An AI able to create an ENABLED task would have been strictly more powerful
// than one able to propose a script, walking straight around the approval gate.

// A fixture of its own rather than a shared one: these tools never create a job,
// so they need no job service, and keeping it self-contained means this file does
// not depend on the scope-isolation suite landing first.
type proposalFixture struct {
	ctx         context.Context
	st          *store.Store
	mineAgent   string
	theirAgent  string
	mineClient  string
	theirClient string
	scopedUser  string
}

func newProposalFixture(t *testing.T) proposalFixture {
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
	mineAgent, err := st.CreateAgent(ctx, "mine-"+suffix, "linux", "amd64", "1.0.0", "tok-mine-"+suffix, &mineSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	theirAgent, err := st.CreateAgent(ctx, "theirs-"+suffix, "linux", "amd64", "1.0.0", "tok-theirs-"+suffix, &theirSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: mine.ID}})
	if err != nil {
		t.Fatal(err)
	}
	return proposalFixture{
		ctx: ctx, st: st,
		mineAgent: mineAgent.ID, theirAgent: theirAgent.ID,
		mineClient: mine.ID, theirClient: theirs.ID,
		scopedUser: scoped.ID,
	}
}

// A restricted technician holding every management permission, so a refusal is
// the scope talking and never a missing permission.
func (f proposalFixture) tc() ToolContext {
	return ToolContext{
		Store: f.st, Actor: "tech", ScopeUserID: f.scopedUser,
		Permissions: map[string]bool{
			models.PermTasksManage:    true,
			models.PermChecksManage:   true,
			models.PermPoliciesManage: true,
			models.PermPatchesManage:  true,
			models.PermTasksView:      true,
			models.PermChecksView:     true,
			models.PermPoliciesView:   true,
		},
	}
}

func taskArgs(agentID string, extra map[string]any) json.RawMessage {
	in := map[string]any{
		"agent_id": agentID, "name": "nightly", "shell": "bash",
		"script": "echo hi", "schedule_kind": "interval", "interval_sec": 3600,
	}
	for k, v := range extra {
		in[k] = v
	}
	b, _ := json.Marshal(in)
	return b
}

// A proposed task lands disabled and stamped, and the scheduler's own query
// cannot see it.
func TestProposedTaskIsInertUntilReviewedIntegration(t *testing.T) {
	f := newProposalFixture(t)

	res := Execute(f.ctx, f.tc(), "propose_task", taskArgs(f.mineAgent, nil))
	if strings.Contains(res.Output, `"error"`) {
		t.Fatalf("propose_task failed: %s", res.Output)
	}
	if !strings.Contains(res.Output, "awaiting_review") {
		t.Errorf("the tool must tell the model nothing runs yet: %s", res.Output)
	}

	tasks, err := f.st.ListTasks(f.ctx, f.mineAgent, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one task, got %d", len(tasks))
	}
	got := tasks[0]
	if got.Enabled {
		t.Fatal("an AI-proposed task was created enabled: it would run without review")
	}
	if got.ProposedBy == "" {
		t.Error("the task carries no provenance, so nothing marks it for review")
	}
	if got.ApprovedBy != "" {
		t.Errorf("nobody reviewed it, yet approved_by = %q", got.ApprovedBy)
	}

	// The load-bearing check: the scheduler's own query — WHERE enabled — is
	// what actually keeps it from running. Assert through that, not through the
	// column, so the test survives a refactor of either.
	due, err := f.st.TasksForAgent(f.ctx, f.mineAgent, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.ID == got.ID {
			t.Fatal("the scheduler can see an unreviewed AI proposal")
		}
	}
}

// The model asking for an enabled task does not get one. It cannot: `enabled` is
// not in the tool schema, and the database refuses the row regardless.
func TestProposedTaskCannotBeCreatedEnabledIntegration(t *testing.T) {
	f := newProposalFixture(t)

	res := Execute(f.ctx, f.tc(), "propose_task",
		taskArgs(f.mineAgent, map[string]any{"enabled": true}))
	if strings.Contains(res.Output, `"error"`) {
		t.Fatalf("propose_task failed: %s", res.Output)
	}
	tasks, _ := f.st.ListTasks(f.ctx, f.mineAgent, "")
	if len(tasks) != 1 || tasks[0].Enabled {
		t.Fatalf("an enabled task was produced from an enabled:true argument: %+v", tasks)
	}

	// And directly at the store, bypassing the tool entirely: the constraint is
	// the real guard, because the REST handlers are a second path into this row.
	_, err := f.st.CreateTask(f.ctx, &models.Task{
		AgentID: &f.mineAgent, Name: "sneaky", Shell: "bash", Script: "echo hi",
		ScheduleKind: models.ScheduleInterval, IntervalSec: 3600,
		Enabled: true, ProposedBy: "ai:someone",
	})
	if err == nil {
		t.Fatal("the database accepted an enabled, unreviewed AI proposal")
	}
}

// Acceptance is a person enabling it, and provenance survives that: months later
// the row still says an AI wrote it.
func TestAcceptingAProposalKeepsItsProvenanceIntegration(t *testing.T) {
	f := newProposalFixture(t)
	Execute(f.ctx, f.tc(), "propose_task", taskArgs(f.mineAgent, nil))
	tasks, _ := f.st.ListTasks(f.ctx, f.mineAgent, "")
	task := tasks[0]

	task.Enabled = true
	task.ApprovedBy = "operator@example.test"
	if err := f.st.UpdateTask(f.ctx, &task); err != nil {
		t.Fatalf("a reviewed proposal must be enablable: %v", err)
	}
	after, err := f.st.GetTask(f.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Enabled {
		t.Fatal("the accepted proposal did not become live")
	}
	if after.ProposedBy == "" {
		t.Error("acceptance erased the provenance; the row no longer says an AI wrote it")
	}
	if after.ApprovedBy != "operator@example.test" {
		t.Errorf("approved_by = %q, want the reviewer", after.ApprovedBy)
	}
}

// Checks get the same treatment, and so does the scope: a proposal aimed at
// another tenant's device is refused before anything is written.
func TestProposedCheckIsInertAndScopedIntegration(t *testing.T) {
	f := newProposalFixture(t)
	args := func(agentID string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{
			"agent_id": agentID, "type": "memory", "name": "ram",
			"warn_threshold": 20, "err_threshold": 10,
		})
		return b
	}

	if res := Execute(f.ctx, f.tc(), "propose_check", args(f.theirAgent)); !strings.Contains(res.Output, "outside your access scope") {
		t.Fatalf("a check was proposed on an out-of-scope device: %s", res.Output)
	}
	if checks, _ := f.st.ListChecks(f.ctx, f.theirAgent, ""); len(checks) != 0 {
		t.Fatalf("the refused proposal still wrote %d check(s)", len(checks))
	}

	if res := Execute(f.ctx, f.tc(), "propose_check", args(f.mineAgent)); strings.Contains(res.Output, `"error"`) {
		t.Fatalf("propose_check failed on an in-scope device: %s", res.Output)
	}
	checks, _ := f.st.ListChecks(f.ctx, f.mineAgent, "")
	if len(checks) != 1 {
		t.Fatalf("expected one check, got %d", len(checks))
	}
	if checks[0].Enabled || checks[0].ProposedBy == "" {
		t.Fatalf("an AI-proposed check must be disabled and stamped: %+v", checks[0])
	}
}

// A policy definition is global, so a restricted principal has no business
// creating one — the same rule the REST handler enforces.
func TestProposePolicyRefusesScopedPrincipalsIntegration(t *testing.T) {
	f := newProposalFixture(t)
	args := json.RawMessage(`{"name":"baseline","description":"x"}`)

	if res := Execute(f.ctx, f.tc(), "propose_policy", args); !strings.Contains(res.Output, "scoped users cannot create") {
		t.Fatalf("a scoped principal created a global policy: %s", res.Output)
	}

	// Unscoped, the same call works and is stamped as a proposal.
	tc := f.tc()
	tc.ScopeUserID = ""
	res := Execute(f.ctx, tc, "propose_policy", args)
	if strings.Contains(res.Output, `"error"`) {
		t.Fatalf("propose_policy failed for an unscoped admin: %s", res.Output)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(res.Output), &out)
	policies, err := f.st.ListPolicies(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range policies {
		if p.ID == out.ID && p.ProposedBy == "" {
			t.Error("the proposed policy carries no provenance")
		}
	}
}

// There is no tool to approve a patch, and that absence is the guarantee: the
// model can install only what a person already approved.
func TestNoPatchApprovalToolExistsIntegration(t *testing.T) {
	perms := map[string]bool{
		models.PermPatchesManage: true, models.PermPatchesView: true,
		models.PermPatchPoliciesManage: true,
	}
	for _, tool := range ToolsForPermissions(perms) {
		name := tool.Function.Name
		if strings.Contains(name, "approve") {
			t.Errorf("tool %q exposes approval to the model; approval is a human decision", name)
		}
	}
}

// Exports hand back a link instead of rows. What matters is that the link is
// scoped and signed, not that a URL comes back — a token a caller could edit
// would let them widen the query the server then trusts.
func TestExportsAreScopedAndSignedIntegration(t *testing.T) {
	f := newProposalFixture(t)
	tc := f.tc()
	tc.Config = &config.Config{SettingsEncryptionKey: "0123456789abcdef0123456789abcdef"}
	tc.PublicURL = "https://acme.openrmm.com"
	tc.Permissions[models.PermAuditView] = true
	tc.Permissions[models.PermReportsView] = true

	// The audit trail is instance-wide, so a scoped account is refused outright
	// rather than shown a filtered view that does not exist.
	if res := Execute(f.ctx, tc, "export_audit", json.RawMessage(`{}`)); !strings.Contains(res.Output, "scoped account cannot read it") {
		t.Fatalf("a scoped principal was given an audit export: %s", res.Output)
	}

	// Inventory IS scoped, so it is allowed — and the token has to carry that
	// scope so the download cannot quietly become someone else's fleet.
	res := Execute(f.ctx, tc, "export_inventory", json.RawMessage(`{"report_type":"machines"}`))
	if strings.Contains(res.Output, `"error"`) {
		t.Fatalf("export_inventory failed for a scoped principal: %s", res.Output)
	}
	var out struct {
		URL string `json:"download_url"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.URL, "https://acme.openrmm.com/api/exports/csv?token=") {
		t.Fatalf("download_url = %q; it must be absolute on the tenant's own origin", out.URL)
	}

	token := out.URL[strings.Index(out.URL, "token=")+len("token="):]
	claims, err := exports.Verify(tc.Config, token)
	if err != nil {
		t.Fatalf("the tool minted a token the server will not accept: %v", err)
	}
	if claims.Scope != f.scopedUser {
		t.Errorf("token scope = %q, want the caller's %q — the download would widen it",
			claims.Scope, f.scopedUser)
	}
	if claims.Params["report_type"] != "machines" {
		t.Errorf("token lost the report type: %+v", claims.Params)
	}

	// A report type the server cannot serve is refused when the link is minted,
	// not discovered as a broken download later.
	if res := Execute(f.ctx, tc, "export_inventory", json.RawMessage(`{"report_type":"everything"}`)); !strings.Contains(res.Output, "report_type must be") {
		t.Errorf("an unknown report type produced a link: %s", res.Output)
	}
}

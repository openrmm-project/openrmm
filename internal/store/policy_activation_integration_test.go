package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// switchFixture is one device under a client and site.
type switchFixture struct {
	st                       *Store
	ctx                      context.Context
	clientID, siteID, device string
}

func newSwitchFixture(t *testing.T, prefix string) switchFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, prefix, url)
	ctx := context.Background()
	client, err := st.CreateClient(ctx, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "HQ")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, "PC-1", "linux", "amd64", "0.11.0", "tok-"+uuid.NewString(), &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return switchFixture{st: st, ctx: ctx, clientID: client.ID, siteID: site.ID, device: agent.ID}
}

// reach reports what of a policy reaches the device, through every query that
// decides it: effective tasks and checks, the check scheduler, "run now", the
// devices a check covers, and the server rule that governs the device.
type reach struct {
	tasks, checks, checkApplies, runNow, covered, governed bool
}

func (f switchFixture) reach(t *testing.T, policyID, checkID, ruleType string) reach {
	t.Helper()
	var r reach
	tasks, err := f.st.TasksForAgent(f.ctx, f.device, &f.siteID, &f.clientID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		r.tasks = r.tasks || (task.PolicyID != nil && *task.PolicyID == policyID)
	}
	checks, err := f.st.ChecksForAgent(f.ctx, f.device, &f.siteID, &f.clientID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		r.checks = r.checks || (c.PolicyID != nil && *c.PolicyID == policyID)
	}
	if r.checkApplies, err = f.st.CheckAppliesToAgent(f.ctx, checkID, f.device); err != nil {
		t.Fatal(err)
	}
	agents, err := f.st.AgentIDsForPolicy(f.ctx, policyID)
	if err != nil {
		t.Fatal(err)
	}
	r.runNow = len(agents) == 1
	covered, err := f.st.CheckCoveredAgents(f.ctx, checkID)
	if err != nil {
		t.Fatal(err)
	}
	r.covered = len(covered) == 1
	if ruleType != "" {
		governing, err := f.st.GoverningRules(f.ctx, ruleType)
		if err != nil {
			t.Fatal(err)
		}
		r.governed = governing[f.device].PolicyID == policyID
	}
	return r
}

func memoryCheck(policyID string, enabled bool, proposedBy string) *models.Check {
	warn, fail := 20.0, 10.0
	return &models.Check{PolicyID: &policyID, Name: "Memory", Type: models.CheckMemory, Config: json.RawMessage(`{}`),
		WarnThreshold: &warn, ErrThreshold: &fail, IntervalSec: 300, TimeoutSec: 30, FailureCount: 2, RecoveryCount: 2,
		Enabled: enabled, ProposedBy: proposedBy}
}

func shellTask(policyID string, enabled bool, proposedBy string) *models.Task {
	return &models.Task{PolicyID: &policyID, Name: "Clean", Shell: "sh", Script: "true",
		ScheduleKind: models.ScheduleInterval, IntervalSec: 3600, Enabled: enabled, ProposedBy: proposedBy}
}

// A switched-off policy applies nothing, through every path, and closes the
// alerts of its checks; switched on again it applies exactly what it did.
func TestSwitchedOffPolicyAppliesNothingIntegration(t *testing.T) {
	f := newSwitchFixture(t, "policy_switch")
	pol, err := f.st.CreatePolicy(f.ctx, "Baseline", "", "")
	if err != nil || !pol.Enabled || pol.Pending {
		t.Fatalf("a person's policy starts on: %+v %v", pol, err)
	}
	if _, err := f.st.AssignPolicy(f.ctx, pol.ID, models.ScopeClient, f.clientID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateTask(f.ctx, shellTask(pol.ID, true, "")); err != nil {
		t.Fatal(err)
	}
	check, err := f.st.CreateCheck(f.ctx, memoryCheck(pol.ID, true, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateCheck(f.ctx, &models.Check{PolicyID: &pol.ID, Name: "Offline", Type: models.CheckConnectivity,
		Config: json.RawMessage(`{"after_min":15}`), IntervalSec: 300, TimeoutSec: 30, FailureCount: 1, RecoveryCount: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	on := reach{true, true, true, true, true, true}
	if got := f.reach(t, pol.ID, check.ID, models.CheckConnectivity); got != on {
		t.Fatalf("switched on: %+v", got)
	}
	var alertID string
	if err := f.st.pool.QueryRow(f.ctx, `INSERT INTO alerts (id,source_type,source_id,agent_id,condition_key,severity,message,first_seen_at,last_seen_at,updated_at)
		VALUES (gen_random_uuid(),'check',$1,$2,'health','warning','memory low',now(),now(),now()) RETURNING id`, check.ID, f.device).Scan(&alertID); err != nil {
		t.Fatal(err)
	}
	alertStatus := func() string {
		var status string
		if err := f.st.pool.QueryRow(f.ctx, `SELECT status FROM alerts WHERE id=$1`, alertID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		return status
	}
	if _, err := f.st.ResolveInapplicableCheckAlerts(f.ctx, time.Now()); err != nil || alertStatus() == "resolved" {
		t.Fatalf("an applying check's alert was closed: %v", err)
	}

	off, err := f.st.DeactivatePolicy(f.ctx, pol.ID)
	if err != nil || off.Enabled {
		t.Fatalf("deactivate: %+v %v", off, err)
	}
	if got := f.reach(t, pol.ID, check.ID, models.CheckConnectivity); got != (reach{}) {
		t.Fatalf("a switched-off policy still reaches the device: %+v", got)
	}
	if _, err := f.st.ResolveInapplicableCheckAlerts(f.ctx, time.Now()); err != nil || alertStatus() != "resolved" {
		t.Fatalf("the alert of a switched-off policy stayed open: %s %v", alertStatus(), err)
	}

	review, err := f.st.PolicyReview(f.ctx, pol.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.st.ActivatePolicy(f.ctx, pol.ID, "admin", review.Fingerprint, ActivationRights{})
	if err != nil || !out.Policy.Enabled || out.ApprovedPolicy || out.ApprovedTasks != 0 {
		t.Fatalf("reactivate: %+v %v", out, err)
	}
	if got := f.reach(t, pol.ID, check.ID, models.CheckConnectivity); got != on {
		t.Fatalf("switched on again: %+v", got)
	}
}

// An AI policy is written in one piece and switched off; the database refuses
// to switch it on unreviewed; the activation refuses a stale review and an
// approval the caller may not make, then approves everything it showed.
func TestPolicyProposalIsInertUntilActivatedIntegration(t *testing.T) {
	f := newSwitchFixture(t, "policy_proposal")
	enabledTask := *shellTask("", true, "")
	enabledCheck := *memoryCheck("", true, "")
	review, err := f.st.CreatePolicyProposal(f.ctx, &PolicyProposal{
		Name: "Workstations", Description: "from MCP", ProposedBy: "mcp:api",
		Assignments: []models.PolicyAssignment{{ScopeType: models.ScopeClient, ScopeID: f.clientID}},
		// Asked for switched on: written switched off regardless.
		Tasks: []models.Task{enabledTask}, Checks: []models.Check{enabledCheck},
	})
	if err != nil {
		t.Fatal(err)
	}
	pol := review.Policy
	if pol.Enabled || !pol.Pending || review.PendingTasks != 1 || review.PendingChecks != 1 || len(review.Assignments) != 1 {
		t.Fatalf("proposal: %+v", review)
	}
	if review.Tasks[0].Enabled || review.Checks[0].Enabled || review.Tasks[0].ProposedBy != "mcp:api" {
		t.Fatalf("contents written switched on: %+v %+v", review.Tasks[0], review.Checks[0])
	}
	checkID := review.Checks[0].ID
	if got := f.reach(t, pol.ID, checkID, ""); got != (reach{}) {
		t.Fatalf("an unreviewed proposal reaches the device: %+v", got)
	}
	_, err = f.st.pool.Exec(f.ctx, `UPDATE policies SET enabled=true WHERE id=$1`, pol.ID)
	if !IsProposalUnreviewed(err) {
		t.Fatalf("the database switched on an unreviewed proposal: %v", err)
	}

	for _, fp := range []string{"", "bogus"} {
		if _, err := f.st.ActivatePolicy(f.ctx, pol.ID, "admin", fp, ActivationRights{true, true}); !errors.Is(err, ErrReviewStale) || !errors.Is(err, ErrConflict) {
			t.Fatalf("fingerprint %q: %v", fp, err)
		}
	}
	// The assistant proposes one more task while the review is open.
	if _, err := f.st.CreateTask(f.ctx, shellTask(pol.ID, false, "mcp:api")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ActivatePolicy(f.ctx, pol.ID, "admin", review.Fingerprint, ActivationRights{true, true}); !errors.Is(err, ErrReviewStale) {
		t.Fatalf("a review that missed a task was accepted: %v", err)
	}
	// A person adds a task of their own, switched off; it stays off.
	human, err := f.st.CreateTask(f.ctx, shellTask(pol.ID, false, ""))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := f.st.PolicyReview(f.ctx, pol.ID)
	if err != nil || fresh.PendingTasks != 2 || fresh.Fingerprint == review.Fingerprint {
		t.Fatalf("fresh review: %+v %v", fresh, err)
	}
	if _, err := f.st.ActivatePolicy(f.ctx, pol.ID, "admin", fresh.Fingerprint, ActivationRights{ApproveChecks: true}); !errors.Is(err, ErrApprovalNotPermitted) {
		t.Fatalf("tasks approved without the tasks permission: %v", err)
	}
	if _, err := f.st.ActivatePolicy(f.ctx, pol.ID, "admin", fresh.Fingerprint, ActivationRights{ApproveTasks: true}); !errors.Is(err, ErrApprovalNotPermitted) {
		t.Fatalf("checks approved without the checks permission: %v", err)
	}
	if still, _ := f.st.GetPolicy(f.ctx, pol.ID); still.Enabled {
		t.Fatal("a refused activation switched the policy on")
	}

	out, err := f.st.ActivatePolicy(f.ctx, pol.ID, "admin", fresh.Fingerprint, ActivationRights{true, true})
	if err != nil || !out.ApprovedPolicy || out.ApprovedTasks != 2 || out.ApprovedChecks != 1 || !out.Policy.Enabled || out.Policy.ApprovedBy != "admin" || out.Policy.Pending {
		t.Fatalf("activation: %+v %v", out, err)
	}
	if got := f.reach(t, pol.ID, checkID, ""); !got.tasks || !got.checks || !got.checkApplies || !got.runNow {
		t.Fatalf("an activated policy does not reach the device: %+v", got)
	}
	tasks, err := f.st.ListTasks(f.ctx, "", pol.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.ID == human.ID && task.Enabled {
			t.Fatal("the person's switched-off task was switched on")
		}
		if task.ProposedBy != "" && (!task.Enabled || task.ApprovedBy != "admin") {
			t.Fatalf("an approved proposal: %+v", task)
		}
	}
	// Switched off and on again by someone else: the first reviewer stays.
	if _, err := f.st.DeactivatePolicy(f.ctx, pol.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := f.st.PolicyReview(f.ctx, pol.ID)
	second, err := f.st.ActivatePolicy(f.ctx, pol.ID, "other", again.Fingerprint, ActivationRights{})
	if err != nil || second.ApprovedPolicy || second.Policy.ApprovedBy != "admin" {
		t.Fatalf("second activation: %+v %v", second, err)
	}
	if _, err := f.st.ActivatePolicy(f.ctx, uuid.NewString(), "admin", "x", ActivationRights{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing policy: %v", err)
	}
}

// An AI patch policy is written switched off with its scopes, covers no device,
// cannot be switched on by an edit, and is activated only from a fresh review.
func TestPatchPolicyProposalIsInertUntilActivatedIntegration(t *testing.T) {
	f := newSwitchFixture(t, "patch_proposal")
	review, err := f.st.CreatePatchPolicyProposal(f.ctx, &models.PatchPolicy{
		Name: "Weekend", Enabled: true, AutoApprove: []string{models.PatchClassSecurity}, AutoApproveAgeDays: 3,
		WindowDays: []int{0, 6}, WindowEndMin: 1439, Timezone: "America/Sao_Paulo", Reboot: models.RebootAlways, ProposedBy: "mcp:api",
	}, []models.PatchPolicyAssignment{{ScopeType: models.ScopeSite, ScopeID: f.siteID}})
	if err != nil {
		t.Fatal(err)
	}
	p := review.Policy
	if p.Enabled || !p.Pending || p.ProposedBy != "mcp:api" || len(review.Assignments) != 1 || p.Timezone != "America/Sao_Paulo" {
		t.Fatalf("proposal: %+v", review)
	}
	covering := func() int {
		pols, err := f.st.PatchPoliciesForAgent(f.ctx, f.device)
		if err != nil {
			t.Fatal(err)
		}
		return len(pols)
	}
	if covering() != 0 {
		t.Fatal("an unreviewed patch policy covers the device")
	}
	edited := p
	edited.Enabled = true
	if err := f.st.UpdatePatchPolicy(f.ctx, &edited); !IsProposalUnreviewed(err) {
		t.Fatalf("an edit switched on an unreviewed proposal: %v", err)
	}
	if err := f.st.AddPatchPolicyAssignment(f.ctx, p.ID, models.ScopeClient, f.clientID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.ActivatePatchPolicy(f.ctx, p.ID, "admin", review.Fingerprint); !errors.Is(err, ErrReviewStale) {
		t.Fatalf("a review that missed a scope was accepted: %v", err)
	}
	fresh, err := f.st.PatchPolicyReview(f.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, approved, err := f.st.ActivatePatchPolicy(f.ctx, p.ID, "admin", fresh.Fingerprint)
	if err != nil || !approved || !got.Enabled || got.ApprovedBy != "admin" || got.Pending {
		t.Fatalf("activation: %+v %v %v", got, approved, err)
	}
	if covering() != 1 {
		t.Fatal("the activated patch policy does not cover the device")
	}
	// A person's patch policy keeps the switch they set, as before.
	mine, err := f.st.CreatePatchPolicy(f.ctx, &models.PatchPolicy{Name: "Mine", Enabled: true})
	if err != nil || !mine.Enabled || mine.Pending || mine.ProposedBy != "" {
		t.Fatalf("a person's patch policy: %+v %v", mine, err)
	}
}

// Migration 032 keeps what applied applying: an AI policy someone assigned was
// reviewed in practice; an unassigned one waits switched off.
func TestPolicyActivationMigrationIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newMigrationTestStore(t, url)
	ctx := context.Background()
	if err := st.migrate(ctx, migrations[:31]); err != nil {
		t.Fatal(err)
	}
	assigned, unassigned, human, patch := newID(), newID(), newID(), newID()
	for _, stmt := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO policies (id, name, proposed_by) VALUES ($1,'assigned AI','ai:dana'), ($2,'unassigned AI','mcp:claude'), ($3,'human','')`, []any{assigned, unassigned, human}},
		{`INSERT INTO policy_assignments (id, policy_id, scope_type, scope_id) VALUES ($1,$2,'client',$3)`, []any{newID(), assigned, newID()}},
		{`INSERT INTO patch_policies (id, name, enabled) VALUES ($1,'legacy',true)`, []any{patch}},
	} {
		if _, err := st.pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]struct {
		enabled, pending bool
		approvedBy       string
	}{
		assigned:   {true, false, "migration:032"},
		unassigned: {false, true, ""},
		human:      {true, false, ""},
	} {
		p, err := st.GetPolicy(ctx, id)
		if err != nil || p.Enabled != want.enabled || p.Pending != want.pending || p.ApprovedBy != want.approvedBy {
			t.Fatalf("%s: %+v %v, want %+v", id, p, err, want)
		}
	}
	legacy, err := st.GetPatchPolicy(ctx, patch)
	if err != nil || !legacy.Enabled || legacy.Pending || legacy.ProposedBy != "" {
		t.Fatalf("legacy patch policy: %+v %v", legacy, err)
	}
	assertCurrentMigrationState(t, st)
}

package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// Targeting a POLICY rather than a device is the wider blast radius: a task on a
// policy runs on every machine the policy is assigned to. The scope rule there is
// deliberately stricter than "can see it" — PolicyManageAllowedForUser requires
// every assignment to be wholly inside the caller's scope — and none of that
// branch was exercised. Neither was readTarget, the read-side twin that the list
// tools rely on for exactly the same decision.

// policyAssignedTo creates a policy bound to one client, which is what makes it
// manageable by a principal scoped to that client.
func (f proposalFixture) policyAssignedTo(t *testing.T, clientID string) string {
	t.Helper()
	p, err := f.st.CreatePolicy(f.ctx, "pol-"+uuid.NewString(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if clientID != "" {
		if _, err := f.st.AssignPolicy(f.ctx, p.ID, models.ScopeClient, clientID); err != nil {
			t.Fatal(err)
		}
	}
	return p.ID
}

func policyTaskArgs(policyID string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"policy_id": policyID, "name": "nightly", "shell": "bash",
		"script": "echo hi", "schedule_kind": "interval", "interval_sec": 3600,
	})
	return b
}

// A restricted technician may propose against a policy that lives entirely
// inside their scope, and the result is still a disabled proposal.
func TestProposeTaskAgainstAnInScopePolicyIntegration(t *testing.T) {
	f := newProposalFixture(t)
	policyID := f.policyAssignedTo(t, f.mineClient)

	res := Execute(f.ctx, f.tc(), "propose_task", policyTaskArgs(policyID))
	if strings.Contains(res.Output, `"error"`) {
		t.Fatalf("propose_task refused an in-scope policy: %s", res.Output)
	}
	tasks, err := f.st.ListTasks(f.ctx, "", policyID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected one task on the policy, got %d", len(tasks))
	}
	if tasks[0].Enabled || tasks[0].ProposedBy == "" {
		t.Fatalf("a policy-targeted proposal must be disabled and stamped: %+v", tasks[0])
	}
}

// A policy reaching another tenant is refused, and nothing is written. This is
// the case that matters: the task would otherwise run on their machines.
func TestProposeTaskAgainstAnOutOfScopePolicyIsRefusedIntegration(t *testing.T) {
	f := newProposalFixture(t)
	policyID := f.policyAssignedTo(t, f.theirClient)

	res := Execute(f.ctx, f.tc(), "propose_task", policyTaskArgs(policyID))
	if !strings.Contains(res.Output, "policy is outside your access scope") {
		t.Fatalf("a cross-tenant policy was accepted: %s", res.Output)
	}
	if tasks, _ := f.st.ListTasks(f.ctx, "", policyID); len(tasks) != 0 {
		t.Fatalf("the refused proposal still wrote %d task(s)", len(tasks))
	}
}

// An unassigned policy is global. A scoped principal cannot manage one, because
// a later assignment could point it anywhere — including outside their scope.
func TestProposeTaskAgainstAnUnassignedPolicyIsRefusedForScopedUsersIntegration(t *testing.T) {
	f := newProposalFixture(t)
	policyID := f.policyAssignedTo(t, "")

	if res := Execute(f.ctx, f.tc(), "propose_task", policyTaskArgs(policyID)); !strings.Contains(res.Output, "policy is outside your access scope") {
		t.Fatalf("a scoped principal reached an unassigned global policy: %s", res.Output)
	}

	// Unscoped, the same policy is fair game.
	tc := f.tc()
	tc.ScopeUserID = ""
	if res := Execute(f.ctx, tc, "propose_task", policyTaskArgs(policyID)); strings.Contains(res.Output, `"error"`) {
		t.Fatalf("an unscoped admin was refused an unassigned policy: %s", res.Output)
	}
}

// An unscoped caller naming a policy that does not exist gets a clear refusal
// rather than a foreign-key error surfaced to the model.
func TestProposeTaskAgainstAMissingPolicyIsRefusedIntegration(t *testing.T) {
	f := newProposalFixture(t)
	tc := f.tc()
	tc.ScopeUserID = ""

	res := Execute(f.ctx, tc, "propose_task", policyTaskArgs(uuid.NewString()))
	if !strings.Contains(res.Output, "no such policy") {
		t.Fatalf("a missing policy produced %s", res.Output)
	}
}

// Exactly one owner. Both, or neither, is a definition the console could not
// have produced and the REST API would reject.
func TestProposeTaskRequiresExactlyOneOwnerIntegration(t *testing.T) {
	f := newProposalFixture(t)
	policyID := f.policyAssignedTo(t, f.mineClient)

	both, _ := json.Marshal(map[string]any{
		"agent_id": f.mineAgent, "policy_id": policyID, "name": "x", "shell": "bash",
		"script": "echo hi", "schedule_kind": "interval", "interval_sec": 3600,
	})
	if res := Execute(f.ctx, f.tc(), "propose_task", both); !strings.Contains(res.Output, "exactly one of agent_id or policy_id") {
		t.Errorf("naming both owners was accepted: %s", res.Output)
	}
	neither, _ := json.Marshal(map[string]any{
		"name": "x", "shell": "bash", "script": "echo hi",
		"schedule_kind": "interval", "interval_sec": 3600,
	})
	if res := Execute(f.ctx, f.tc(), "propose_task", neither); !strings.Contains(res.Output, "exactly one of agent_id or policy_id") {
		t.Errorf("naming no owner was accepted: %s", res.Output)
	}
}

// ---- the read side: readTarget ----

// The list tools carry the same scope decision as the proposal tools, on a
// looser rule: a policy only has to be visible, not manageable.
func TestListToolsRespectTheCallerScopeIntegration(t *testing.T) {
	f := newProposalFixture(t)
	Execute(f.ctx, f.tc(), "propose_task", taskArgs(f.mineAgent, nil))

	mine := Execute(f.ctx, f.tc(), "list_tasks", json.RawMessage(`{"agent_id":"`+f.mineAgent+`"}`))
	if strings.Contains(mine.Output, `"error"`) {
		t.Fatalf("list_tasks refused an in-scope device: %s", mine.Output)
	}
	if !strings.Contains(mine.Output, "nightly") {
		t.Errorf("list_tasks did not return the device's task: %s", mine.Output)
	}
	// It must also surface the review state, or nobody can be told a proposal is
	// pending without opening the console.
	if !strings.Contains(mine.Output, "proposed_by") {
		t.Errorf("list_tasks hides the proposal provenance: %s", mine.Output)
	}

	theirs := Execute(f.ctx, f.tc(), "list_tasks", json.RawMessage(`{"agent_id":"`+f.theirAgent+`"}`))
	if !strings.Contains(theirs.Output, "outside your access scope") {
		t.Fatalf("list_tasks enumerated an out-of-scope device: %s", theirs.Output)
	}

	checks := Execute(f.ctx, f.tc(), "list_checks", json.RawMessage(`{"agent_id":"`+f.theirAgent+`"}`))
	if !strings.Contains(checks.Output, "outside your access scope") {
		t.Fatalf("list_checks enumerated an out-of-scope device: %s", checks.Output)
	}

	if res := Execute(f.ctx, f.tc(), "list_tasks", json.RawMessage(`{}`)); !strings.Contains(res.Output, "exactly one of agent_id or policy_id") {
		t.Errorf("list_tasks accepted no owner: %s", res.Output)
	}
}

// Reading a policy's contents needs visibility only, which is why an assigned
// in-scope policy works while another tenant's does not.
func TestListToolsScopePolicyReadsIntegration(t *testing.T) {
	f := newProposalFixture(t)
	mineP := f.policyAssignedTo(t, f.mineClient)
	theirP := f.policyAssignedTo(t, f.theirClient)

	if res := Execute(f.ctx, f.tc(), "list_tasks", json.RawMessage(`{"policy_id":"`+mineP+`"}`)); strings.Contains(res.Output, `"error"`) {
		t.Errorf("list_tasks refused a visible policy: %s", res.Output)
	}
	if res := Execute(f.ctx, f.tc(), "list_checks", json.RawMessage(`{"policy_id":"`+theirP+`"}`)); !strings.Contains(res.Output, "policy is outside your access scope") {
		t.Errorf("list_checks read another tenant's policy: %s", res.Output)
	}
}

// list_policies takes no target, so its scoping is the store's: a restricted
// principal sees only policies reaching their own machines.
func TestListPoliciesToolIsScopedIntegration(t *testing.T) {
	f := newProposalFixture(t)
	mineP := f.policyAssignedTo(t, f.mineClient)
	theirP := f.policyAssignedTo(t, f.theirClient)

	scoped := Execute(f.ctx, f.tc(), "list_policies", json.RawMessage(`{}`))
	if !strings.Contains(scoped.Output, mineP) {
		t.Errorf("list_policies hid an in-scope policy: %s", scoped.Output)
	}
	if strings.Contains(scoped.Output, theirP) {
		t.Errorf("list_policies leaked another tenant's policy: %s", scoped.Output)
	}

	tc := f.tc()
	tc.ScopeUserID = ""
	unscoped := Execute(f.ctx, tc, "list_policies", json.RawMessage(`{}`))
	if !strings.Contains(unscoped.Output, mineP) || !strings.Contains(unscoped.Output, theirP) {
		t.Errorf("an unscoped admin should see every policy: %s", unscoped.Output)
	}
}

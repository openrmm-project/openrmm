package ai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

func policyNames(t *testing.T, f scopeFixture) map[string]bool {
	t.Helper()
	pols, err := f.st.ListPolicies(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, p := range pols {
		out[p.Name] = true
	}
	return out
}

// A model proposes a whole policy — scopes, tasks, checks — in one call. All of
// it is written switched off and reaches no device; a refusal anywhere in the
// request writes nothing at all. These run the real tool against a real
// database (CI only).
func TestProposePolicyInOnePieceIsSwitchedOffIntegration(t *testing.T) {
	f := newScopeFixture(t)
	tc := ToolContext{Store: f.st, Jobs: f.js, Actor: "mcp:api", Permissions: map[string]bool{
		models.PermPoliciesManage: true, models.PermPoliciesView: true, models.PermTasksManage: true, models.PermChecksManage: true,
	}}
	name := "AI baseline " + uuid.NewString()[:8]
	args, _ := json.Marshal(map[string]any{
		"name": name, "description": "Workstations of one client",
		"assignments": []map[string]string{{"scope_type": "client", "scope_id": f.theirClient}, {"scope_type": "client", "scope_id": f.theirClient}},
		// A model that asks for its task switched on gets it switched off.
		"tasks": []map[string]any{{"name": "Clean temp", "shell": "bash", "script": "find /tmp -mtime +7 -delete",
			"schedule_kind": "daily", "run_at_minute": 180, "timezone": "America/Sao_Paulo", "enabled": true}},
		"checks": []map[string]any{{"type": "diskspace", "name": "Disk", "warn_threshold": 20, "err_threshold": 10, "config": map[string]string{"path": "/"}}},
	})
	res := Execute(f.ctx, tc, "propose_policy", args)
	var out struct {
		ID, Status            string
		Scopes, Tasks, Checks int
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil || out.Status != "awaiting_review" || out.Scopes != 1 || out.Tasks != 1 || out.Checks != 1 {
		t.Fatalf("propose_policy: %s", res.Output)
	}
	t.Cleanup(func() { _ = f.st.DeletePolicy(f.ctx, out.ID) })
	review, err := f.st.PolicyReview(f.ctx, out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if review.Policy.Enabled || !review.Policy.Pending || review.Policy.ProposedBy != "mcp:api" || review.PendingTasks != 1 || review.PendingChecks != 1 {
		t.Fatalf("stored proposal: %+v", review)
	}
	if review.Tasks[0].Enabled || review.Checks[0].Enabled || review.Tasks[0].Timezone != "America/Sao_Paulo" {
		t.Fatalf("contents: %+v %+v", review.Tasks[0], review.Checks[0])
	}
	if agents, err := f.st.AgentIDsForPolicy(f.ctx, out.ID); err != nil || len(agents) != 0 {
		t.Fatalf("an unreviewed policy reaches devices: %v %v", agents, err)
	}
	listed := Execute(f.ctx, tc, "list_policies", json.RawMessage(`{}`)).Output
	var pols []models.Policy
	if err := json.Unmarshal([]byte(listed), &pols); err != nil {
		t.Fatalf("list_policies: %s", listed)
	}
	found := false
	for _, p := range pols {
		if p.ID == out.ID {
			found = !p.Enabled && p.Pending
		}
	}
	if !found {
		t.Fatalf("list_policies does not show the proposal switched off and pending: %s", listed)
	}

	// Refusals leave nothing half-created: an unknown scope, and a valid task
	// followed by an invalid one.
	for i, bad := range []map[string]any{
		{"name": name + " scope", "assignments": []map[string]string{{"scope_type": "client", "scope_id": uuid.NewString()}}},
		{"name": name + " tasks", "tasks": []map[string]any{
			{"name": "ok", "shell": "sh", "script": "true", "schedule_kind": "interval", "interval_sec": 600},
			{"name": "bad", "shell": "sh", "script": "true", "schedule_kind": "weekly"},
		}},
	} {
		raw, _ := json.Marshal(bad)
		if res := Execute(f.ctx, tc, "propose_policy", raw); !strings.Contains(res.Output, "error") {
			t.Fatalf("case %d accepted: %s", i, res.Output)
		}
		if policyNames(t, f)[bad["name"].(string)] {
			t.Fatalf("case %d left a policy behind", i)
		}
	}
	// A restricted principal proposes no global policy, whatever it carries.
	scoped := f.scopedContext()
	scoped.Permissions[models.PermPoliciesManage] = true
	if res := Execute(f.ctx, scoped, "propose_policy", args); !strings.Contains(res.Output, "scoped users cannot") {
		t.Fatalf("scoped proposal: %s", res.Output)
	}
}

// A model proposes a patch policy switched off: it covers no device and is
// listed as awaiting review until a person activates it.
func TestProposePatchPolicyIsSwitchedOffIntegration(t *testing.T) {
	f := newScopeFixture(t)
	tc := ToolContext{Store: f.st, Jobs: f.js, Actor: "mcp:api", Permissions: map[string]bool{models.PermPatchPoliciesManage: true}}
	args, _ := json.Marshal(map[string]any{
		"name": "AI weekend " + uuid.NewString()[:8], "auto_approve": []string{"security", "critical"}, "auto_approve_age_days": 3,
		"window_days": []int{0, 6}, "window_start_min": 120, "window_end_min": 360, "timezone": "America/Sao_Paulo", "reboot": "always",
		"assignments": []map[string]string{{"scope_type": "client", "scope_id": f.theirClient}},
	})
	res := Execute(f.ctx, tc, "propose_patch_policy", args)
	var out struct{ ID, Kind, Status string }
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil || out.Status != "awaiting_review" || out.Kind != "patch_policy" {
		t.Fatalf("propose_patch_policy: %s", res.Output)
	}
	t.Cleanup(func() { _ = f.st.DeletePatchPolicy(f.ctx, out.ID) })
	p, err := f.st.GetPatchPolicy(f.ctx, out.ID)
	if err != nil || p.Enabled || !p.Pending || p.Reboot != models.RebootAlways || p.Timezone != "America/Sao_Paulo" || p.WindowStartMin != 120 {
		t.Fatalf("stored proposal: %+v %v", p, err)
	}
	covering, err := f.st.PatchPoliciesForAgent(f.ctx, f.theirAgent)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range covering {
		if c.ID == out.ID {
			t.Fatal("an unreviewed patch policy covers a device")
		}
	}
	if listed := Execute(f.ctx, tc, "list_patch_policies", json.RawMessage(`{}`)).Output; !strings.Contains(listed, out.ID) || !strings.Contains(listed, `"pending":true`) {
		t.Fatalf("list_patch_policies: %s", listed)
	}
	scoped := f.scopedContext()
	scoped.Permissions[models.PermPatchPoliciesManage] = true
	if res := Execute(f.ctx, scoped, "propose_patch_policy", args); !strings.Contains(res.Output, "scoped users cannot") {
		t.Fatalf("scoped patch policy proposal: %s", res.Output)
	}
}

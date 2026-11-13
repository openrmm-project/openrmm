package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// propose_patch_policy and list_patch_policies are offered only with the patch
// policy permission; proposals are changes, the listing a read; and propose
// policy declares its scopes, tasks and checks without accepting anything else.
func TestPolicyProposalToolsCatalogAndClasses(t *testing.T) {
	offered := func(perms ...string) map[string]bool {
		set := map[string]bool{}
		for _, p := range perms {
			set[p] = true
		}
		out := map[string]bool{}
		for _, tool := range ToolsForPermissions(set) {
			out[tool.Function.Name] = true
		}
		return out
	}
	if got := offered(models.PermPoliciesManage, models.PermTasksManage, models.PermChecksManage); got["propose_patch_policy"] || got["list_patch_policies"] {
		t.Fatal("patch policy tools offered without patches.policies.manage")
	}
	if got := offered(models.PermPatchPoliciesManage); !got["propose_patch_policy"] || !got["list_patch_policies"] || got["propose_policy"] {
		t.Fatalf("patches.policies.manage: %v", got)
	}
	for name, want := range map[string]string{"propose_patch_policy": ClassChange, "list_patch_policies": ClassConsoleRead, "propose_policy": ClassChange} {
		if got, ok := ToolRiskClass(name); !ok || got != want {
			t.Fatalf("%s class = %q, want %q", name, got, want)
		}
	}
	for _, tool := range ToolsForPermissions(map[string]bool{models.PermPoliciesManage: true, models.PermPatchPoliciesManage: true}) {
		switch tool.Function.Name {
		case "propose_policy", "propose_patch_policy":
		default:
			continue
		}
		var schema struct {
			AdditionalProperties *bool                      `json:"additionalProperties"`
			Properties           map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			t.Fatalf("%s accepts undeclared arguments: %v", tool.Function.Name, err)
		}
		if _, ok := schema.Properties["assignments"]; !ok {
			t.Fatalf("%s has no assignments", tool.Function.Name)
		}
		if tool.Function.Name == "propose_policy" {
			for _, field := range []string{"tasks", "checks"} {
				var list struct {
					Items struct {
						AdditionalProperties *bool `json:"additionalProperties"`
					} `json:"items"`
				}
				if err := json.Unmarshal(schema.Properties[field], &list); err != nil || list.Items.AdditionalProperties == nil || *list.Items.AdditionalProperties {
					t.Fatalf("propose_policy %s accept undeclared fields", field)
				}
			}
			// No field can ask for the policy or its contents switched on.
			if _, ok := schema.Properties["enabled"]; ok {
				t.Fatal("propose_policy offers an enabled flag")
			}
		}
		if !strings.Contains(tool.Function.Description, "SWITCHED OFF") {
			t.Fatalf("%s does not tell the model it is created switched off", tool.Function.Name)
		}
	}
	// There is still no tool that assigns, activates or approves.
	for _, tool := range ToolsForPermissions(map[string]bool{models.PermPoliciesManage: true, models.PermPatchPoliciesManage: true,
		models.PermTasksManage: true, models.PermChecksManage: true}) {
		for _, verb := range []string{"activate", "assign", "approve_", "enable"} {
			if strings.Contains(tool.Function.Name, verb) {
				t.Errorf("tool %q lets the model %s", tool.Function.Name, verb)
			}
		}
	}
}

// Refusals that need no database: everything is validated before the single
// transaction that writes the proposal, so a refusal leaves nothing behind.
func TestPolicyProposalsRefuseBeforeWriting(t *testing.T) {
	ctx := context.Background()
	all := map[string]bool{models.PermPoliciesManage: true, models.PermTasksManage: true, models.PermChecksManage: true, models.PermPatchPoliciesManage: true}
	full := ToolContext{Actor: "mcp:api", Permissions: all}
	scoped := ToolContext{Actor: "tech", ScopeUserID: "user-1", Permissions: all}
	task := `{"name":"Clean","shell":"powershell","script":"Get-Date","schedule_kind":"daily","run_at_minute":180,"timezone":"America/Sao_Paulo"}`
	check := `{"type":"diskspace","name":"Disk","warn_threshold":20,"err_threshold":10,"config":{"path":"C:"}}`
	many := func(item string, n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = item
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	for name, tc := range map[string]struct {
		ctx  ToolContext
		args string
		want string
	}{
		"scoped principal":     {scoped, `{"name":"P"}`, "scoped users"},
		"no name":              {full, `{"name":" "}`, "name is required"},
		"tasks without perm":   {ToolContext{Actor: "x", Permissions: map[string]bool{models.PermPoliciesManage: true}}, `{"name":"P","tasks":[` + task + `]}`, "tasks.manage"},
		"checks without perm":  {ToolContext{Actor: "x", Permissions: map[string]bool{models.PermPoliciesManage: true}}, `{"name":"P","checks":[` + check + `]}`, "checks.manage"},
		"too many tasks":       {full, `{"name":"P","tasks":` + many(task, maxPolicyProposalItems+1) + `}`, "at most"},
		"too many checks":      {full, `{"name":"P","checks":` + many(check, maxPolicyProposalItems+1) + `}`, "at most"},
		"too many scopes":      {full, `{"name":"P","assignments":` + many(`{"scope_type":"client","scope_id":"x"}`, maxPolicyProposalItems+1) + `}`, "at most"},
		"unknown scope type":   {full, `{"name":"P","assignments":[{"scope_type":"tenant","scope_id":"00000000-0000-0000-0000-000000000001"}]}`, "scope_type must be"},
		"scope id not an id":   {full, `{"name":"P","assignments":[{"scope_type":"client","scope_id":"Acme"}]}`, "not an id"},
		"bad task shell":       {full, `{"name":"P","tasks":[{"name":"T","shell":"zsh","script":"x","schedule_kind":"daily"}]}`, "task 1"},
		"bad task schedule":    {full, `{"name":"P","tasks":[{"name":"T","shell":"sh","script":"x","schedule_kind":"daily","run_at_minute":1440}]}`, "task 1"},
		"bad task zone":        {full, `{"name":"P","tasks":[{"name":"T","shell":"sh","script":"x","schedule_kind":"daily","timezone":"Mars/Olympus"}]}`, "task 1"},
		"bad check thresholds": {full, `{"name":"P","checks":[{"type":"diskspace","name":"D","warn_threshold":5,"err_threshold":10}]}`, "check 1"},
		"unknown check type":   {full, `{"name":"P","checks":[{"type":"telepathy","name":"D"}]}`, "check 1"},
	} {
		// No store in this test: reaching the write would panic, so each case
		// also proves the refusal happens before anything is written.
		out, err := proposePolicy(ctx, tc.ctx, json.RawMessage(tc.args))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v %v, want an error containing %q", name, out, err, tc.want)
		}
	}

	for name, tc := range map[string]struct {
		ctx  ToolContext
		args string
		want string
	}{
		"scoped principal":  {scoped, `{"name":"W"}`, "scoped users"},
		"no name":           {full, `{"name":""}`, "name is required"},
		"unknown class":     {full, `{"name":"W","auto_approve":["all"]}`, "classification"},
		"window past day":   {full, `{"name":"W","window_end_min":1440}`, "minutes of the day"},
		"weekday out":       {full, `{"name":"W","window_days":[7]}`, "weekdays"},
		"bad reboot":        {full, `{"name":"W","reboot":"maybe"}`, "reboot"},
		"bad zone":          {full, `{"name":"W","timezone":"Nowhere/Here"}`, ""},
		"agent scope bad":   {full, `{"name":"W","assignments":[{"scope_type":"group","scope_id":"00000000-0000-0000-0000-000000000001"}]}`, "scope_type must be"},
		"negative age days": {full, `{"name":"W","auto_approve":["security"],"auto_approve_age_days":-3}`, "auto_approve_age_days"},
	} {
		out, err := proposePatchPolicy(ctx, tc.ctx, json.RawMessage(tc.args))
		if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: %v %v, want an error containing %q", name, out, err, tc.want)
		}
	}
}

// A scope id is stored in one spelling, so a model cannot create a duplicate
// assignment with a braced or upper-case form of the same id.
func TestProposalScopesAreCanonical(t *testing.T) {
	const id = "00000000-0000-4000-8000-0000000000ab"
	for _, spelling := range []string{id, strings.ToUpper(id), "{" + id + "}", "urn:uuid:" + id} {
		if got, err := uuidCanonical(spelling); err != nil || got != id {
			t.Fatalf("%s -> %q %v", spelling, got, err)
		}
	}
	if _, err := uuidCanonical("Acme"); err == nil {
		t.Fatal("a name was accepted as an id")
	}
}

// The shared builders keep propose_task and propose_check on the console's
// validation and always produce an unapproved, switched-off proposal.
func TestProposedTaskAndCheckBuildersAreAlwaysSwitchedOff(t *testing.T) {
	tc := ToolContext{Actor: "mcp:api"}
	task, err := proposedTaskArgs{Name: " Clean ", Shell: "sh", Script: "true", ScheduleKind: "interval", IntervalSec: 3600}.task(tc)
	if err != nil || task.Enabled || task.ProposedBy != "mcp:api" || task.ApprovedBy != "" || task.Name != "Clean" {
		t.Fatalf("task: %+v %v", task, err)
	}
	defaulted, err := proposedTaskArgs{Name: "T", Shell: "sh", Script: "true", IntervalSec: 600}.task(tc)
	if err != nil || defaulted.ScheduleKind != models.ScheduleInterval {
		t.Fatalf("schedule default: %+v %v", defaulted, err)
	}
	policy := "p1"
	check, err := proposedCheckArgs{Type: "disk_space", Name: "Disk", WarnThreshold: ptr(20), ErrThreshold: ptr(10)}.check(tc, nil, &policy)
	if err != nil || check.Enabled || check.Type != models.CheckDiskSpace || check.ProposedBy != "mcp:api" || check.IntervalSec != 300 {
		t.Fatalf("check: %+v %v", check, err)
	}
	for i, bad := range []proposedTaskArgs{{Shell: "sh", Script: "x"}, {Name: "T", Shell: "sh"}, {Name: "T", Shell: "fish", Script: "x"}} {
		if _, err := bad.task(tc); err == nil {
			t.Errorf("task %d accepted: %+v", i, bad)
		}
	}
	if out := fmt.Sprint(proposalActorFor("claude")); out != "ai:claude" {
		t.Fatalf("actor = %s", out)
	}
}

func ptr(v float64) *float64 { return &v }

// Assignments carry OS and role filters, or cover every device; exclusions
// name devices. All of it is validated before anything is written (no store
// is reachable in this test).
func TestPolicyProposalTargetingIsValidatedBeforeWriting(t *testing.T) {
	ctx := context.Background()
	scopes, err := proposalScopes(ctx, ToolContext{}, []proposedScope{
		{ScopeType: "global", OS: "windows", Role: "server"},
		{ScopeType: "global", ScopeID: "ignored", OS: "windows", Role: "server"},
		{ScopeType: "global", OS: "linux"},
	}, models.ScopeClient)
	if err != nil || len(scopes) != 2 || scopes[0].ScopeID != models.GlobalScopeID {
		t.Fatalf("global scopes: %+v %v", scopes, err)
	}
	for name, in := range map[string]proposedScope{
		"unknown os":   {ScopeType: "global", OS: "darwin"},
		"unknown role": {ScopeType: "global", Role: "kiosk"},
		"upper case":   {ScopeType: "global", OS: "Windows"},
	} {
		if _, err := proposalScopes(ctx, ToolContext{}, []proposedScope{in}, models.ScopeClient); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	tooMany := make([]string, maxProposalExclusions+1)
	if _, err := proposalExclusions(ctx, ToolContext{}, tooMany); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("too many exclusions: %v", err)
	}
	if _, err := proposalExclusions(ctx, ToolContext{}, []string{"PC-01"}); err == nil || !strings.Contains(err.Error(), "not a device id") {
		t.Fatalf("a hostname as exclusion: %v", err)
	}
	for _, tool := range ToolsForPermissions(map[string]bool{models.PermPoliciesManage: true, models.PermPatchPoliciesManage: true}) {
		if tool.Function.Name != "propose_policy" && tool.Function.Name != "propose_patch_policy" {
			continue
		}
		schema := string(tool.Function.Parameters)
		for _, want := range []string{`"exclude_devices"`, `"os"`, `"role"`, `"global"`} {
			if !strings.Contains(schema, want) {
				t.Errorf("%s schema lacks %s", tool.Function.Name, want)
			}
		}
	}
}

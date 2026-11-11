package ai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// A model may propose a script variable and a library script, and nothing it
// proposes works before a person approves it: no value, no binding, no run, no
// schedule. These run the real tools against a real database (CI only).
func TestAIProposalsOfVariablesAndLibraryScriptsWaitForReviewIntegration(t *testing.T) {
	f := newScopeFixture(t)
	ctx := f.ctx
	cfg := &config.Config{SettingsEncryptionKey: "synthetic-proposal-test-key"}
	suffix := strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	varName := "AI_PROPOSED_KEY_" + suffix
	tc := ToolContext{
		Store: f.st, Jobs: f.js, Actor: "mcp:claude",
		Permissions: map[string]bool{
			models.PermScriptsView: true, models.PermScriptsVariables: true, models.PermScriptsManage: true, models.PermJobsCreate: true,
		},
	}
	call := func(tc ToolContext, name string, args map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		var out map[string]any
		if err := json.Unmarshal([]byte(Execute(ctx, tc, name, raw).Output), &out); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return out
	}

	// 1. The variable proposal: pending, attributed, without a value.
	res := call(tc, "propose_script_variable", map[string]any{"name": varName, "description": "Synthetic vendor key", "secret": true})
	if res["status"] != "awaiting_review" {
		t.Fatalf("propose_script_variable: %v", res)
	}
	varID := res["id"].(string)
	t.Cleanup(func() { _, _ = f.st.DeleteScriptVariable(ctx, varID) })
	listed := call(tc, "list_script_variables", map[string]any{"search": varName})
	items := listed["variables"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["awaiting_review"] != true {
		t.Fatalf("list_script_variables: %v", listed)
	}
	if _, leaked := items[0].(map[string]any)["values"]; leaked {
		t.Fatal("the model received value information")
	}
	vars, err := f.st.ListScriptVariables(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if v.ID == varID && (v.ProposedBy != "mcp:claude" || !v.Pending) {
			t.Fatalf("provenance: %+v", v)
		}
	}
	// 2. Pending: no value, no binding.
	if err := f.st.SetScriptVariableValue(ctx, cfg, varID, "global", "", "x"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("value set on a pending proposal: %v", err)
	}
	res = call(tc, "propose_library_script", map[string]any{"name": "Install vendor " + suffix, "description": "x", "shell": "powershell", "body": "Write-Output $env:" + varName, "variables": []string{varName}})
	if msg, _ := res["error"].(string); !strings.Contains(msg, "awaiting review") {
		t.Fatalf("a pending variable was bound: %v", res)
	}
	// 3. Approve; approving twice is a conflict; then a value can be set.
	if _, err := f.st.ApproveScriptVariable(ctx, varID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ApproveScriptVariable(ctx, varID, "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second approval: %v", err)
	}
	if err := f.st.SetScriptVariableValue(ctx, cfg, varID, "global", "", "synthetic-value"); err != nil {
		t.Fatal(err)
	}

	// 4. The library script proposal, binding the approved variable.
	res = call(tc, "propose_library_script", map[string]any{"name": "Install vendor " + suffix, "description": "Installs the vendor agent", "shell": "powershell", "body": "Write-Output $env:" + varName, "variables": []string{varName}})
	if res["status"] != "awaiting_review" || res["category"] != defaultProposalCategory {
		t.Fatalf("propose_library_script: %v", res)
	}
	scriptID := res["id"].(string)
	t.Cleanup(func() { _ = f.st.DeleteScript(ctx, scriptID) })
	sc, err := f.st.GetScript(ctx, scriptID)
	if err != nil {
		t.Fatal(err)
	}
	if !sc.Pending || sc.ProposedBy != "mcp:claude" || strings.Join(sc.Variables, ",") != varName {
		t.Fatalf("stored proposal: %+v", sc)
	}
	// 5. Pending: no scheduled or approval-gated run.
	if _, err := f.js.RunAutomated(ctx, f.mineAgent, sc.Shell, sc.Body, "task", nil, sc); !errors.Is(err, jobs.ErrScriptPending) {
		t.Fatalf("scheduled run of a pending script: %v", err)
	}
	if _, err := f.js.ProposeScriptWithParams(ctx, f.mineAgent, sc, nil, "run", "admin"); !errors.Is(err, jobs.ErrScriptPending) {
		t.Fatalf("library run of a pending script: %v", err)
	}
	if out := Execute(ctx, tc, "run_preapproved_action", json.RawMessage(`{"script_id":"`+scriptID+`","agent_id":"`+f.mineAgent+`"}`)); !strings.Contains(out.Output, "error") {
		t.Fatalf("autonomous run of a pending script: %s", out.Output)
	}
	// 6. Approve: the script is ordinary library content from then on.
	if _, err := f.st.ApproveScript(ctx, scriptID, "admin"); err != nil {
		t.Fatal(err)
	}
	if sc, err = f.st.GetScript(ctx, scriptID); err != nil || sc.Pending || sc.ApprovedBy != "admin" {
		t.Fatalf("after approval: %+v %v", sc, err)
	}
	if _, err := f.st.ApproveScript(ctx, scriptID, "admin"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second script approval: %v", err)
	}
}

// The library and variable definitions are global: a principal confined to
// some clients proposes neither, binding needs the variables permission, and
// reserved names are refused as they are in the console.
func TestAIProposalsRespectScopePermissionsAndReservedNamesIntegration(t *testing.T) {
	f := newScopeFixture(t)
	scoped := f.scopedContext()
	scoped.Permissions[models.PermScriptsVariables] = true
	scoped.Permissions[models.PermScriptsManage] = true
	for _, tc := range []struct {
		name string
		args string
	}{
		{"propose_script_variable", `{"name":"SCOPED_KEY","description":"x","secret":true}`},
		{"propose_library_script", `{"name":"Scoped","description":"x","shell":"bash","body":"echo hi"}`},
	} {
		if out := Execute(f.ctx, scoped, tc.name, json.RawMessage(tc.args)).Output; !strings.Contains(out, "scoped users cannot") {
			t.Fatalf("%s by a scoped user: %s", tc.name, out)
		}
	}
	full := ToolContext{Store: f.st, Jobs: f.js, Actor: "admin", Permissions: map[string]bool{models.PermScriptsManage: true, models.PermScriptsVariables: true}}
	if out := Execute(f.ctx, full, "propose_script_variable", json.RawMessage(`{"name":"PSModulePath","description":"x","secret":false}`)).Output; !strings.Contains(out, "reserved") {
		t.Fatalf("reserved name: %s", out)
	}
	noVars := ToolContext{Store: f.st, Jobs: f.js, Actor: "admin", Permissions: map[string]bool{models.PermScriptsManage: true}}
	if out := Execute(f.ctx, noVars, "propose_library_script", json.RawMessage(`{"name":"Binds","description":"x","shell":"bash","body":"echo $X","variables":["SOME_KEY"]}`)).Output; !strings.Contains(out, "scripts.variables") {
		t.Fatalf("binding without the variables permission: %s", out)
	}
}

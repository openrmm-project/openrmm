package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// CI-only PostgreSQL; real authenticated router and persistence. What AI Chat
// or MCP proposes stays inert until an unrestricted operator approves it in the
// console: a proposed variable takes no value, a proposed library script does
// not run or get released for AI autonomy, a scoped user cannot approve either,
// and approving is audited and happens once.
func TestAIProposalsReviewThroughTheAPIE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()

	variable, err := f.store.ProposeScriptVariable(ctx, "PROPOSED_KEY", "Synthetic", true, "mcp:claude")
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodPut, "/api/script-variables/"+variable.ID+"/values", `{"scope_type":"global","value":"v"}`); rec.Code != http.StatusConflict {
		t.Fatalf("value on a pending variable = %d %s", rec.Code, rec.Body.String())
	}
	rec := f.do(http.MethodGet, "/api/script-variables", "")
	var list struct {
		Variables []store.ScriptVariable `json:"variables"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Variables) != 1 || !list.Variables[0].Pending || list.Variables[0].ProposedBy != "mcp:claude" {
		t.Fatalf("listed proposal: %d %s", rec.Code, rec.Body.String())
	}

	script, err := f.store.CreateScript(ctx, &models.Script{Name: "Proposed", Category: "AI proposals", Shell: models.ShellBash, Body: "echo hi", ProposedBy: "ai:dana"})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.store.CreateAgent(ctx, "PROPOSAL-HOST", "linux", "amd64", "0.11.0", "hash-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rec := f.do(http.MethodPost, "/api/devices/"+agent.ID+"/run-script", `{"script_id":"`+script.ID+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("run of a pending script = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, "/api/scripts/"+script.ID+"/ai-release", `{"release":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("AI release of a pending script = %d %s", rec.Code, rec.Body.String())
	}

	// A scoped administrator cannot approve global definitions.
	scoped, err := f.store.CreateUserWithScopes(ctx, "scoped-"+uuid.NewString(), "hash", models.RoleAdmin, []models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: agent.ID}})
	if err != nil {
		t.Fatal(err)
	}
	adminToken := f.token
	f.token = auth.NewToken()
	if err := f.store.CreateSession(ctx, scoped.ID, auth.Hash(f.token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/script-variables/" + variable.ID + "/approve", "/api/scripts/" + script.ID + "/approve"} {
		if rec := f.do(http.MethodPost, path, `{}`); rec.Code != http.StatusForbidden {
			t.Fatalf("scoped approval %s = %d", path, rec.Code)
		}
	}
	f.token = adminToken

	// Approve each once; the second time is a conflict.
	for _, path := range []string{"/api/script-variables/" + variable.ID + "/approve", "/api/scripts/" + script.ID + "/approve"} {
		if rec := f.do(http.MethodPost, path, `{}`); rec.Code != http.StatusOK {
			t.Fatalf("approve %s = %d %s", path, rec.Code, rec.Body.String())
		}
		if rec := f.do(http.MethodPost, path, `{}`); rec.Code != http.StatusConflict {
			t.Fatalf("second approval %s = %d", path, rec.Code)
		}
	}
	if f.audit("script_variable.approve") == nil || f.audit("script.approve") == nil {
		t.Fatal("approvals were not audited")
	}
	if rec := f.do(http.MethodPut, "/api/script-variables/"+variable.ID+"/values", `{"scope_type":"global","value":"v"}`); rec.Code != http.StatusOK {
		t.Fatalf("value after approval = %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do(http.MethodGet, "/api/scripts/"+script.ID, "")
	var got models.Script
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Pending || got.ProposedBy != "ai:dana" || got.ApprovedBy == "" {
		t.Fatalf("approved script: %d %s", rec.Code, rec.Body.String())
	}
}

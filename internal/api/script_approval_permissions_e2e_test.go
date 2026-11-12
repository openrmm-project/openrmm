package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// Accepting new code that consumes vault variables needs the same permission
// as writing that code. Running an already reviewed script remains separate.
func TestScriptApprovalRequiresVariablePermissionE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	variable, err := f.store.CreateScriptVariable(ctx, "REVIEW_CREDENTIAL", "Synthetic definition", true)
	if err != nil {
		t.Fatal(err)
	}
	proposal := func(name string, vars []string) *models.Script {
		t.Helper()
		sc, err := f.store.CreateScript(ctx, &models.Script{Name: name, Shell: models.ShellPowerShell, Body: "Write-Output 'review me'", Variables: vars, ProposedBy: "mcp:synthetic-proposer"})
		if err != nil {
			t.Fatal(err)
		}
		return sc
	}
	bound := proposal("Bound proposal", []string{variable.Name})
	unbound := proposal("Unbound proposal", nil)
	restore := f.asUser(t, "", []string{models.PermScriptsView, models.PermScriptsManage}, nil)
	if rec := f.do(http.MethodPost, "/api/scripts/"+bound.ID+"/approve", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("manage-only approval of bound proposal = %d, want 403", rec.Code)
	}
	got, err := f.store.GetScript(ctx, bound.ID)
	if err != nil || !got.Pending || got.ApprovedBy != "" {
		t.Fatal("denied approval changed proposal state")
	}
	if rec := f.do(http.MethodPost, "/api/scripts/"+unbound.ID+"/approve", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("unbound control approval = %d, want 200", rec.Code)
	}
	restore()
	restore = f.asUser(t, "", []string{models.PermScriptsView, models.PermScriptsManage, models.PermScriptsVariables}, nil)
	defer restore()
	if rec := f.do(http.MethodPost, "/api/scripts/"+bound.ID+"/approve", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("variable-capable approval = %d, want 200", rec.Code)
	}
	got, err = f.store.GetScript(ctx, bound.ID)
	if err != nil || got.Pending || got.ApprovedBy == "" {
		t.Fatal("authorized approval did not persist")
	}
}

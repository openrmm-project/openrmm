package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// The new tools appear only for roles holding their permission, and both
// proposals are changes (never read-only, never autonomous).
func TestProposalToolsCatalogAndClasses(t *testing.T) {
	names := func(perms ...string) map[string]bool {
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
	none := names()
	for _, name := range []string{"list_script_variables", "propose_script_variable", "propose_library_script"} {
		if none[name] {
			t.Fatalf("%s offered without any permission", name)
		}
	}
	if got := names(models.PermScriptsView); !got["list_script_variables"] || got["propose_script_variable"] || got["propose_library_script"] {
		t.Fatalf("scripts.view: %v", got)
	}
	if got := names(models.PermScriptsVariables); !got["propose_script_variable"] || got["propose_library_script"] {
		t.Fatalf("scripts.variables: %v", got)
	}
	if got := names(models.PermScriptsManage); !got["propose_library_script"] || got["propose_script_variable"] {
		t.Fatalf("scripts.manage: %v", got)
	}
	for name, want := range map[string]string{"list_script_variables": ClassConsoleRead, "propose_script_variable": ClassChange, "propose_library_script": ClassChange} {
		if got, ok := ToolRiskClass(name); !ok || got != want {
			t.Fatalf("%s class = %q, want %q", name, got, want)
		}
	}
	for _, tool := range ToolsForPermissions(map[string]bool{models.PermScriptsVariables: true, models.PermScriptsManage: true, models.PermScriptsView: true}) {
		if !strings.HasPrefix(tool.Function.Name, "propose_") && tool.Function.Name != "list_script_variables" {
			continue
		}
		var schema struct {
			AdditionalProperties *bool `json:"additionalProperties"`
		}
		if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
			t.Fatalf("%s accepts undeclared arguments", tool.Function.Name)
		}
	}
}

// Refusals that need no database: a scoped principal, and binding variables
// without the variables permission, are turned away before any write.
func TestProposalToolsRefuseBeforeWriting(t *testing.T) {
	ctx := context.Background()
	scoped := ToolContext{Actor: "tech", ScopeUserID: "user-1", Permissions: map[string]bool{models.PermScriptsVariables: true, models.PermScriptsManage: true}}
	if _, err := proposeScriptVariable(ctx, scoped, json.RawMessage(`{"name":"KEY","description":"x","secret":true}`)); err == nil || !strings.Contains(err.Error(), "scoped users") {
		t.Fatalf("scoped variable proposal: %v", err)
	}
	if _, err := proposeLibraryScript(ctx, scoped, json.RawMessage(`{"name":"S","description":"x","shell":"bash","body":"echo"}`)); err == nil || !strings.Contains(err.Error(), "scoped users") {
		t.Fatalf("scoped script proposal: %v", err)
	}
	full := ToolContext{Actor: "admin", Permissions: map[string]bool{models.PermScriptsManage: true}}
	if _, err := proposeLibraryScript(ctx, full, json.RawMessage(`{"name":"S","description":"x","shell":"bash","body":"echo $K","variables":["K"]}`)); err == nil || !strings.Contains(err.Error(), "scripts.variables") {
		t.Fatalf("binding without permission: %v", err)
	}
	if _, err := proposeLibraryScript(ctx, full, json.RawMessage(`{"name":"S","description":"x","shell":"zsh","body":"echo"}`)); err == nil {
		t.Fatal("unsupported shell accepted")
	}
	if _, err := proposeLibraryScript(ctx, full, json.RawMessage(`{"name":"","description":"x","shell":"bash","body":"echo"}`)); err == nil {
		t.Fatal("empty name accepted")
	}
	if out := proposalActorFor("mcp:claude"); out != "mcp:claude" {
		t.Fatalf("mcp provenance: %s", out)
	}
	if out := proposalActorFor("dana"); out != "ai:dana" {
		t.Fatalf("chat provenance: %s", out)
	}
}

func proposalActorFor(actor string) string { return ToolContext{Actor: actor}.proposalActor() }

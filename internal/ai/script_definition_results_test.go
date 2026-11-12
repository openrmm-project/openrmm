package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

func TestScriptDefinitionErrorsHaveModelBudget(t *testing.T) {
	tc := ToolContext{Actor: "synthetic", Permissions: map[string]bool{models.PermScriptsVariables: true, models.PermScriptsManage: true}}
	largeReserved := "PYTHON" + strings.Repeat("A", 128<<10)
	for _, tool := range []string{"propose_script_variable", "propose_library_script"} {
		t.Run(tool, func(t *testing.T) {
			var args []byte
			if tool == "propose_script_variable" {
				args, _ = json.Marshal(map[string]any{"name": largeReserved})
			} else {
				args, _ = json.Marshal(map[string]any{"name": "Fixture", "shell": "sh", "body": "echo ok", "variables": []string{largeReserved}})
			}
			// Name validation fails before any database access. In the old dispatcher
			// the full 128 KiB name was reflected into the response and chat history.
			result := Execute(context.Background(), tc, tool, args)
			if len(result.Output) > maxScriptDefinitionResultBytes || !json.Valid([]byte(result.Output)) {
				t.Fatalf("%s exceeded the JSON model budget", tool)
			}
			var parsed map[string]any
			_ = json.Unmarshal([]byte(result.Output), &parsed)
			if parsed["truncated"] != true || parsed["error"] == nil {
				t.Fatal("oversized error was not explicitly reported")
			}
		})
	}
	short := Execute(context.Background(), tc, "propose_script_variable", json.RawMessage(`{"name":"PATH"}`))
	if !strings.Contains(short.Output, "reserved") || strings.Contains(short.Output, "truncated") {
		t.Fatal("ordinary validation control changed")
	}
}

func TestScriptDefinitionResultPreservesSuccessfulProposalIdentity(t *testing.T) {
	value := map[string]any{"id": "fixture-id", "kind": "script_variable", "status": "awaiting_review", "name": strings.Repeat("V", 128<<10)}
	result := scriptDefinitionResult(value, nil)
	var parsed map[string]any
	if len(result.Output) > maxScriptDefinitionResultBytes || json.Unmarshal([]byte(result.Output), &parsed) != nil {
		t.Fatal("invalid bounded result")
	}
	for _, key := range []string{"id", "kind", "status"} {
		if parsed[key] != value[key] {
			t.Fatal("proposal identity lost")
		}
	}
	if parsed["truncated"] != true {
		t.Fatal("omission was silent")
	}
	small := map[string]any{"id": "fixture-id", "status": "awaiting_review", "name": "customerKey"}
	if scriptDefinitionResult(small, nil).Output != jsonResult(small, nil).Output {
		t.Fatal("ordinary successful result changed")
	}
	result = scriptDefinitionResult(nil, errors.New("invalid \x00 value"))
	if !json.Valid([]byte(result.Output)) {
		t.Fatal("validation error is not JSON")
	}
}

func TestScriptVariableListHasSerializedByteBudgetAndExactNames(t *testing.T) {
	definitions := []store.ScriptVariableModelDefinition{}
	for i := 0; i < 200; i++ {
		definitions = append(definitions, store.ScriptVariableModelDefinition{Name: fmt.Sprintf("customerKey_%d", i), Description: strings.Repeat("語\"\\\n", 1024), Secret: true, Pending: true})
	}
	output := scriptDefinitionResult(scriptVariableListForModel(definitions, len(definitions)), nil).Output
	var parsed struct {
		Variables []store.ScriptVariableModelDefinition `json:"variables"`
		Matched   int                                   `json:"matched"`
		Truncated bool                                  `json:"truncated"`
	}
	if len(output) > maxScriptDefinitionResultBytes || !utf8.ValidString(output) || json.Unmarshal([]byte(output), &parsed) != nil {
		t.Fatal("serialized list exceeded budget or broke JSON")
	}
	if !parsed.Truncated || parsed.Matched != 200 || len(parsed.Variables) == 0 || len(parsed.Variables) >= 200 {
		t.Fatal("list omissions are not explicit")
	}
	for _, item := range parsed.Variables {
		if !item.DescriptionTruncated || utf8.RuneCountInString(item.Description) > store.ScriptVariableModelDescriptionRunes {
			t.Fatal("description budget failed")
		}
		if !strings.HasPrefix(item.Name, "customerKey_") {
			t.Fatal("identifier was rewritten")
		}
	}
	small := []store.ScriptVariableModelDefinition{{Name: "customerKey", Description: "Ordinary", Secret: true}}
	raw, _ := json.Marshal(scriptVariableListForModel(small, 1))
	_ = json.Unmarshal(raw, &parsed)
	if parsed.Truncated || len(parsed.Variables) != 1 || parsed.Variables[0].Name != "customerKey" || parsed.Variables[0].Description != "Ordinary" {
		t.Fatal("small list control changed")
	}
	large := store.ScriptVariableModelDefinition{Name: strings.Repeat("A", store.ScriptVariableModelNameBytes+1)}
	raw, _ = json.Marshal(scriptVariableListForModel(append([]store.ScriptVariableModelDefinition{large}, small...), 2))
	_ = json.Unmarshal(raw, &parsed)
	if !parsed.Truncated || parsed.Matched != 2 || len(parsed.Variables) != 1 || parsed.Variables[0].Name != "customerKey" {
		t.Fatal("oversized identifier was truncated rather than omitted")
	}
}

// The count and list queries may see different snapshots. Omission flags must
// still describe omitted rows even when a later insert made matched stale.
func TestScriptVariableListReportsOmissionsWithConcurrentCount(t *testing.T) {
	definitions := []store.ScriptVariableModelDefinition{
		{Name: strings.Repeat("A", store.ScriptVariableModelNameBytes+1)},
		{Name: "ordinaryKey"},
	}
	raw, _ := json.Marshal(scriptVariableListForModel(definitions, 1))
	var result struct {
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.Truncated {
		t.Fatal("stale match count concealed an omitted definition")
	}
}

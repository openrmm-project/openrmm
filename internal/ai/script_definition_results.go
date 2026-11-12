package ai

import (
	"encoding/json"
	"unicode/utf8"

	"github.com/p1ngu/rmm/internal/store"
)

const maxScriptDefinitionResultBytes = 64 << 10

// scriptDefinitionResult bounds all outputs of the three definition tools,
// including a proposed name or validation error reflected back to the model.
// Preserve operation identifiers when metadata is omitted: a successful
// proposal must not look like an invitation to repeat the mutation.
func scriptDefinitionResult(value any, err error) ToolResult {
	var serializable any = value
	if err != nil {
		serializable = map[string]string{"error": err.Error()}
	}
	raw, encodeErr := json.Marshal(serializable)
	if encodeErr != nil {
		return ToolResult{Output: `{"error":"definition output could not be encoded"}`}
	}
	if len(raw) <= maxScriptDefinitionResultBytes {
		return ToolResult{Output: string(raw)}
	}
	bounded := map[string]any{
		"truncated": true,
		"message":   "Definition metadata exceeded the model output budget. Refine the read search or inspect the console. Do not repeat a mutation just because its metadata was omitted.",
	}
	if err != nil {
		bounded["error"] = "definition details exceed model output budget"
	} else if object, ok := value.(map[string]any); ok {
		for _, key := range []string{"id", "kind", "status"} {
			if text, ok := object[key].(string); ok && len(text) <= 128 {
				bounded[key] = text
			}
		}
	}
	return jsonResult(bounded, nil)
}

// scriptVariableListForModel retains exact identifiers and valid JSON. It may
// shorten descriptions, but never an identifier the model would later bind.
func scriptVariableListForModel(definitions []store.ScriptVariableModelDefinition, matched int) any {
	type listing struct {
		Variables []store.ScriptVariableModelDefinition `json:"variables"`
		Matched   int                                   `json:"matched"`
		Truncated bool                                  `json:"truncated"`
	}
	result := listing{Variables: []store.ScriptVariableModelDefinition{}, Matched: matched}
	omitted := false
	for _, definition := range definitions {
		if len(result.Variables) >= store.ScriptVariableModelLimit {
			omitted = true
			break
		}
		if len(definition.Name) > store.ScriptVariableModelNameBytes {
			omitted = true
			continue
		}
		if utf8.RuneCountInString(definition.Description) > store.ScriptVariableModelDescriptionRunes {
			count := 0
			for index := range definition.Description {
				if count == store.ScriptVariableModelDescriptionRunes {
					definition.Description = definition.Description[:index]
					definition.DescriptionTruncated = true
					break
				}
				count++
			}
		}
		result.Variables = append(result.Variables, definition)
		// Budget with false (one byte longer than true), then set the final flag.
		raw, _ := json.Marshal(result)
		if len(raw) > maxScriptDefinitionResultBytes {
			result.Variables = result.Variables[:len(result.Variables)-1]
			omitted = true
		}
	}
	result.Truncated = omitted || len(result.Variables) < matched
	return result
}

package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// listScriptVariablesTool returns definitions only. Values, and which client,
// site or device holds one, never reach the model: they are customer data and
// credentials, and the model has no use for them.
func listScriptVariablesTool(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Search string `json:"search"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
	}
	definitions, matched, err := tc.Store.ScriptVariableDefinitionsForModel(ctx, in.Search)
	if err != nil {
		return nil, err
	}
	return scriptVariableListForModel(definitions, matched), nil
}

// proposeScriptVariable creates a definition awaiting review. Definitions are
// global, so a principal confined to some clients cannot propose one, the same
// rule the REST handler applies to creating one.
func proposeScriptVariable(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Secret      bool   `json:"secret"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if tc.ScopeUserID != "" {
		return nil, fmt.Errorf("scoped users cannot create global script variable definitions")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("name is required")
	}
	out, err := tc.Store.ProposeScriptVariable(ctx, name, strings.TrimSpace(in.Description), in.Secret, tc.proposalActor())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":     out.ID,
		"kind":   "script_variable",
		"name":   out.Name,
		"secret": out.Secret,
		"status": "awaiting_review",
		"message": "PROPOSAL CREATED, WITHOUT A VALUE. No script can bind it and no run can use it until a person " +
			"approves it in Scripts > Script variables and sets its values there. Tell the user it is waiting for " +
			"their review. Never ask them to type the value in this chat.",
	}, nil
}

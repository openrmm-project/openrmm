package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/p1ngu/rmm/internal/autonomy"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/scriptvars"
)

// defaultProposalCategory groups AI-authored scripts in the library, so the
// operator finds what is waiting for review.
const defaultProposalCategory = "AI proposals"

// proposeLibraryScript adds a script to the library awaiting review. It runs
// nothing and creates no job: until a person approves it, no run, schedule or
// AI release can use it (jobs.ErrScriptPending, store.ErrScriptPending). The
// rules are the REST handler's: the library is global, so a scoped principal
// cannot write to it, and binding variables needs the variables permission.
func proposeLibraryScript(ctx context.Context, tc ToolContext, args json.RawMessage) (any, error) {
	var in struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Category    string   `json:"category"`
		Shell       string   `json:"shell"`
		Body        string   `json:"body"`
		Variables   []string `json:"variables"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, err
	}
	if tc.ScopeUserID != "" {
		return nil, fmt.Errorf("scoped users cannot add scripts to the global library")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || strings.TrimSpace(in.Body) == "" {
		return nil, fmt.Errorf("name and body are required")
	}
	if !models.SupportedShell(in.Shell) {
		return nil, fmt.Errorf("unsupported shell %q", in.Shell)
	}
	if err := autonomy.ValidateParamSchema(in.Shell, nil); err != nil {
		return nil, err
	}
	var names []string
	if len(in.Variables) > 0 {
		if !tc.Permissions[models.PermScriptsVariables] {
			return nil, fmt.Errorf("binding script variables needs the scripts.variables permission")
		}
		normalized, err := scriptvars.Normalize(in.Variables)
		if err != nil {
			return nil, err
		}
		if err := scriptvars.CheckShell(in.Shell, len(normalized)); err != nil {
			return nil, err
		}
		missing, err := tc.Store.UnknownScriptVariables(ctx, normalized)
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("unknown script variable, or a proposal awaiting review: %s", strings.Join(missing, ", "))
		}
		names = normalized
	}
	category := strings.TrimSpace(in.Category)
	if category == "" {
		category = defaultProposalCategory
	}
	out, err := tc.Store.CreateScript(ctx, &models.Script{
		Name: name, Description: strings.TrimSpace(in.Description), Category: category,
		Shell: in.Shell, Body: in.Body, Variables: names, ProposedBy: tc.proposalActor(),
	})
	if err != nil {
		return nil, err
	}
	tc.Store.RegisterScriptCategory(ctx, out.Category)
	return map[string]any{
		"id":       out.ID,
		"kind":     "library_script",
		"name":     out.Name,
		"category": out.Category,
		"status":   "awaiting_review",
		"message": "PROPOSAL CREATED IN THE LIBRARY. It runs nothing: it cannot run, be scheduled or be released " +
			"for AI autonomy until a person reads and approves it in Scripts. Tell the user it is waiting for their review.",
	}, nil
}

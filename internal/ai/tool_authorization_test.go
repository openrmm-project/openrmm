package ai

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/p1ngu/rmm/internal/models"
	"strings"
	"testing"
)

// The model can wait between calls; removing a capability must affect the next
// call in that same turn. Malformed arguments keep the permitted control inert.
func TestExecuteReloadsIdentityForEachTool(t *testing.T) {
	ctx := context.Background()
	current := ToolIdentity{Actor: "operator", Permissions: map[string]bool{models.PermJobsCreate: true}}
	calls := 0
	tc := ToolContext{Permissions: map[string]bool{models.PermJobsCreate: true}, Authorize: func(context.Context) (ToolIdentity, error) { calls++; return current, nil }}
	control := Execute(ctx, tc, "propose_script", json.RawMessage(`not json`))
	if strings.Contains(control.Output, "missing permission") || calls != 1 {
		t.Fatalf("permitted control: %s, refreshes=%d", control.Output, calls)
	}
	current.Permissions = map[string]bool{}
	denied := Execute(ctx, tc, "propose_script", json.RawMessage(`not json`))
	if !strings.Contains(denied.Output, "missing permission") || calls != 2 {
		t.Fatalf("stale permission: %s, refreshes=%d", denied.Output, calls)
	}
	tc.Authorize = func(context.Context) (ToolIdentity, error) { return ToolIdentity{}, errors.New("revoked") }
	if got := Execute(ctx, tc, "propose_script", json.RawMessage(`not json`)); !strings.Contains(got.Output, "authorization expired") {
		t.Fatal(got.Output)
	}
	tc.Authorize = func(context.Context) (ToolIdentity, error) { return ToolIdentity{}, nil }
	if got := Execute(ctx, tc, "propose_script", json.RawMessage(`not json`)); !strings.Contains(got.Output, "authorization expired") {
		t.Fatal(got.Output)
	}
}

package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/p1ngu/rmm/internal/models"
)

// The UPDATE, not a stale handler read, must enforce who may approve bindings.
func TestScriptApprovalChecksCurrentBindingsIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newIsolatedStore(t, "script_approval_rights", url)
	ctx := context.Background()
	sc, err := st.CreateScript(ctx, &models.Script{Name: "Pending", Shell: "sh", Body: "echo ok", ProposedBy: "mcp:synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	// A review taken before the binding was added would say no variable rights
	// were needed. The final write must use the current declarations instead.
	if len(sc.Variables) != 0 {
		t.Fatal("control script was already bound")
	}
	sc.Variables = []string{"REVIEW_CREDENTIAL"}
	if _, err := st.UpdateScript(ctx, sc, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveScript(ctx, sc.ID, "manage-only", false); !errors.Is(err, ErrScriptApprovalVariables) {
		t.Fatalf("bound approval did not enforce current rights: %v", err)
	}
	pending, err := st.GetScript(ctx, sc.ID)
	if err != nil || !pending.Pending || pending.ApprovedBy != "" {
		t.Fatal("denied approval changed state")
	}
	if _, err := st.ApproveScript(ctx, sc.ID, "variable-manager", true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveScript(ctx, sc.ID, "manage-only", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("already approved control should remain a conflict: %v", err)
	}
	plain, err := st.CreateScript(ctx, &models.Script{Name: "Plain", Shell: "sh", Body: "echo ok", ProposedBy: "mcp:synthetic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApproveScript(ctx, plain.ID, "manage-only", false); err != nil {
		t.Fatal("unbound control refused", err)
	}
}

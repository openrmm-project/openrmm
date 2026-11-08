package api

import (
	"context"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/models"
	"testing"
)

// The same closure is reused during a chat turn; it must resolve authority
// afresh after an administrator changes the principal.
func TestChatAuthorizerReloadsRoleScopeAndDisabledStateIntegration(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	server := &Server{store: f.store, cfg: &config.Config{}}
	authorize := server.authorizeChat(f.token)
	first, err := authorize(ctx)
	if err != nil || !first.Permissions[models.PermAIAutonomyRun] || first.ScopeUserID != "" {
		t.Fatalf("admin control=%+v err=%v", first, err)
	}
	if _, err := f.store.UpdateUserRole(ctx, first.Actor, models.RoleTech); err != nil {
		t.Fatal(err)
	}
	current, err := authorize(ctx)
	if err != nil || current.Permissions[models.PermAIAutonomyRun] || !current.Permissions[models.PermAIChatUse] {
		t.Fatalf("role change=%+v err=%v", current, err)
	}
	client, err := f.store.CreateClient(ctx, "chat scope")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReplaceUserScopes(ctx, first.Actor, []models.UserScope{{ScopeType: "client", ScopeID: client.ID}}); err != nil {
		t.Fatal(err)
	}
	current, err = authorize(ctx)
	if err != nil || current.ScopeUserID != first.Actor {
		t.Fatalf("scope change=%+v err=%v", current, err)
	}
	if _, err := f.store.SetUserDisabled(ctx, first.Actor, true); err != nil {
		t.Fatal(err)
	}
	if _, err := authorize(ctx); err == nil {
		t.Fatal("disabled account authorized")
	}
}

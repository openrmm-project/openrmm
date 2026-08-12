package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

func scopeIntegrityStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func scopesOf(t *testing.T, st *Store, ctx context.Context, userID string) []models.UserScope {
	t.Helper()
	scopes, err := st.ListUserScopes(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	return scopes
}

// A scope row naming a deleted device is not inert: the console loads it, sends
// it back, and the endpoint rejects the whole request — so the account can no
// longer be edited at all. The row has to go when its target does.
func TestDeletingAScopeTargetRemovesTheScopeRowIntegration(t *testing.T) {
	st, ctx := scopeIntegrityStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, "host-"+suffix, "linux", "amd64", "1.0.0", "token-"+suffix, &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Two scopes, so removing one does not leave the account unscoped — that
	// path has its own test below.
	user, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{
			{ScopeType: models.ScopeAgent, ScopeID: agent.ID},
			{ScopeType: models.ScopeClient, ScopeID: client.ID},
		})
	if err != nil {
		t.Fatal(err)
	}
	if got := scopesOf(t, st, ctx, user.ID); len(got) != 2 {
		t.Fatalf("setup: scopes = %#v, want 2", got)
	}

	if err := st.DeleteAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	got := scopesOf(t, st, ctx, user.ID)
	if len(got) != 1 || got[0].ScopeType != models.ScopeClient {
		t.Fatalf("after deleting the agent, scopes = %#v, want only the client scope", got)
	}
	if u, err := st.GetUserByID(ctx, user.ID); err != nil || u.DisabledAt != nil {
		t.Errorf("the account still had a scope and must stay enabled (disabled_at=%v, err=%v)", u.DisabledAt, err)
	}
}

// Deleting a company cascades to its sites and their departments, and a cascade
// never reaches application code — which is why this is enforced by a trigger.
func TestDeletingAClientPrunesScopesOfItsCascadeIntegration(t *testing.T) {
	st, ctx := scopeIntegrityStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateGroup(ctx, "dept-"+suffix, &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	survivor, err := st.CreateClient(ctx, "other-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{
			{ScopeType: models.ScopeSite, ScopeID: site.ID},
			{ScopeType: models.ScopeGroup, ScopeID: group.ID},
			{ScopeType: models.ScopeClient, ScopeID: survivor.ID},
		})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteClient(ctx, client.ID); err != nil {
		t.Fatal(err)
	}
	got := scopesOf(t, st, ctx, user.ID)
	if len(got) != 1 || got[0].ScopeID != survivor.ID {
		t.Fatalf("scopes after the cascade = %#v, want only the untouched client", got)
	}
}

// An empty scope set means unrestricted. Deleting the last thing a restricted
// technician could see must not hand them the fleet; the account is disabled
// instead, which is reversible and puts a person in front of the decision.
func TestLosingTheLastScopeDisablesTheAccountIntegration(t *testing.T) {
	st, ctx := scopeIntegrityStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteClient(ctx, client.ID); err != nil {
		t.Fatal(err)
	}
	if got := scopesOf(t, st, ctx, user.ID); len(got) != 0 {
		t.Fatalf("scopes = %#v, want none", got)
	}
	u, err := st.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.DisabledAt == nil {
		t.Fatal("an account left with no scopes was silently promoted to full fleet access")
	}
}

// The counterpart: an operator deliberately granting full access also empties
// the table for that user, and must not be disabled for it. This is why the rule
// lives in the pruning trigger rather than on user_scopes.
func TestDeliberatelyClearingScopesDoesNotDisableIntegration(t *testing.T) {
	st, ctx := scopeIntegrityStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUserWithScopes(ctx, "scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	if err != nil {
		t.Fatal(err)
	}

	if err := st.ReplaceUserScopes(ctx, user.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := scopesOf(t, st, ctx, user.ID); len(got) != 0 {
		t.Fatalf("scopes = %#v, want none", got)
	}
	u, err := st.GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if u.DisabledAt != nil {
		t.Fatal("granting full access on purpose disabled the account")
	}

	// And the ordinary replace-one-set-with-another case, which also deletes
	// every row before inserting.
	other, err := st.CreateClient(ctx, "other-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceUserScopes(ctx, user.ID, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: other.ID}}); err != nil {
		t.Fatal(err)
	}
	if u, err := st.GetUserByID(ctx, user.ID); err != nil || u.DisabledAt != nil {
		t.Errorf("replacing a scope set disabled the account (disabled_at=%v, err=%v)", u.DisabledAt, err)
	}
}

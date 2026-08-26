package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestAuthorizationAndConcurrentBootstrapIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `TRUNCATE users, clients, policies, patch_policies CASCADE`); err != nil {
		t.Fatal(err)
	}

	var created atomic.Int32
	var conflicts atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.BootstrapAdmin(ctx, fmt.Sprintf("admin-%d", i), "hash", fmt.Sprintf("token-%d", i), time.Now().Add(time.Hour))
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, ErrConflict):
				conflicts.Add(1)
			default:
				t.Errorf("bootstrap %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if created.Load() != 1 || conflicts.Load() != 7 {
		t.Fatalf("bootstrap created=%d conflicts=%d", created.Load(), conflicts.Load())
	}

	clientA, err := st.CreateClient(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := st.CreateClient(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	siteA, err := st.CreateSite(ctx, clientA.ID, "A1")
	if err != nil {
		t.Fatal(err)
	}
	siteB, err := st.CreateSite(ctx, clientB.ID, "B1")
	if err != nil {
		t.Fatal(err)
	}
	agentA, err := st.CreateAgent(ctx, "a", "linux", "amd64", "1", "a-token", &siteA.ID)
	if err != nil {
		t.Fatal(err)
	}
	agentB, err := st.CreateAgent(ctx, "b", "linux", "amd64", "1", "b-token", &siteB.ID)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUserWithScopes(ctx, "tenant-a", "hash", models.RoleTech, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: siteA.ID}})
	if err != nil {
		t.Fatal(err)
	}

	assertAllowed := func(name string, got bool, err error, want bool) {
		t.Helper()
		if err != nil || got != want {
			t.Fatalf("%s: got (%t, %v), want %t", name, got, err, want)
		}
	}
	ok, err := st.AgentAllowedForUser(ctx, user.ID, agentA.ID)
	assertAllowed("own agent", ok, err, true)
	ok, err = st.AgentAllowedForUser(ctx, user.ID, agentB.ID)
	assertAllowed("foreign agent", ok, err, false)
	ok, err = st.ScopeAllowedForUser(ctx, user.ID, models.ScopeSite, siteA.ID)
	assertAllowed("own site", ok, err, true)
	ok, err = st.ScopeAllowedForUser(ctx, user.ID, models.ScopeClient, clientA.ID)
	assertAllowed("parent client from child grant", ok, err, false)
	ok, err = st.ScopeAllowedForUser(ctx, user.ID, models.ScopeSite, siteB.ID)
	assertAllowed("foreign site", ok, err, false)

	policy, err := st.CreatePolicy(ctx, "policy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AssignPolicy(ctx, policy.ID, models.ScopeSite, siteA.ID); err != nil {
		t.Fatal(err)
	}
	ok, err = st.PolicyManageAllowedForUser(ctx, user.ID, policy.ID)
	assertAllowed("own policy", ok, err, true)
	if _, err := st.AssignPolicy(ctx, policy.ID, models.ScopeSite, siteB.ID); err != nil {
		t.Fatal(err)
	}
	ok, err = st.PolicyManageAllowedForUser(ctx, user.ID, policy.ID)
	assertAllowed("cross-tenant policy", ok, err, false)

	patchPolicy, err := st.CreatePatchPolicy(ctx, &models.PatchPolicy{Name: "patch policy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddPatchPolicyAssignment(ctx, patchPolicy.ID, models.ScopeSite, siteA.ID); err != nil {
		t.Fatal(err)
	}
	ok, err = st.PatchPolicyAllowedForUser(ctx, user.ID, patchPolicy.ID)
	assertAllowed("visible patch policy", ok, err, true)
	ok, err = st.PatchPolicyManageAllowedForUser(ctx, user.ID, patchPolicy.ID)
	assertAllowed("own patch policy", ok, err, true)
	if err := st.AddPatchPolicyAssignment(ctx, patchPolicy.ID, models.ScopeSite, siteB.ID); err != nil {
		t.Fatal(err)
	}
	ok, err = st.PatchPolicyManageAllowedForUser(ctx, user.ID, patchPolicy.ID)
	assertAllowed("cross-tenant patch policy", ok, err, false)
	ok, err = st.ScopeIntersectsUser(ctx, user.ID, models.ScopeSite, siteA.ID)
	assertAllowed("visible assignment", ok, err, true)
	ok, err = st.ScopeIntersectsUser(ctx, user.ID, models.ScopeSite, siteB.ID)
	assertAllowed("foreign assignment", ok, err, false)
	ok, err = st.PatchAllowedForUser(ctx, user.ID, agentA.ID)
	assertAllowed("unknown patch", ok, err, false)

	groupA, err := st.CreateGroup(ctx, "A group", &siteA.ID)
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := st.CreateGroup(ctx, "B group", &siteB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentGroup(ctx, agentA.ID, &groupA.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentSite(ctx, agentA.ID, &siteB.ID); err != nil {
		t.Fatal(err)
	}
	moved, err := st.GetAgent(ctx, agentA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.GroupID != nil {
		t.Fatalf("site move retained stale group %s", *moved.GroupID)
	}
	if err := st.SetAgentGroup(ctx, agentA.ID, &groupA.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-site group assignment error = %v, want conflict", err)
	}
	if err := st.SetAgentGroup(ctx, agentA.ID, &groupB.ID); err != nil {
		t.Fatalf("same-site group assignment: %v", err)
	}

	unrestricted, err := st.CreateUser(ctx, "unrestricted", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	restricted, err := st.UserHasScopes(ctx, unrestricted.ID)
	if err != nil || restricted {
		t.Fatalf("new user restricted=(%t, %v), want false", restricted, err)
	}

	if err := st.SetPendingUserTOTP(ctx, unrestricted.ID, "secret-1"); err != nil {
		t.Fatal(err)
	}
	active, enabled, err := st.GetUserTOTP(ctx, unrestricted.ID)
	if err != nil || active != "" || enabled {
		t.Fatalf("pending setup changed active MFA: secret=%q enabled=%t err=%v", active, enabled, err)
	}
	if err := st.EnablePendingUserTOTP(ctx, unrestricted.ID, "secret-1", []string{"recovery"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPendingUserTOTP(ctx, unrestricted.ID, "secret-2"); err != nil {
		t.Fatal(err)
	}
	active, enabled, err = st.GetUserTOTP(ctx, unrestricted.ID)
	if err != nil || active != "secret-1" || !enabled {
		t.Fatalf("MFA rotation weakened active factor: secret=%q enabled=%t err=%v", active, enabled, err)
	}

	var consumed atomic.Int32
	wg = sync.WaitGroup{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			used, err := st.UseRecoveryCode(context.Background(), unrestricted.ID, "recovery")
			if err != nil {
				t.Errorf("consume recovery code: %v", err)
				return
			}
			if used {
				consumed.Add(1)
			}
		}()
	}
	wg.Wait()
	if consumed.Load() != 1 {
		t.Fatalf("recovery code consumed %d times, want 1", consumed.Load())
	}
}

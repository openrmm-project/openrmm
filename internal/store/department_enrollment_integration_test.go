package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func departmentStore(t *testing.T) (*Store, context.Context) {
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

// The token carries where the agent lands. Binding one to a department is what
// makes a machine enrol straight into it instead of arriving at the site and
// being moved by hand, one at a time.
func TestEnrollTokenPlacesAgentInDepartmentIntegration(t *testing.T) {
	st, ctx := departmentStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	dept, err := st.CreateGroup(ctx, "finance-"+suffix, &site.ID)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := st.CreateEnrollToken(ctx, "tok-"+suffix, "finance", 1, nil, &site.ID, &dept.ID)
	if err != nil {
		t.Fatal(err)
	}
	siteID, groupID, err := st.ConsumeEnrollToken(ctx, tok.Token)
	if err != nil {
		t.Fatal(err)
	}
	if siteID == nil || *siteID != site.ID || groupID == nil || *groupID != dept.ID {
		t.Fatalf("token resolved to site=%v group=%v, want %s / %s", siteID, groupID, site.ID, dept.ID)
	}

	agent, err := st.CreateAgent(ctx, "host-"+suffix, "linux", "amd64", "1.0.0", "hash-"+suffix, siteID, groupID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.GroupID == nil || *agent.GroupID != dept.ID {
		t.Fatalf("agent landed in group %v, want %s", agent.GroupID, dept.ID)
	}
	if agent.SiteID == nil || *agent.SiteID != site.ID {
		t.Fatalf("agent landed in site %v, want %s", agent.SiteID, site.ID)
	}
}

// A department belongs to exactly one site, and an agent's department must be
// one of its own site's. A token naming an inconsistent pair would create the
// state that rule exists to prevent, so it is refused where it is written.
func TestEnrollTokenRefusesDepartmentFromAnotherSiteIntegration(t *testing.T) {
	st, ctx := departmentStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	siteA, err := st.CreateSite(ctx, client.ID, "site-a-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	siteB, err := st.CreateSite(ctx, client.ID, "site-b-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	deptOfB, err := st.CreateGroup(ctx, "dept-b-"+suffix, &siteB.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateEnrollToken(ctx, "tok-x-"+suffix, "mismatch", 1, nil, &siteA.ID, &deptOfB.ID); err == nil {
		t.Fatal("a token naming a department of another site was accepted")
	} else if !strings.Contains(err.Error(), "does not belong to site") {
		t.Fatalf("unexpected error: %v", err)
	}

	// And a department with no site at all is equally unplaceable.
	if _, err := st.CreateEnrollToken(ctx, "tok-y-"+suffix, "no site", 1, nil, nil, &deptOfB.ID); err == nil {
		t.Fatal("a token naming a department without a site was accepted")
	}
}

// Even if a token somehow named a mismatched pair, enrolment must not file the
// agent into another site's department — the second line of defence.
func TestCreateAgentIgnoresDepartmentOutsideItsSiteIntegration(t *testing.T) {
	st, ctx := departmentStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	siteA, err := st.CreateSite(ctx, client.ID, "site-a-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	siteB, err := st.CreateSite(ctx, client.ID, "site-b-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	deptOfB, err := st.CreateGroup(ctx, "dept-b-"+suffix, &siteB.ID)
	if err != nil {
		t.Fatal(err)
	}

	agent, err := st.CreateAgent(ctx, "host-"+suffix, "linux", "amd64", "1.0.0", "hash-"+suffix, &siteA.ID, &deptOfB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if agent.GroupID != nil {
		t.Fatalf("agent filed into %v, a department of another site", *agent.GroupID)
	}
	if agent.SiteID == nil || *agent.SiteID != siteA.ID {
		t.Fatalf("agent site = %v, want %s", agent.SiteID, siteA.ID)
	}
}

// Renaming used to mean deleting and rebuilding, which unfiles every device
// under the department. Only the label moves.
func TestRenameGroupKeepsItsDevicesIntegration(t *testing.T) {
	st, ctx := departmentStore(t)
	suffix := uuid.NewString()

	client, err := st.CreateClient(ctx, "acme-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	dept, err := st.CreateGroup(ctx, "finance-"+suffix, &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, "host-"+suffix, "linux", "amd64", "1.0.0", "hash-"+suffix, &site.ID, &dept.ID)
	if err != nil {
		t.Fatal(err)
	}

	renamed, err := st.RenameGroup(ctx, dept.ID, "Financeiro")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "Financeiro" || renamed.ID != dept.ID {
		t.Fatalf("renamed = %+v, want the same department under a new name", renamed)
	}
	if renamed.SiteID == nil || *renamed.SiteID != site.ID {
		t.Fatalf("renaming moved the department to site %v", renamed.SiteID)
	}
	after, err := st.GetAgent(ctx, agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.GroupID == nil || *after.GroupID != dept.ID {
		t.Fatalf("the device was unfiled by the rename: group=%v", after.GroupID)
	}

	if _, err := st.RenameGroup(ctx, uuid.NewString(), "ghost"); err == nil {
		t.Error("renaming a department that does not exist reported success")
	}
}

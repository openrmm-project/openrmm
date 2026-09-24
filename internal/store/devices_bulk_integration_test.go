package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

type orgFixture struct {
	st        *Store
	ctx       context.Context
	client    *models.Client
	site      *models.Site
	otherSite *models.Site
	dept      *models.DeviceGroup
	otherDept *models.DeviceGroup
}

func newOrgFixture(t *testing.T) orgFixture {
	t.Helper()
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
	other, err := st.CreateSite(ctx, client.ID, "other-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	dept, err := st.CreateGroup(ctx, "finance-"+suffix, &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherDept, err := st.CreateGroup(ctx, "sales-"+suffix, &other.ID)
	if err != nil {
		t.Fatal(err)
	}
	return orgFixture{st: st, ctx: ctx, client: client, site: site, otherSite: other, dept: dept, otherDept: otherDept}
}

func (f orgFixture) agent(t *testing.T, siteID, groupID *string) string {
	t.Helper()
	a, err := f.st.CreateAgent(f.ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), siteID, groupID)
	if err != nil {
		t.Fatal(err)
	}
	return a.ID
}

func (f orgFixture) siteExists(t *testing.T, id string) bool {
	t.Helper()
	ok, err := f.st.ScopeExists(f.ctx, models.ScopeSite, id)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// A site with devices is refused, with the count, and left untouched.
func TestDeleteSiteRefusesWhileItHasDevicesIntegration(t *testing.T) {
	f := newOrgFixture(t)
	f.agent(t, &f.site.ID, nil)
	f.agent(t, &f.site.ID, &f.dept.ID)

	err := f.st.DeleteSite(f.ctx, f.site.ID)
	var notEmpty *SiteNotEmptyError
	if !errors.As(err, &notEmpty) || notEmpty.Devices != 2 {
		t.Fatalf("DeleteSite = %v, want SiteNotEmptyError{2}", err)
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatal("a refusal should read as a conflict")
	}
	if !f.siteExists(t, f.site.ID) {
		t.Fatal("the refused site was deleted anyway")
	}
}

// The defect behind "Internal error" when deleting a site: a token bound to one
// of its departments made the whole delete fail in a trigger.
func TestDeleteSiteWithDepartmentTokenIntegration(t *testing.T) {
	f := newOrgFixture(t)
	expires := time.Now().Add(time.Hour)
	if _, err := f.st.CreateEnrollToken(f.ctx, "tok-"+uuid.NewString(), "dept", 1, &expires, &f.site.ID, &f.dept.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateEnrollToken(f.ctx, "tok-"+uuid.NewString(), "site", 1, &expires, &f.site.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteSite(f.ctx, f.site.ID); err != nil {
		t.Fatalf("DeleteSite with enrollment tokens: %v", err)
	}
	if f.siteExists(t, f.site.ID) {
		t.Fatal("the site is still there")
	}
	var left int
	if err := f.st.pool.QueryRow(f.ctx, `SELECT count(*) FROM enroll_tokens WHERE description IN ('dept','site') AND site_id IS NULL AND group_id IS NULL AND created_at > now() - interval '1 minute'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d tokens were left pointing nowhere; they would enrol devices into no site", left)
	}
}

func TestDeleteSiteNotFoundIntegration(t *testing.T) {
	f := newOrgFixture(t)
	if err := f.st.DeleteSite(f.ctx, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteSite(unknown) = %v, want ErrNotFound", err)
	}
}

// Deleting a company cascades to its sites and hit the same trigger.
func TestDeleteClientWithDepartmentTokenIntegration(t *testing.T) {
	f := newOrgFixture(t)
	expires := time.Now().Add(time.Hour)
	if _, err := f.st.CreateEnrollToken(f.ctx, "tok-"+uuid.NewString(), "dept", 1, &expires, &f.site.ID, &f.dept.ID); err != nil {
		t.Fatal(err)
	}
	device := f.agent(t, &f.site.ID, nil)
	if err := f.st.DeleteClient(f.ctx, f.client.ID); err != nil {
		t.Fatalf("DeleteClient with a department token: %v", err)
	}
	// The console promises the devices become unassigned, not deleted.
	a, err := f.st.GetAgent(f.ctx, device)
	if err != nil {
		t.Fatalf("the company's device was deleted with it: %v", err)
	}
	if a.SiteID != nil {
		t.Fatal("the device still points at a deleted site")
	}
}

func TestMoveAgentsIntegration(t *testing.T) {
	f := newOrgFixture(t)
	a := f.agent(t, &f.site.ID, &f.dept.ID)
	b := f.agent(t, &f.otherSite.ID, &f.otherDept.ID)
	c := f.agent(t, nil, nil)

	t.Run("into a department", func(t *testing.T) {
		if err := f.st.MoveAgents(f.ctx, []string{a, b, c}, f.site.ID, &f.dept.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{a, b, c} {
			got, _ := f.st.GetAgent(f.ctx, id)
			if got.SiteID == nil || *got.SiteID != f.site.ID || got.GroupID == nil || *got.GroupID != f.dept.ID {
				t.Fatalf("%s: site %v group %v", id, got.SiteID, got.GroupID)
			}
		}
	})

	t.Run("a site alone keeps a department that belongs to it", func(t *testing.T) {
		if err := f.st.MoveAgents(f.ctx, []string{a}, f.site.ID, nil); err != nil {
			t.Fatal(err)
		}
		got, _ := f.st.GetAgent(f.ctx, a)
		if got.GroupID == nil || *got.GroupID != f.dept.ID {
			t.Fatalf("department dropped moving within the same site: %v", got.GroupID)
		}
	})

	t.Run("and drops one that does not", func(t *testing.T) {
		if err := f.st.MoveAgents(f.ctx, []string{a}, f.otherSite.ID, nil); err != nil {
			t.Fatal(err)
		}
		got, _ := f.st.GetAgent(f.ctx, a)
		if got.GroupID != nil {
			t.Fatalf("kept department %s of the previous site", *got.GroupID)
		}
	})

	t.Run("a department of another site is refused", func(t *testing.T) {
		err := f.st.MoveAgents(f.ctx, []string{b}, f.site.ID, &f.otherDept.ID)
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("got %v, want ErrConflict", err)
		}
	})

	t.Run("one unknown device moves nothing", func(t *testing.T) {
		before, _ := f.st.GetAgent(f.ctx, c)
		err := f.st.MoveAgents(f.ctx, []string{c, uuid.NewString()}, f.otherSite.ID, nil)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
		after, _ := f.st.GetAgent(f.ctx, c)
		if *after.SiteID != *before.SiteID {
			t.Fatal("a partially valid move was partially applied")
		}
	})
}

func TestDeviceOperationLifecycleIntegration(t *testing.T) {
	f := newOrgFixture(t)
	a := f.agent(t, &f.site.ID, nil)

	op, err := f.st.CreateDeviceOperation(f.ctx, &models.DeviceOperation{
		Kind: models.DeviceOpMove, AgentIDs: []string{a}, SiteID: &f.otherSite.ID, ProposedBy: "ai:tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "awaiting_approval" || op.DecidedBy != "" || len(op.AgentIDs) != 1 || op.AgentIDs[0] != a {
		t.Fatalf("created %+v", op)
	}
	// Proposing changes nothing.
	if got, _ := f.st.GetAgent(f.ctx, a); *got.SiteID != f.site.ID {
		t.Fatal("creating a proposal moved the device")
	}

	claimed, err := f.st.ClaimDeviceOperation(f.ctx, op.ID, "alice")
	if err != nil || claimed.Status != "approved" || claimed.DecidedBy != "alice" {
		t.Fatalf("claim: %+v, %v", claimed, err)
	}
	if _, err := f.st.ClaimDeviceOperation(f.ctx, op.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim = %v, want ErrConflict: it must not run twice", err)
	}
	if err := f.st.RejectDeviceOperation(f.ctx, op.ID, "bob"); !errors.Is(err, ErrConflict) {
		t.Fatalf("reject after approval = %v, want ErrConflict", err)
	}
	if err := f.st.FinishDeviceOperation(f.ctx, op.ID, "completed", map[string]int{"moved": 1}); err != nil {
		t.Fatal(err)
	}
	done, _ := f.st.GetDeviceOperation(f.ctx, op.ID)
	if done.Status != "completed" || string(done.Result) != `{"moved": 1}` {
		t.Fatalf("finished as %s %s", done.Status, done.Result)
	}
}

// The schema itself refuses malformed proposals, whatever code path writes them.
func TestDeviceOperationSchemaGuardsIntegration(t *testing.T) {
	f := newOrgFixture(t)
	a := f.agent(t, &f.site.ID, nil)
	cases := map[string]*models.DeviceOperation{
		"a move without a destination": {Kind: models.DeviceOpMove, AgentIDs: []string{a}, ProposedBy: "ai:x"},
		"a delete with a destination":  {Kind: models.DeviceOpDelete, AgentIDs: []string{a}, SiteID: &f.site.ID, ProposedBy: "ai:x"},
		"no devices":                   {Kind: models.DeviceOpDelete, AgentIDs: []string{}, ProposedBy: "ai:x"},
		"no author":                    {Kind: models.DeviceOpDelete, AgentIDs: []string{a}},
		"an unknown kind":              {Kind: "wipe", AgentIDs: []string{a}, ProposedBy: "ai:x"},
	}
	for name, op := range cases {
		if _, err := f.st.CreateDeviceOperation(f.ctx, op); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A proposal whose destination disappears has nothing left to approve, and
// must not block deleting that destination.
func TestDeviceOperationGoesWithItsDestinationIntegration(t *testing.T) {
	f := newOrgFixture(t)
	a := f.agent(t, &f.site.ID, nil)
	op, err := f.st.CreateDeviceOperation(f.ctx, &models.DeviceOperation{
		Kind: models.DeviceOpMove, AgentIDs: []string{a}, SiteID: &f.otherSite.ID, GroupID: &f.otherDept.ID, ProposedBy: "ai:x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteSite(f.ctx, f.otherSite.ID); err != nil {
		t.Fatalf("a pending proposal blocked deleting its destination: %v", err)
	}
	if _, err := f.st.GetDeviceOperation(f.ctx, op.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("proposal survived its destination: %v", err)
	}
}

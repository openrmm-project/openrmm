package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// The dashboard's numbers are counted by the database over everything the
// caller may see — not over the latest page of jobs the console downloaded.
func TestDashboardCountsIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "store_dashboard", url)

	site := func(name string) string {
		t.Helper()
		c, err := st.CreateClient(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		s, err := st.CreateSite(ctx, c.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	siteA, siteB := site("A"), site("B")
	agent := func(host, siteID, status string) *models.Agent {
		t.Helper()
		a, err := st.CreateAgent(ctx, host, "windows", "amd64", "1", "tok-"+host, &siteID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetAgentStatus(ctx, a.ID, status); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a1 := agent("a1", siteA, models.AgentOnline)
	agent("a2", siteA, "offline")
	b1 := agent("b1", siteB, models.AgentOnline)

	job := func(agentID, status, description string) string {
		t.Helper()
		j, err := st.CreateJob(ctx, &models.Job{AgentID: agentID, Type: models.JobRunScript, Shell: "powershell",
			Script: "secret script", Description: description, Status: status, CreatedBy: "tester"})
		if err != nil {
			t.Fatal(err)
		}
		return j.ID
	}
	// A job waiting for a month, buried under 120 newer ones: a count over the
	// latest 100 jobs never saw it.
	old := job(a1.ID, models.JobAwaitingApproval, "old")
	if _, err := st.pool.Exec(ctx, `UPDATE jobs SET created_at=now()-interval '30 days' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO jobs (id, agent_id, type, description, shell, script, status, created_by)
		 SELECT gen_random_uuid(), $1, 'run_script', 'done', 'sh', '', 'completed', 'tester' FROM generate_series(1,120)`, a1.ID); err != nil {
		t.Fatal(err)
	}
	job(a1.ID, models.JobAwaitingApproval, "new")
	job(b1.ID, models.JobAwaitingApproval, "other site")
	job(a1.ID, models.JobFailed, "failed now")
	stale := job(a1.ID, models.JobFailed, "failed long ago")
	if _, err := st.pool.Exec(ctx, `UPDATE jobs SET updated_at=now()-interval '2 days' WHERE id=$1`, stale); err != nil {
		t.Fatal(err)
	}

	scoped, err := st.CreateUserWithScopes(ctx, "scoped", "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: siteA}})
	if err != nil {
		t.Fatal(err)
	}
	unscoped, err := st.CreateUser(ctx, "unscoped", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-24 * time.Hour)

	for _, tc := range []struct {
		name, user     string
		devices        DeviceCounts
		awaiting, fail int
	}{
		{"unscoped principal", "", DeviceCounts{Total: 3, Online: 2}, 3, 1},
		{"user without scopes", unscoped.ID, DeviceCounts{Total: 3, Online: 2}, 3, 1},
		{"user scoped to site A", scoped.ID, DeviceCounts{Total: 2, Online: 1}, 2, 1},
	} {
		d, err := st.DashboardDeviceCounts(ctx, tc.user)
		if err != nil {
			t.Fatal(err)
		}
		if d != tc.devices {
			t.Errorf("%s: devices %+v, want %+v", tc.name, d, tc.devices)
		}
		j, err := st.DashboardJobCounts(ctx, tc.user, since)
		if err != nil {
			t.Fatal(err)
		}
		if j.Awaiting != tc.awaiting || j.Failed != tc.fail {
			t.Errorf("%s: jobs %+v, want awaiting %d, failed %d", tc.name, j, tc.awaiting, tc.fail)
		}
	}

	items, err := st.AwaitingJobSummaries(ctx, scoped.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Description != "new" || items[1].ID != old || items[0].Hostname != "a1" || items[0].OS != "windows" {
		t.Errorf("scoped awaiting list: %+v", items)
	}
	if items, err := st.AwaitingJobSummaries(ctx, "", 1); err != nil || len(items) != 1 {
		t.Errorf("limit: %d items, %v", len(items), err)
	}

	// The shared predicate still drives the scoped device list.
	agents, err := st.ListAgentsForUser(ctx, scoped.ID)
	if err != nil || len(agents) != 2 {
		t.Errorf("ListAgentsForUser(scoped) = %d agents, %v", len(agents), err)
	}
}

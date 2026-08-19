package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// "Run now" asks how many agents it is about to touch before it touches them,
// and the console words the confirmation differently when the answer is zero.
// Both answers come from resolving the policy's assignments.
func TestTaskTargetsResolveThroughThePolicyIntegration(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()

	client, err := f.store.CreateClient(ctx, "acme-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	site, err := f.store.CreateSite(ctx, client.ID, "site-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.store.CreateAgent(ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := f.store.CreatePolicy(ctx, "baseline-"+uuid.NewString(), "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.store.CreateTask(ctx, &models.Task{
		PolicyID: &policy.ID, Name: "nightly", Shell: "bash", ScheduleKind: "interval",
		IntervalSec: 3600, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	targets := func() int {
		t.Helper()
		rec := f.do(http.MethodGet, "/api/tasks/"+task.ID+"/targets", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("targets status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var out struct {
			Count int `json:"count"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Count
	}

	// Unassigned: the task exists and has nowhere to run. The console warns
	// "no target agents in scope right now — nothing will run".
	if got := targets(); got != 0 {
		t.Errorf("targets before assignment = %d, want 0", got)
	}

	if _, err := f.store.AssignPolicy(ctx, policy.ID, models.ScopeSite, site.ID); err != nil {
		t.Fatal(err)
	}
	if got := targets(); got != 1 {
		t.Errorf("targets after assigning the site = %d, want 1 (agent %s)", got, agent.ID)
	}

	// Removing the agent from the site removes it from the policy's reach
	// without touching the assignment.
	if err := f.store.DeleteAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if got := targets(); got != 0 {
		t.Errorf("targets after the agent went away = %d, want 0", got)
	}
}

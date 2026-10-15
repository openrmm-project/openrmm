package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/models"
)

func strp(s string) *string { return &s }

func TestDeviceChecksPairStateAndSource(t *testing.T) {
	value := 91.5
	at := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	checks := []models.Check{
		{ID: "c-policy-b", PolicyID: strp("p2"), Name: "disk"},
		{ID: "c-direct", AgentID: strp("a1"), Name: "spooler"},
		{ID: "c-policy-a", PolicyID: strp("p1"), Name: "memory"},
		{ID: "c-never", PolicyID: strp("p1"), Name: "cpu"},
	}
	states := map[string]*models.CheckState{
		"c-policy-b": {Status: models.CheckFailing, LastValue: &value, LastOutput: "C: 91.5% used", LastResultAt: &at},
		"c-direct":   {Status: ""},
	}
	got := deviceChecks(checks, states, map[string]string{"p1": "Baseline", "p2": "Servers"})

	order := []string{}
	for _, c := range got {
		order = append(order, c.ID)
	}
	want := []string{"c-direct", "c-never", "c-policy-a", "c-policy-b"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v (direct first, then by policy, then by name)", order, want)
		}
	}
	if got[0].PolicyName != "" || got[0].Status != models.CheckUnknown {
		t.Errorf("direct check without a recorded status = %+v, want no policy and unknown", got[0])
	}
	if got[1].Status != models.CheckUnknown || got[1].LastResultAt != nil {
		t.Errorf("a check never run here must read unknown, got %+v", got[1])
	}
	failing := got[3]
	if failing.PolicyName != "Servers" || failing.Status != models.CheckFailing || failing.Value == nil ||
		*failing.Value != value || failing.Output != "C: 91.5% used" || !failing.LastResultAt.Equal(at) {
		t.Errorf("policy check with state = %+v", failing)
	}
}

func TestDeviceTasksLastRun(t *testing.T) {
	at := time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC)
	tasks := []models.Task{
		{ID: "t-policy", PolicyID: strp("p1"), Name: "cleanup"},
		{ID: "t-direct", AgentID: strp("a1"), Name: "backup"},
	}
	got := deviceTasks(tasks, map[string]time.Time{"t-policy": at, "t-direct": {}}, map[string]string{"p1": "Baseline"})
	if got[0].ID != "t-direct" || got[0].LastRunAt != nil {
		t.Errorf("direct task first, never run: %+v", got[0])
	}
	if got[1].PolicyName != "Baseline" || got[1].LastRunAt == nil || !got[1].LastRunAt.Equal(at) {
		t.Errorf("policy task with a run: %+v", got[1])
	}
	if got := deviceTasks(nil, nil, nil); got == nil || len(got) != 0 {
		t.Errorf("no tasks must encode as an empty list, got %#v", got)
	}
}

// The device drawer lists what the scheduler runs on the device: assigned to
// it, or inherited from a policy on its site — not disabled rows, and not
// another device's.
func TestDeviceChecksAndTasksIncludeInheritedIntegration(t *testing.T) {
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
	other, err := f.store.CreateAgent(ctx, "other-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := f.store.CreatePolicy(ctx, "baseline-"+uuid.NewString(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.AssignPolicy(ctx, policy.ID, models.ScopeSite, site.ID); err != nil {
		t.Fatal(err)
	}
	inherited, err := f.store.CreateCheck(ctx, &models.Check{PolicyID: &policy.ID, Type: "memory", Name: "memory", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := f.store.CreateCheck(ctx, &models.Check{AgentID: &agent.ID, Type: "cpuload", Name: "cpu", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []*models.Check{
		{AgentID: &agent.ID, Type: "diskspace", Name: "disabled", Enabled: false},
		{AgentID: &other.ID, Type: "diskspace", Name: "someone else's", Enabled: true},
	} {
		if _, err := f.store.CreateCheck(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	exec, _, err := f.store.CreateCheckExecution(ctx, direct.ID, agent.ID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.RecordCheckResult(ctx, &models.CheckResult{
		CheckID: direct.ID, AgentID: agent.ID, ExecutionID: &exec.ID, Status: models.CheckPassing, Output: "load 0.4",
	}); err != nil {
		t.Fatal(err)
	}
	task, err := f.store.CreateTask(ctx, &models.Task{
		PolicyID: &policy.ID, Name: "nightly", Shell: "bash", Script: "true", ScheduleKind: "interval", IntervalSec: 3600, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateTask(ctx, &models.Task{
		AgentID: &agent.ID, Name: "off", Shell: "bash", Script: "true", ScheduleKind: "interval", IntervalSec: 3600,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateJob(ctx, &models.Job{
		AgentID: agent.ID, Type: models.JobRunScript, Description: "task: nightly", Shell: "bash", Script: "true",
		Status: models.JobCompleted, CreatedBy: "scheduler", TaskID: &task.ID,
	}); err != nil {
		t.Fatal(err)
	}

	rec := f.do(http.MethodGet, "/api/devices/"+agent.ID+"/checks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("checks status = %d body=%s", rec.Code, rec.Body.String())
	}
	var checks struct {
		Checks []deviceCheck `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &checks); err != nil {
		t.Fatal(err)
	}
	if len(checks.Checks) != 2 || checks.Checks[0].ID != direct.ID || checks.Checks[1].ID != inherited.ID {
		t.Fatalf("checks = %+v, want the direct one then the inherited one, nothing else", checks.Checks)
	}
	if checks.Checks[0].Status != models.CheckPassing || checks.Checks[0].Output != "load 0.4" {
		t.Errorf("direct check state = %+v", checks.Checks[0])
	}
	if checks.Checks[1].PolicyName != policy.Name || checks.Checks[1].Status != models.CheckUnknown {
		t.Errorf("inherited check = %+v, want its policy's name and unknown", checks.Checks[1])
	}

	rec = f.do(http.MethodGet, "/api/devices/"+agent.ID+"/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tasks status = %d body=%s", rec.Code, rec.Body.String())
	}
	var tasks struct {
		Tasks []deviceTask `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatal(err)
	}
	if len(tasks.Tasks) != 1 || tasks.Tasks[0].ID != task.ID || tasks.Tasks[0].PolicyName != policy.Name || tasks.Tasks[0].LastRunAt == nil {
		t.Fatalf("tasks = %+v, want the inherited task with its last run", tasks.Tasks)
	}

	// A user scoped to another client may not read this device's.
	elsewhere, err := f.store.CreateClient(ctx, "elsewhere-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.store.CreateUser(ctx, "scoped-"+uuid.NewString(), "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReplaceUserScopes(ctx, user.ID, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: elsewhere.ID}}); err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := f.store.CreateSession(ctx, user.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/checks", "/tasks"} {
		req := httptest.NewRequest(http.MethodGet, "/api/devices/"+agent.ID+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		f.handler.ServeHTTP(out, req)
		if out.Code != http.StatusForbidden {
			t.Errorf("%s for an out-of-scope device = %d, want 403", path, out.Code)
		}
	}
}

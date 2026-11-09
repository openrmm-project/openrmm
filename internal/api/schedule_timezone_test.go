package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

// A task's and a patch policy's times of day are in the zone they carry. One
// created without a zone is UTC, as before zones existed; an edit that sends
// none keeps the zone, so an older console or API client cannot move a
// schedule; and a zone that is not an IANA name is refused.
func TestScheduleTimezonesThroughTheAPIIntegration(t *testing.T) {
	f := newPolicyAPIFixture(t)
	ctx := context.Background()
	agent, err := f.store.CreateAgent(ctx, "TZ-"+uuid.NewString()[:8], "windows", "amd64", "0.10.0", "tok-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	decodeTask := func(body []byte) models.Task {
		t.Helper()
		var task models.Task
		if err := json.Unmarshal(body, &task); err != nil {
			t.Fatal(err)
		}
		return task
	}
	taskJSON := func(tz string) string {
		zone := ""
		if tz != "-" {
			zone = fmt.Sprintf(`,"timezone":%q`, tz)
		}
		return fmt.Sprintf(`{"agent_id":%q,"name":"tz","shell":"bash","script":"true","schedule_kind":"daily","run_at_minute":700,"enabled":true%s}`, agent.ID, zone)
	}

	rec := f.do(http.MethodPost, "/api/tasks", taskJSON("America/New_York"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task: %d %s", rec.Code, rec.Body)
	}
	task := decodeTask(rec.Body.Bytes())
	t.Cleanup(func() { _ = f.store.DeleteTask(context.Background(), task.ID) })
	if task.Timezone != "America/New_York" {
		t.Fatalf("created task's zone = %q", task.Timezone)
	}

	rec = f.do(http.MethodPost, "/api/tasks", taskJSON("-"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create task without a zone: %d %s", rec.Code, rec.Body)
	}
	legacy := decodeTask(rec.Body.Bytes())
	t.Cleanup(func() { _ = f.store.DeleteTask(context.Background(), legacy.ID) })
	if legacy.Timezone != models.DefaultTimezone {
		t.Fatalf("a task created without a zone is %q, want UTC", legacy.Timezone)
	}

	rec = f.do(http.MethodPut, "/api/tasks/"+task.ID, taskJSON("-"))
	if rec.Code != http.StatusOK || decodeTask(rec.Body.Bytes()).Timezone != "America/New_York" {
		t.Fatalf("an edit without a zone moved the task: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(http.MethodPut, "/api/tasks/"+task.ID, taskJSON("Asia/Tokyo"))
	if rec.Code != http.StatusOK || decodeTask(rec.Body.Bytes()).Timezone != "Asia/Tokyo" {
		t.Fatalf("changing the zone: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{"Local", "../../etc/passwd", "America/Nowhere"} {
		if rec := f.do(http.MethodPut, "/api/tasks/"+task.ID, taskJSON(bad)); rec.Code != http.StatusBadRequest {
			t.Fatalf("zone %q was accepted: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if got, err := f.store.GetTask(ctx, task.ID); err != nil || got.Timezone != "Asia/Tokyo" {
		t.Fatalf("a refused edit changed the stored zone: %+v %v", got, err)
	}

	decodePolicy := func(body []byte) models.PatchPolicy {
		t.Helper()
		var p models.PatchPolicy
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	policyJSON := func(name, tz string) string {
		zone := ""
		if tz != "-" {
			zone = fmt.Sprintf(`,"timezone":%q`, tz)
		}
		return fmt.Sprintf(`{"name":%q,"window_start_min":1320,"window_end_min":1439,"reboot":"never"%s}`, name, zone)
	}
	name := "tz-" + uuid.NewString()[:8]
	rec = f.do(http.MethodPost, "/api/patch-policies", policyJSON(name, "Europe/Lisbon"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create patch policy: %d %s", rec.Code, rec.Body)
	}
	policy := decodePolicy(rec.Body.Bytes())
	t.Cleanup(func() { _ = f.store.DeletePatchPolicy(context.Background(), policy.ID) })
	if policy.Timezone != "Europe/Lisbon" {
		t.Fatalf("created policy's zone = %q", policy.Timezone)
	}
	rec = f.do(http.MethodPost, "/api/patch-policies", policyJSON(name+"-utc", "-"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create patch policy without a zone: %d %s", rec.Code, rec.Body)
	}
	utcPolicy := decodePolicy(rec.Body.Bytes())
	t.Cleanup(func() { _ = f.store.DeletePatchPolicy(context.Background(), utcPolicy.ID) })
	if utcPolicy.Timezone != models.DefaultTimezone {
		t.Fatalf("a policy created without a zone is %q, want UTC", utcPolicy.Timezone)
	}
	rec = f.do(http.MethodPut, "/api/patch-policies/"+policy.ID, policyJSON(name, "-"))
	if rec.Code != http.StatusOK || decodePolicy(rec.Body.Bytes()).Timezone != "Europe/Lisbon" {
		t.Fatalf("an edit without a zone moved the window: %d %s", rec.Code, rec.Body)
	}
	if rec := f.do(http.MethodPut, "/api/patch-policies/"+policy.ID, policyJSON(name, "Local")); rec.Code != http.StatusBadRequest {
		t.Fatalf("the server's own zone was accepted for a window: %d %s", rec.Code, rec.Body)
	}
	list, err := f.store.ListPatchPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.ID == policy.ID && p.Timezone != "Europe/Lisbon" {
			t.Fatalf("stored window zone = %q", p.Timezone)
		}
	}
}

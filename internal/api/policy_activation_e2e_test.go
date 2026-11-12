package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// deviceIn enrols a device under a client, so the client's scope is visible to
// a reader confined to it.
func deviceIn(t *testing.T, f *scriptFixture, clientID string) {
	t.Helper()
	ctx := context.Background()
	site, err := f.store.CreateSite(ctx, clientID, "Site "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.CreateAgent(ctx, "PC-"+uuid.NewString()[:8], "linux", "amd64", "0.11.0", "hash-"+uuid.NewString(), &site.ID, nil); err != nil {
		t.Fatal(err)
	}
}

// CI-only PostgreSQL, through the real authenticated router: an AI policy is
// switched on only by an activation that sends back the fingerprint of the
// review, by a caller allowed to approve what it carries; the activation and
// the switch-off are audited; a scoped user can do neither.
func TestPolicyActivationThroughTheAPIE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	client, err := f.store.CreateClient(ctx, "Acme "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	// A policy is visible to a scoped reader through the devices it reaches, so
	// the client needs one.
	deviceIn(t, f, client.ID)
	other, err := f.store.CreateClient(ctx, "Other "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	warn, fail := 20.0, 10.0
	proposal, err := f.store.CreatePolicyProposal(ctx, &store.PolicyProposal{
		Name: "AI workstations", ProposedBy: "mcp:claude",
		Assignments: []models.PolicyAssignment{{ScopeType: models.ScopeClient, ScopeID: client.ID}, {ScopeType: models.ScopeClient, ScopeID: other.ID}},
		Tasks:       []models.Task{{Name: "Clean", Shell: "sh", Script: "true", ScheduleKind: models.ScheduleInterval, IntervalSec: 3600}},
		Checks: []models.Check{{Name: "Memory", Type: models.CheckMemory, Config: json.RawMessage(`{}`), WarnThreshold: &warn, ErrThreshold: &fail,
			IntervalSec: 300, TimeoutSec: 30, FailureCount: 2, RecoveryCount: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := proposal.Policy.ID
	base := "/api/policies/" + id

	rec := f.do(http.MethodGet, "/api/policies", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pending":true`) || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("listed proposal: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.do(http.MethodGet, base+"/review", "")
	var review models.PolicyReview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &review) != nil || review.Fingerprint == "" || review.PendingTasks != 1 || len(review.Assignments) != 2 {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	// Confined to one client, a reader sees that client's scope only — as the
	// assignment listing shows it — and gets no fingerprint to activate with.
	restore := f.asUser(t, "", []string{models.PermPoliciesView}, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	rec = f.do(http.MethodGet, base+"/review", "")
	var scopedReview models.PolicyReview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &scopedReview) != nil || len(scopedReview.Assignments) != 1 ||
		scopedReview.Assignments[0].ScopeID != client.ID || scopedReview.Fingerprint != "" || strings.Contains(rec.Body.String(), other.ID) {
		t.Fatalf("scoped review leaks other scopes: %d %s", rec.Code, rec.Body.String())
	}
	restore()
	if rec := f.do(http.MethodPost, base+"/activate", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("activation without a fingerprint = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, base+"/activate", `{"fingerprint":"stale"}`); rec.Code != http.StatusConflict {
		t.Fatalf("activation of a stale review = %d %s", rec.Code, rec.Body.String())
	}
	body := `{"fingerprint":"` + review.Fingerprint + `"}`

	// policies.manage alone switches policies on, but approving the AI task and
	// check inside this one needs their permissions too.
	// Without tasks.view and checks.view the review leaves the scripts and
	// settings out and seals nothing, and the activation is refused even with
	// a fingerprint someone else read.
	restore = f.asUser(t, "", []string{models.PermPoliciesView, models.PermPoliciesManage}, nil)
	rec = f.do(http.MethodGet, base+"/review", "")
	var blind models.PolicyReview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &blind) != nil || len(blind.Tasks) != 0 || len(blind.Checks) != 0 ||
		blind.Fingerprint != "" || strings.Contains(rec.Body.String(), "Clean") {
		t.Fatalf("review without tasks.view/checks.view: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, base+"/activate", body); rec.Code != http.StatusForbidden {
		t.Fatalf("activation without the view permissions = %d %s", rec.Code, rec.Body.String())
	}
	restore()
	// Able to read everything but not to approve the AI task and check inside.
	restore = f.asUser(t, "", []string{models.PermPoliciesView, models.PermPoliciesManage, models.PermTasksView, models.PermChecksView}, nil)
	if rec := f.do(http.MethodPost, base+"/activate", body); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "tasks.manage") {
		t.Fatalf("activation without tasks/checks manage = %d %s", rec.Code, rec.Body.String())
	}
	restore()
	// A user confined to the client the policy applies to cannot switch on a
	// global definition.
	restore = f.asUser(t, models.RoleAdmin, nil, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	if rec := f.do(http.MethodPost, base+"/activate", body); rec.Code != http.StatusForbidden {
		t.Fatalf("scoped activation = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, base+"/deactivate", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("scoped switch-off = %d", rec.Code)
	}
	restore()

	rec = f.do(http.MethodPost, base+"/activate", body)
	var out store.PolicyActivation
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || !out.Policy.Enabled || !out.ApprovedPolicy || out.ApprovedTasks != 1 || out.ApprovedChecks != 1 {
		t.Fatalf("activation: %d %s", rec.Code, rec.Body.String())
	}
	entry := f.audit("policy.activate")
	var detail struct {
		Name           string `json:"name"`
		ApprovedPolicy bool   `json:"approved_policy"`
		ApprovedTasks  int    `json:"approved_tasks"`
		ApprovedChecks int    `json:"approved_checks"`
	}
	if entry == nil || entry.Target != id || json.Unmarshal(entry.Detail, &detail) != nil || detail.Name != "AI workstations" ||
		!detail.ApprovedPolicy || detail.ApprovedTasks != 1 || detail.ApprovedChecks != 1 {
		t.Fatalf("activation audit: %+v", entry)
	}
	tasks, err := f.store.ListTasks(ctx, "", id)
	if err != nil || len(tasks) != 1 || !tasks[0].Enabled || tasks[0].ApprovedBy == "" {
		t.Fatalf("approved task: %+v %v", tasks, err)
	}
	if rec := f.do(http.MethodPost, base+"/deactivate", `{}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Fatalf("switch-off: %d %s", rec.Code, rec.Body.String())
	}
	if f.audit("policy.deactivate") == nil {
		t.Fatal("switch-off not audited")
	}
	if rec := f.do(http.MethodGet, "/api/policies/"+uuid.NewString()+"/review", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing policy review = %d", rec.Code)
	}
}

// Approving an AI task or check by switching it on is audited as an approval.
func TestTaskAndCheckProposalApprovalIsAuditedE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	agent, err := f.store.CreateAgent(ctx, "AUDIT-HOST", "linux", "amd64", "0.11.0", "hash-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.store.CreateTask(ctx, &models.Task{AgentID: &agent.ID, Name: "Proposed task", Shell: "sh", Script: "true",
		ScheduleKind: models.ScheduleInterval, IntervalSec: 3600, ProposedBy: "mcp:claude"})
	if err != nil {
		t.Fatal(err)
	}
	enable, _ := json.Marshal(map[string]any{"name": task.Name, "shell": task.Shell, "script": task.Script, "schedule_kind": task.ScheduleKind,
		"interval_sec": task.IntervalSec, "enabled": true})
	if rec := f.do(http.MethodPut, "/api/tasks/"+task.ID, string(enable)); rec.Code != http.StatusOK {
		t.Fatalf("approve task: %d %s", rec.Code, rec.Body.String())
	}
	if e := f.audit("task.approve"); e == nil || e.Target != task.ID || !strings.Contains(string(e.Detail), "Proposed task") {
		t.Fatalf("task approval audit: %+v", e)
	}
	warn, fail := 20.0, 10.0
	check, err := f.store.CreateCheck(ctx, &models.Check{AgentID: &agent.ID, Name: "Proposed check", Type: models.CheckMemory,
		Config: json.RawMessage(`{}`), WarnThreshold: &warn, ErrThreshold: &fail, IntervalSec: 300, TimeoutSec: 30,
		FailureCount: 2, RecoveryCount: 2, ProposedBy: "mcp:claude"})
	if err != nil {
		t.Fatal(err)
	}
	enableCheck, _ := json.Marshal(map[string]any{"name": check.Name, "type": check.Type, "config": map[string]any{},
		"warn_threshold": warn, "err_threshold": fail, "interval_sec": 300, "enabled": true})
	if rec := f.do(http.MethodPut, "/api/checks/"+check.ID, string(enableCheck)); rec.Code != http.StatusOK {
		t.Fatalf("approve check: %d %s", rec.Code, rec.Body.String())
	}
	if e := f.audit("check.approve"); e == nil || e.Target != check.ID {
		t.Fatalf("check approval audit: %+v", e)
	}
}

// An AI patch policy cannot be switched on by an edit, only by its reviewed
// activation; the REST API validates a policy and never lets a client claim
// that one was proposed by an assistant.
func TestPatchPolicyActivationThroughTheAPIE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	client, err := f.store.CreateClient(ctx, "Patch "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	deviceIn(t, f, client.ID)
	other, err := f.store.CreateClient(ctx, "Other "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	review, err := f.store.CreatePatchPolicyProposal(ctx, &models.PatchPolicy{Name: "AI weekend", AutoApprove: []string{models.PatchClassSecurity},
		AutoApproveAgeDays: 3, WindowEndMin: 1439, Timezone: "UTC", Reboot: models.RebootAlways, ProposedBy: "mcp:claude"},
		[]models.PatchPolicyAssignment{{ScopeType: models.ScopeClient, ScopeID: client.ID}, {ScopeType: models.ScopeClient, ScopeID: other.ID}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/patch-policies/" + review.Policy.ID
	edit, _ := json.Marshal(map[string]any{"name": "AI weekend", "enabled": true, "auto_approve": []string{"security"},
		"auto_approve_age_days": 3, "window_end_min": 1439, "reboot": "always"})
	if rec := f.do(http.MethodPut, base, string(edit)); rec.Code != http.StatusConflict {
		t.Fatalf("edit switching on a proposal = %d %s", rec.Code, rec.Body.String())
	}
	// An edit is validated like a creation, and changes nothing when refused.
	invalid, _ := json.Marshal(map[string]any{"name": "AI weekend", "enabled": false, "window_days": []int{9}, "reboot": "always"})
	if rec := f.do(http.MethodPut, base, string(invalid)); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid edit = %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, base+"/activate", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("activation without a fingerprint = %d", rec.Code)
	}
	rec := f.do(http.MethodGet, base+"/review", "")
	var fresh models.PatchPolicyReview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &fresh) != nil || fresh.Fingerprint == "" || !fresh.Policy.Pending {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, base+"/activate", `{"fingerprint":"old"}`); rec.Code != http.StatusConflict {
		t.Fatalf("stale activation = %d", rec.Code)
	}
	restore := f.asUser(t, models.RoleAdmin, nil, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	if rec := f.do(http.MethodPost, base+"/activate", `{"fingerprint":"`+fresh.Fingerprint+`"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("scoped activation = %d", rec.Code)
	}
	// The scoped reader sees its own client's scope only, and no fingerprint.
	rec = f.do(http.MethodGet, base+"/review", "")
	var scopedReview models.PatchPolicyReview
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &scopedReview) != nil || len(scopedReview.Assignments) != 1 ||
		scopedReview.Fingerprint != "" || strings.Contains(rec.Body.String(), other.ID) {
		t.Fatalf("scoped patch review leaks other scopes: %d %s", rec.Code, rec.Body.String())
	}
	restore()
	rec = f.do(http.MethodPost, base+"/activate", `{"fingerprint":"`+fresh.Fingerprint+`"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"enabled":true`) || !strings.Contains(rec.Body.String(), `"pending":false`) {
		t.Fatalf("activation: %d %s", rec.Code, rec.Body.String())
	}
	var patchDetail struct {
		Name     string `json:"name"`
		Approved bool   `json:"approved_proposal"`
	}
	if e := f.audit("patch.policy.activate"); e == nil || json.Unmarshal(e.Detail, &patchDetail) != nil || !patchDetail.Approved || patchDetail.Name != "AI weekend" {
		t.Fatalf("activation audit: %+v", e)
	}

	// A client cannot claim AI provenance, and invalid settings are refused.
	rec = f.do(http.MethodPost, "/api/patch-policies", `{"name":"Mine","enabled":true,"proposed_by":"mcp:claude","approved_by":"x"}`)
	var mine models.PatchPolicy
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &mine) != nil || mine.ProposedBy != "" || !mine.Enabled {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []string{`{"name":"B","reboot":"sometimes"}`, `{"name":"B","window_days":[9]}`, `{"name":"B","auto_approve":["everything"]}`} {
		if rec := f.do(http.MethodPost, "/api/patch-policies", bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s accepted: %d", bad, rec.Code)
		}
	}
}

// A client cannot claim AI provenance when it creates a check or a task: the
// server records who proposed and who approved, never the request body.
func TestCheckAndTaskProvenanceIsTheServersE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	agent, err := f.store.CreateAgent(ctx, "PROVENANCE-HOST", "linux", "amd64", "0.11.0", "hash-"+uuid.NewString(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkBody, _ := json.Marshal(map[string]any{"agent_id": agent.ID, "type": "memory", "name": "Mine", "warn_threshold": 20, "err_threshold": 10,
		"interval_sec": 300, "enabled": false, "proposed_by": "mcp:forged", "approved_by": "forged"})
	rec := f.do(http.MethodPost, "/api/checks", string(checkBody))
	var check models.Check
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &check) != nil || check.ProposedBy != "" || check.ApprovedBy != "" {
		t.Fatalf("check provenance: %d %s", rec.Code, rec.Body.String())
	}
	taskBody, _ := json.Marshal(map[string]any{"agent_id": agent.ID, "name": "Mine", "shell": "sh", "script": "true",
		"schedule_kind": "interval", "interval_sec": 3600, "enabled": false, "proposed_by": "mcp:forged", "approved_by": "forged"})
	rec = f.do(http.MethodPost, "/api/tasks", string(taskBody))
	var task models.Task
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &task) != nil || task.ProposedBy != "" || task.ApprovedBy != "" {
		t.Fatalf("task provenance: %d %s", rec.Code, rec.Body.String())
	}
}

// Policy targeting through the router: assignments take OS and role filters
// and a global scope only an unrestricted principal may use; exceptions are
// added, listed and removed; a device's role can be corrected; every change
// is audited.
func TestPolicyTargetingThroughTheAPIE2E(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	client, err := f.store.CreateClient(ctx, "Targets "+uuid.NewString()[:8])
	if err != nil {
		t.Fatal(err)
	}
	site, err := f.store.CreateSite(ctx, client.ID, "HQ")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.store.CreateAgent(ctx, "MAC-"+uuid.NewString()[:6], "darwin", "arm64", "0.11.0", "hash-"+uuid.NewString(), &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	pol, err := f.store.CreatePolicy(ctx, "Servers "+uuid.NewString()[:6], "", "")
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/policies/" + pol.ID

	rec := f.do(http.MethodPost, base+"/assignments", `{"scope_type":"client","scope_id":"`+client.ID+`","os":"windows","role":"server"}`)
	var a models.PolicyAssignment
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &a) != nil || a.OS != "windows" || a.Role != "server" {
		t.Fatalf("filtered assignment: %d %s", rec.Code, rec.Body.String())
	}
	if e := f.audit("policy.assign"); e == nil || !strings.Contains(string(e.Detail), "windows") {
		t.Fatalf("assignment audit: %+v", e)
	}
	for _, bad := range []string{`{"scope_type":"client","scope_id":"` + client.ID + `","os":"darwin"}`, `{"scope_type":"client","scope_id":"` + client.ID + `","role":"kiosk"}`, `{"scope_type":"tenant","scope_id":"x"}`} {
		if rec := f.do(http.MethodPost, base+"/assignments", bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d", bad, rec.Code)
		}
	}
	rec = f.do(http.MethodPost, base+"/assignments", `{"scope_type":"global","scope_id":"ignored","role":"server"}`)
	var global models.PolicyAssignment
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &global) != nil || global.ScopeID != models.GlobalScopeID {
		t.Fatalf("global assignment: %d %s", rec.Code, rec.Body.String())
	}
	// A principal confined to the client can neither add nor remove a global one.
	restore := f.asUser(t, models.RoleAdmin, nil, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	if rec := f.do(http.MethodPost, base+"/assignments", `{"scope_type":"global"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("scoped global assignment = %d", rec.Code)
	}
	if rec := f.do(http.MethodDelete, "/api/policies/assignments/"+global.ID, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("scoped removal of a global assignment = %d", rec.Code)
	}
	restore()
	if rec := f.do(http.MethodDelete, "/api/policies/assignments/"+global.ID, ""); rec.Code != http.StatusOK || f.audit("policy.unassign") == nil {
		t.Fatalf("remove global: %d", rec.Code)
	}

	// Exceptions.
	if rec := f.do(http.MethodPost, base+"/exclusions", `{"agent_id":"`+agent.ID+`"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), agent.ID) {
		t.Fatalf("exclude: %d %s", rec.Code, rec.Body.String())
	}
	if e := f.audit("policy.exclude"); e == nil || e.Target != pol.ID || !strings.Contains(string(e.Detail), agent.Hostname) {
		t.Fatalf("exclusion audit: %+v", e)
	}
	if rec := f.do(http.MethodGet, base+"/exclusions", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), agent.Hostname) {
		t.Fatalf("list exclusions: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPost, base+"/exclusions", `{"agent_id":"`+uuid.NewString()+`"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("excluding a missing device = %d", rec.Code)
	}
	if rec := f.do(http.MethodPost, base+"/exclusions", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("excluding nothing = %d", rec.Code)
	}
	review := f.do(http.MethodGet, base+"/review", "")
	if !strings.Contains(review.Body.String(), `"exclusions":[{"agent_id":"`+agent.ID) {
		t.Fatalf("the review does not list the exception: %s", review.Body.String())
	}
	if rec := f.do(http.MethodDelete, base+"/exclusions/"+agent.ID, ""); rec.Code != http.StatusOK || f.audit("policy.include") == nil {
		t.Fatalf("include: %d", rec.Code)
	}
	if rec := f.do(http.MethodDelete, base+"/exclusions/"+agent.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("including a device that is not excluded = %d", rec.Code)
	}

	// A device's role: a Mac can be marked as a server, and put back.
	rec = f.do(http.MethodPut, "/api/devices/"+agent.ID+"/role", `{"role":"server"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"server"`) || f.audit("device.role") == nil {
		t.Fatalf("role override: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodPut, "/api/devices/"+agent.ID+"/role", `{"role":"kiosk"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown role = %d", rec.Code)
	}
	if rec := f.do(http.MethodPut, "/api/devices/"+agent.ID+"/role", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no role = %d", rec.Code)
	}
	if rec := f.do(http.MethodPut, "/api/devices/"+agent.ID+"/role", `{"role":""}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role_override":""`) {
		t.Fatalf("clear override: %d %s", rec.Code, rec.Body.String())
	}
	// The role decides which fleet-wide policies reach a device, so a principal
	// confined to the device's client cannot change it.
	restore = f.asUser(t, models.RoleAdmin, nil, []models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}})
	if rec := f.do(http.MethodPut, "/api/devices/"+agent.ID+"/role", `{"role":"workstation"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("scoped role change = %d", rec.Code)
	}
	restore()

	// Patch policies take the same filters and exceptions.
	pp, err := f.store.CreatePatchPolicy(ctx, &models.PatchPolicy{Name: "Patch " + uuid.NewString()[:6], Enabled: true, WindowEndMin: 1439})
	if err != nil {
		t.Fatal(err)
	}
	pbase := "/api/patch-policies/" + pp.ID
	if rec := f.do(http.MethodPost, pbase+"/assignments", `{"scope_type":"site","scope_id":"`+site.ID+`","role":"workstation"}`); rec.Code != http.StatusCreated || f.audit("patch.policy.assign") == nil {
		t.Fatalf("patch assignment: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodGet, pbase+"/assignments", ""); !strings.Contains(rec.Body.String(), `"role":"workstation"`) {
		t.Fatalf("patch assignments: %s", rec.Body.String())
	}
	if rec := f.do(http.MethodPost, pbase+"/exclusions", `{"agent_id":"`+agent.ID+`"}`); rec.Code != http.StatusOK || f.audit("patch.policy.exclude") == nil {
		t.Fatalf("patch exclusion: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(http.MethodDelete, pbase+"/exclusions/"+agent.ID, ""); rec.Code != http.StatusOK || f.audit("patch.policy.include") == nil {
		t.Fatalf("patch include: %d", rec.Code)
	}
}

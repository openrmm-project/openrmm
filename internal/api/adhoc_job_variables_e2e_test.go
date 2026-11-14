package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/ai"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/remote"
	"github.com/p1ngu/rmm/internal/store"
)

// CI-only PostgreSQL, through the real router: an ad hoc job (the device page's
// Run script) may ask for script variables only from a caller who could bind
// them to a saved script — scripts.manage and scripts.variables on an
// unrestricted principal. jobs.create alone, either permission alone, or a
// principal confined to a client is refused and creates nothing. The allowed
// job waits for approval unless the instance lets jobs skip it and the caller
// may (jobs.manage and settings.manage) and asks to, as for any job; it carries
// the names and never a value, and is audited with the names. Unknown names and
// CMD are refused; a job without names is unchanged.
func TestAdHocJobVariablesTakeWhatBindingTakesE2E(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	st := newTestStore(t, url)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	client, err := st.CreateClient(ctx, "Acme "+uuid.NewString()[:8])
	must(err)
	site, err := st.CreateSite(ctx, client.ID, "HQ")
	must(err)
	agent, err := st.CreateAgent(ctx, "PC-"+uuid.NewString()[:8], "linux", "amd64", "0.11.0", "tok-"+uuid.NewString(), &site.ID, nil)
	must(err)

	h := hub.New()
	cfg := &config.Config{APIRateLimit: 100000, AllowUnapprovedJobs: true, SettingsEncryptionKey: "synthetic-adhoc-variables-key"}
	chat := ai.NewEngine(ai.NewClient("", "", ""), st, nil, nil, cfg)
	srv := New(cfg, st, h, jobs.New(st, h).WithSettings(cfg), remote.NewManager(st, h, "", "", "", false, false), remote.NewRelay(), chat, nil, nil)
	handler := srv.Router()

	session := func(role string, perms []string, scopes []models.UserScope) string {
		t.Helper()
		if perms != nil {
			role = "r_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
			_, err := st.CreateRole(ctx, &models.Role{Key: role, Name: "Custom " + role, Permissions: perms})
			must(err)
		}
		u, err := st.CreateUserWithScopes(ctx, "u-"+uuid.NewString(), "hash", role, scopes)
		must(err)
		token := auth.NewToken()
		must(st.CreateSession(ctx, u.ID, auth.Hash(token), time.Now().Add(time.Hour)))
		return "Bearer " + token
	}
	call := func(authz, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, strings.NewReader(string(b)))
		req.RemoteAddr = "198.51.100." + uuid.NewString()[:2] + ":1234"
		req.Header.Set("Authorization", authz)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	admin := session(models.RoleAdmin, nil, nil)
	jobsFor := func() []models.Job {
		t.Helper()
		list, err := st.ListJobs(ctx, agent.ID, 100)
		must(err)
		return list
	}

	// A secret with a global value, as an administrator sets it.
	name := "VENDOR_KEY_" + strings.ToUpper(uuid.NewString()[:8])
	const secret = "synthetic-vendor-secret-value"
	rec := call(admin, http.MethodPost, "/api/script-variables", map[string]any{"name": name, "secret": true})
	var variable struct{ ID string }
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &variable) != nil {
		t.Fatalf("variable: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(admin, http.MethodPut, "/api/script-variables/"+variable.ID+"/values", map[string]string{"scope_type": "global", "scope_id": "", "value": secret}); rec.Code != http.StatusOK {
		t.Fatalf("value: %d %s", rec.Code, rec.Body.String())
	}
	asking := map[string]any{"agent_id": agent.ID, "shell": "sh", "script": `printf '%s' "$` + name + `"`, "description": "edited copy",
		"require_approval": false, "variables": []string{name}}

	base := []string{models.PermDevicesView, models.PermJobsView, models.PermJobsCreate, models.PermJobsManage, models.PermSettingsManage, models.PermScriptsView}
	for label, authz := range map[string]string{
		"jobs.create only":          session("", base, nil),
		"without scripts.variables": session("", append(append([]string{}, base...), models.PermScriptsManage), nil),
		"without scripts.manage":    session("", append(append([]string{}, base...), models.PermScriptsVariables), nil),
		"confined to the client": session("", append(append([]string{}, base...), models.PermScriptsManage, models.PermScriptsVariables),
			[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: client.ID}}),
	} {
		if rec := call(authz, http.MethodPost, "/api/jobs", asking); rec.Code != http.StatusForbidden {
			t.Fatalf("%s = %d %s", label, rec.Code, rec.Body.String())
		}
	}
	if got := jobsFor(); len(got) != 0 {
		t.Fatalf("a refused request created %d job(s)", len(got))
	}
	// The control: the same caller without names still creates a job.
	plain := session("", base, nil)
	if rec := call(plain, http.MethodPost, "/api/jobs", map[string]any{"agent_id": agent.ID, "shell": "sh", "script": "uptime"}); rec.Code != http.StatusCreated {
		t.Fatalf("plain job: %d %s", rec.Code, rec.Body.String())
	}

	created := func(authz string, body map[string]any) models.Job {
		t.Helper()
		rec := call(authz, http.MethodPost, "/api/jobs", body)
		var j models.Job
		if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &j) != nil {
			t.Fatalf("job: %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("the response carries the value")
		}
		if strings.Join(j.Variables, ",") != name || j.ScriptID != nil {
			t.Fatalf("job names: %+v", j)
		}
		return j
	}
	gatedAsk := map[string]any{}
	for k, v := range asking {
		if k != "require_approval" {
			gatedAsk[k] = v
		}
	}
	// Not asking to skip approval: it waits, names only.
	if j := created(admin, gatedAsk); j.Status != models.JobAwaitingApproval || !j.RequiresApproval {
		t.Fatalf("default: %+v", j)
	}
	// A binder who may not skip approval (no settings.manage) still waits.
	binder := session("", append(append([]string{}, base[:len(base)-2]...), models.PermScriptsView, models.PermScriptsManage, models.PermScriptsVariables), nil)
	if j := created(binder, asking); j.Status != models.JobAwaitingApproval || !j.RequiresApproval {
		t.Fatalf("binder without settings.manage skipped approval: %+v", j)
	}
	// The instance allows it and the administrator may: sent without approval
	// (the device is not connected, so it stays pending).
	job := created(admin, asking)
	if job.Status != models.JobPending || job.RequiresApproval {
		t.Fatalf("allowed run without approval: %+v", job)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatal("the response carries the value")
	}
	stored, err := st.GetJob(ctx, job.ID)
	must(err)
	if strings.Join(stored.Variables, ",") != name {
		t.Fatalf("stored names: %v", stored.Variables)
	}
	entries, _, err := st.ListAudit(ctx, store.AuditFilter{Limit: 50})
	must(err)
	var audited *models.AuditEntry
	for i := range entries {
		if entries[i].Action == "job.propose" && entries[i].Target == job.ID {
			audited = &entries[i]
		}
	}
	if audited == nil || !strings.Contains(string(audited.Detail), name) || strings.Contains(string(audited.Detail), secret) {
		t.Fatalf("audit: %+v", audited)
	}

	// Refused for what binding refuses.
	for label, body := range map[string]map[string]any{
		"unknown name": {"agent_id": agent.ID, "shell": "sh", "script": "echo", "variables": []string{"NO_SUCH_" + strings.ToUpper(uuid.NewString()[:6])}},
		"cmd":          {"agent_id": agent.ID, "shell": "cmd", "script": "echo %" + name + "%", "variables": []string{name}},
	} {
		if rec := call(admin, http.MethodPost, "/api/jobs", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d %s", label, rec.Code, rec.Body.String())
		}
	}
	if got := jobsFor(); len(got) != 4 {
		t.Fatalf("jobs after the refusals: %d, want the plain one and the three with variables", len(got))
	}
}

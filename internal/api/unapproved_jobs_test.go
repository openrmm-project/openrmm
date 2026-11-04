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

// unapprovedFixture is a console with one Linux device, a library script, an
// administrator and an approver who cannot manage the configuration.
type unapprovedFixture struct {
	t          *testing.T
	st         *store.Store
	handler    http.Handler
	agentID    string
	scriptID   string
	asAdmin    string
	asApprover string
	ctx        context.Context
}

func newUnapprovedFixture(t *testing.T, allowFromEnv bool) *unapprovedFixture {
	t.Helper()
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
	agent, err := st.CreateAgent(ctx, "host-"+uuid.NewString(), "linux", "amd64", "1.0.0", "tok-"+uuid.NewString(), nil, nil)
	must(err)
	sc, err := st.CreateScript(ctx, &models.Script{Name: "uptime-" + uuid.NewString()[:8], Shell: models.ShellBash, Body: "uptime"})
	must(err)

	admin, err := st.CreateUser(ctx, "admin-"+uuid.NewString(), "hash", models.RoleAdmin)
	must(err)
	approver, err := st.CreateUser(ctx, "approver-"+uuid.NewString(), "hash", models.RoleReadOnly)
	must(err)
	roleKey := "approver_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	_, err = st.CreateRole(ctx, &models.Role{Key: roleKey, Name: "Approver", Permissions: []string{
		models.PermDevicesView, models.PermScriptsView, models.PermJobsView, models.PermJobsCreate, models.PermJobsManage,
	}})
	must(err)
	_, err = st.UpdateUserRole(ctx, approver.ID, roleKey)
	must(err)
	adminToken, approverToken := auth.NewToken(), auth.NewToken()
	must(st.CreateSession(ctx, admin.ID, auth.Hash(adminToken), time.Now().Add(time.Hour)))
	must(st.CreateSession(ctx, approver.ID, auth.Hash(approverToken), time.Now().Add(time.Hour)))

	h := hub.New()
	cfg := &config.Config{APIRateLimit: 100000, AllowUnapprovedJobs: allowFromEnv}
	chat := ai.NewEngine(ai.NewClient("", "", ""), st, nil, nil, cfg)
	// The device is not connected, so a job that skips approval stays pending.
	srv := New(cfg, st, h, jobs.New(st, h), remote.NewManager(st, h, "", "", "", false, false), remote.NewRelay(), chat, nil, nil)
	return &unapprovedFixture{
		t: t, st: st, handler: srv.Router(), agentID: agent.ID, scriptID: sc.ID,
		asAdmin: "Bearer " + adminToken, asApprover: "Bearer " + approverToken, ctx: ctx,
	}
}

func (f *unapprovedFixture) call(method, path, authz string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, strings.NewReader(string(b)))
	req.RemoteAddr = "198.51.100." + uuid.NewString()[:2] + ":1234"
	req.Header.Set("Authorization", authz)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// allowed reads the setting the way the console does.
func (f *unapprovedFixture) allowed() bool {
	f.t.Helper()
	rec := f.call(http.MethodGet, "/api/config", f.asAdmin, nil)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("GET /api/config: %d %s", rec.Code, rec.Body)
	}
	var c struct {
		Jobs struct {
			AllowUnapproved bool `json:"allow_unapproved"`
		} `json:"jobs"`
		Server struct {
			AllowUnapprovedJobs bool `json:"allow_unapproved_jobs"`
		} `json:"server"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		f.t.Fatal(err)
	}
	if c.Jobs.AllowUnapproved != c.Server.AllowUnapprovedJobs {
		f.t.Fatalf("jobs.allow_unapproved=%t but server.allow_unapproved_jobs=%t", c.Jobs.AllowUnapproved, c.Server.AllowUnapprovedJobs)
	}
	return c.Jobs.AllowUnapproved
}

func (f *unapprovedFixture) set(on bool, authz string) int {
	f.t.Helper()
	return f.call(http.MethodPut, "/api/config", authz, map[string]any{"jobs": map[string]any{"allow_unapproved": on}}).Code
}

// gated creates a job asking to skip approval, ad hoc or from the library, and
// reports whether it still waits for approval.
func (f *unapprovedFixture) gated(authz string, library bool) bool {
	f.t.Helper()
	var rec *httptest.ResponseRecorder
	if library {
		rec = f.call(http.MethodPost, "/api/devices/"+f.agentID+"/run-script", authz,
			map[string]any{"script_id": f.scriptID, "require_approval": false})
	} else {
		rec = f.call(http.MethodPost, "/api/jobs", authz,
			map[string]any{"agent_id": f.agentID, "shell": "bash", "script": "uptime", "require_approval": false})
	}
	if rec.Code != http.StatusCreated {
		f.t.Fatalf("create job (library=%t): %d %s", library, rec.Code, rec.Body)
	}
	var j models.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
		f.t.Fatal(err)
	}
	if j.RequiresApproval != (j.Status == models.JobAwaitingApproval) {
		f.t.Fatalf("job %s: requires_approval=%t but status %s", j.ID, j.RequiresApproval, j.Status)
	}
	return j.RequiresApproval
}

func TestUnapprovedJobsSetting(t *testing.T) {
	f := newUnapprovedFixture(t, false)

	// Off by default: asking to skip approval is ignored, for everyone.
	if f.allowed() {
		t.Fatal("a new instance allows unapproved jobs")
	}
	for _, library := range []bool{false, true} {
		if !f.gated(f.asAdmin, library) {
			t.Fatalf("an administrator skipped approval with the setting off (library=%t)", library)
		}
	}

	// Only configuration managers can turn it on.
	if code := f.set(true, f.asApprover); code != http.StatusForbidden {
		t.Fatalf("an approver without settings.manage changed the setting: %d", code)
	}
	if f.allowed() {
		t.Fatal("a refused change turned the setting on")
	}
	if code := f.set(true, f.asAdmin); code != http.StatusOK {
		t.Fatalf("an administrator could not turn it on: %d", code)
	}
	if !f.allowed() {
		t.Fatal("the setting did not turn on")
	}

	// On: an administrator skips approval, on both paths; an approver who does
	// not manage the configuration still waits, as the console shows them.
	for _, library := range []bool{false, true} {
		if f.gated(f.asAdmin, library) {
			t.Fatalf("an administrator could not skip approval with the setting on (library=%t)", library)
		}
		if !f.gated(f.asApprover, library) {
			t.Fatalf("an approver without settings.manage skipped approval (library=%t)", library)
		}
	}
	// Asking for approval still gets approval.
	rec := f.call(http.MethodPost, "/api/jobs", f.asAdmin, map[string]any{"agent_id": f.agentID, "shell": "bash", "script": "uptime"})
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"status":"awaiting_approval"`) {
		t.Fatalf("a job without require_approval skipped approval: %d %s", rec.Code, rec.Body)
	}

	// Saving another setting keeps it (nil means preserve).
	if code := f.call(http.MethodPut, "/api/config", f.asAdmin, map[string]any{"remote": map[string]any{"rustdesk_enabled": false}}).Code; code != http.StatusOK {
		t.Fatalf("saving another setting: %d", code)
	}
	if !f.allowed() {
		t.Fatal("saving another setting turned it off")
	}

	// Off again: approval is back for everyone.
	if code := f.set(false, f.asAdmin); code != http.StatusOK {
		t.Fatalf("an administrator could not turn it off: %d", code)
	}
	if !f.gated(f.asAdmin, false) {
		t.Fatal("an administrator skipped approval after the setting was turned off")
	}

	// Each change is audited with the new value, by the person who made it.
	entries, _, err := f.st.ListAudit(f.ctx, store.AuditFilter{Action: "config.unapproved_jobs", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 config.unapproved_jobs entries (on, off), got %d", len(entries))
	}
	var got []bool
	for _, e := range entries {
		var d struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(e.Detail, &d); err != nil || d.Enabled == nil {
			t.Fatalf("audit detail %s: %v", e.Detail, err)
		}
		got = append(got, *d.Enabled)
	}
	if !((got[0] == false && got[1] == true) || (got[0] == true && got[1] == false)) {
		t.Fatalf("audited values %v, want one on and one off", got)
	}
}

// RMM_ALLOW_UNAPPROVED_JOBS=true only sets the starting value: an instance
// that had it keeps working, and an administrator can still turn it off.
func TestUnapprovedJobsEnvIsOnlyTheStartingValue(t *testing.T) {
	f := newUnapprovedFixture(t, true)
	if !f.allowed() {
		t.Fatal("RMM_ALLOW_UNAPPROVED_JOBS=true did not start the setting on")
	}
	if f.gated(f.asAdmin, false) {
		t.Fatal("an administrator could not skip approval on an instance started with the variable")
	}
	if code := f.set(false, f.asAdmin); code != http.StatusOK {
		t.Fatalf("turning it off: %d", code)
	}
	if f.allowed() {
		t.Fatal("the environment variable overrode the saved setting")
	}
	if !f.gated(f.asAdmin, false) {
		t.Fatal("an administrator skipped approval after turning the setting off")
	}
}

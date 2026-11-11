package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/auth"
	"github.com/p1ngu/rmm/internal/config"
	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

func TestScriptVariablesUserChosenNamesAndValuesAPIIntegration(t *testing.T) {
	f := newScriptFixture(t)
	for _, tc := range []struct {
		name, value string
		secret      bool
	}{
		{"customerKey", "q7X", true},
		{"_shortKey", "x", true},
		{"customerKey_" + strings.Repeat("Ab", 4096), strings.Repeat("v", 65536), false},
	} {
		payload, _ := json.Marshal(map[string]any{"name": tc.name, "secret": tc.secret})
		rec := f.do("POST", "/api/script-variables", string(payload))
		if rec.Code != http.StatusCreated {
			t.Fatalf("create status %d", rec.Code)
		}
		var created store.ScriptVariable
		if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.Name != tc.name {
			t.Fatal("name spelling was not preserved")
		}
		payload, _ = json.Marshal(map[string]any{"scope_type": "global", "value": tc.value})
		if rec = f.do("PUT", "/api/script-variables/"+created.ID+"/values", string(payload)); rec.Code != http.StatusOK {
			t.Fatalf("set value status %d", rec.Code)
		}
		rec = f.do("GET", "/api/script-variables", "")
		var list struct {
			Variables []store.ScriptVariable `json:"variables"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
			t.Fatal("list failed")
		}
		found := false
		for _, item := range list.Variables {
			if item.ID != created.ID {
				continue
			}
			found = true
			if item.Name != tc.name || len(item.Values) != 1 || !item.Values[0].Set {
				t.Fatal("value or original name missing")
			}
			want := tc.value
			if tc.secret {
				want = ""
			}
			if item.Values[0].Value != want {
				t.Fatal("secret exposed or public value truncated")
			}
		}
		if !found {
			t.Fatal("created variable missing")
		}
	}
	for _, name := range []string{"CUSTOMERKEY", "Path", "rmm_agent_id", "OpenRmm_PARAM_X", "1key", "has space"} {
		payload, _ := json.Marshal(map[string]any{"name": name})
		if rec := f.do("POST", "/api/script-variables", string(payload)); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid or ambiguous name accepted: %s (status %d)", name, rec.Code)
		}
	}
}

func TestScriptVariablesRBACAndWriteOnlyAPIIntegration(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	// Read and write the same routes the browser uses; no external listener.
	rec := f.do("POST", "/api/script-variables", `{"name":"CUSTOM_KEY","secret":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-customer-credential"
	rec = f.do("PUT", "/api/script-variables/"+v.ID+"/values", `{"scope_type":"global","value":"`+secret+`"}`)
	if rec.Code != 200 {
		t.Fatalf("set status %d", rec.Code)
	}
	rec = f.do("GET", "/api/script-variables", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "enc:v1:") {
		t.Fatal("API leaked secret")
	}
	audit := f.audit("script_variable.set")
	if audit == nil || strings.Contains(string(audit.Detail), secret) {
		t.Fatal("secret in/missing audit")
	}
	rec = f.do("PUT", "/api/script-variables/"+v.ID, `{"secret":false}`)
	if rec.Code < 400 {
		t.Fatal("secret downgrade accepted")
	}
	sc := f.script("bound", "Tests")
	sc.Variables = []string{"CUSTOM_KEY"}
	if _, err := f.store.UpdateScript(ctx, sc); err != nil {
		t.Fatal(err)
	}
	role := &models.Role{Key: "script-editor", Name: "Editor", Permissions: []string{models.PermScriptsView, models.PermScriptsManage}}
	if _, err := f.store.CreateRole(ctx, role); err != nil {
		t.Fatal(err)
	}
	user, err := f.store.CreateUser(ctx, "editor-"+uuid.NewString(), "hash", role.Key)
	if err != nil {
		t.Fatal(err)
	}
	token := auth.NewToken()
	if err := f.store.CreateSession(ctx, user.ID, auth.Hash(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.token = token
	for _, route := range []struct{ method, path, body string }{{"POST", "/api/script-variables", `{"name":"NEW_KEY"}`}, {"PUT", "/api/script-variables/" + v.ID + "/values", `{"scope_type":"global","value":"replacement-value"}`}, {"POST", "/api/scripts", `{"name":"exfil","shell":"sh","body":"env","variables":["CUSTOM_KEY"]}`}, {"PUT", "/api/scripts/" + sc.ID, `{"name":"exfil","shell":"sh","body":"env","variables":[]}`}} {
		if rec := f.do(route.method, route.path, route.body); rec.Code != 403 {
			t.Fatalf("editor passed gate %s %s: %d", route.method, route.path, rec.Code)
		}
	}
	if rec := f.do("POST", "/api/scripts", `{"name":"ordinary","shell":"sh","body":"echo hi"}`); rec.Code != 201 {
		t.Fatal("unbound editor control failed")
	}
	// Live role changes apply to an existing session.
	role.Permissions = append(role.Permissions, models.PermScriptsVariables)
	if _, err := f.store.UpdateRole(ctx, role); err != nil {
		t.Fatal(err)
	}
	client, err := f.store.CreateClient(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.store.CreateClient(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.ReplaceUserScopes(ctx, user.ID, []models.UserScope{{ScopeType: "client", ScopeID: client.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{client.ID, other.ID} {
		rec := f.do("PUT", "/api/script-variables/"+v.ID+"/values", `{"scope_type":"client","scope_id":"`+id+`","value":"scoped-customer-key"}`)
		want := 403
		if id == client.ID {
			want = 200
		}
		if rec.Code != want {
			t.Fatalf("scope write=%d want=%d", rec.Code, want)
		}
	}
	if rec := f.do("PUT", "/api/script-variables/"+v.ID+"/values", `{"scope_type":"global","value":"global-replacement-key"}`); rec.Code != 403 {
		t.Fatal("scoped manager modified global")
	}
	// The same scope restriction applies to a user-bound API token.
	apiToken := auth.NewToken()
	if _, err := f.store.CreateAPIToken(ctx, auth.Hash(apiToken), "variable-manager", role.Key, &user.ID, nil); err != nil {
		t.Fatal(err)
	}
	f.token = apiToken
	if rec := f.do("PUT", "/api/script-variables/"+v.ID+"/values", `{"scope_type":"client","scope_id":"`+other.ID+`","value":"other-customer-key"}`); rec.Code != 403 {
		t.Fatal("API token escaped scope")
	}
	if rec := f.do("PUT", "/api/script-variables/"+v.ID+"/values", `{"scope_type":"client","scope_id":"`+client.ID+`","value":"allowed-customer-key"}`); rec.Code != 200 {
		t.Fatal("in-scope token control failed")
	}

	// Store crypto uses the instance's existing format (synthetic fixture key).
	env, _, err := f.store.ResolveScriptVariables(ctx, &config.Config{}, "missing", []string{"CUSTOM_KEY"})
	if err == nil || env != nil {
		t.Fatal("unknown device accepted")
	}
}

type variableE2ESender struct{ frames [][]byte }

func (s *variableE2ESender) Send(_ string, frame []byte) bool {
	s.frames = append(s.frames, frame)
	return true
}
func TestScriptVariablesJobLifecycleE2EIntegration(t *testing.T) {
	f := newScriptFixture(t)
	ctx := context.Background()
	cfg := &config.Config{APIRateLimit: 10000, SettingsEncryptionKey: "synthetic-instance-key"}
	sender := &variableE2ESender{}
	service := jobs.New(f.store, sender).WithSettings(cfg)
	f.handler = (&Server{cfg: cfg, store: f.store, jobs: service}).Router()
	client, err := f.store.CreateClient(ctx, "Customer A")
	if err != nil {
		t.Fatal(err)
	}
	site, err := f.store.CreateSite(ctx, client.ID, "HQ")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := f.store.CreateAgent(ctx, "E2E-PC", "linux", "amd64", "0.11.0", "synthetic-agent-token", &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	variable, err := f.store.CreateScriptVariable(ctx, "CUSTOM_KEY", "", true)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "synthetic-customer-key"
	rec := f.do("PUT", "/api/script-variables/"+variable.ID+"/values", `{"scope_type":"client","scope_id":"`+client.ID+`","value":"`+secret+`"}`)
	if rec.Code != 200 {
		t.Fatal("set value failed")
	}
	rec = f.do("POST", "/api/scripts", `{"name":"Installer","shell":"sh","body":"printf '%s' \"$CUSTOM_KEY\"","variables":["CUSTOM_KEY"]}`)
	if rec.Code != 201 {
		t.Fatalf("create script status=%d", rec.Code)
	}
	var sc models.Script
	if err := json.Unmarshal(rec.Body.Bytes(), &sc); err != nil {
		t.Fatal(err)
	}
	rec = f.do("POST", "/api/devices/"+agent.ID+"/run-script", `{"script_id":"`+sc.ID+`"}`)
	if rec.Code != 201 {
		t.Fatalf("propose status=%d", rec.Code)
	}
	var j models.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
		t.Fatal(err)
	}
	if j.Status != models.JobAwaitingApproval || len(sender.frames) != 0 || strings.Contains(rec.Body.String(), secret) {
		t.Fatal("proposal leaked/dispatched")
	}
	rec = f.do("POST", "/api/jobs/"+j.ID+"/approve", `{}`)
	if rec.Code != 200 || len(sender.frames) != 1 {
		t.Fatalf("approve status=%d frames=%d", rec.Code, len(sender.frames))
	}
	var frame protocol.Envelope
	var msg protocol.JobMsg
	if err := json.Unmarshal(sender.frames[0], &frame); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(frame.Data, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Vars["CUSTOM_KEY"] != secret || msg.Vars["RMM_CLIENT_NAME"] != "Customer A" {
		t.Fatal("runtime context wrong")
	}
	if err := service.OnResult(ctx, agent.ID, protocol.JobResultMsg{JobID: j.ID, Success: true, Stdout: "installer echoed " + secret, Stderr: secret}); err != nil {
		t.Fatal(err)
	}
	stored, err := f.store.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(stored)
	if strings.Contains(string(raw), secret) || !strings.Contains(stored.Stdout, "********") {
		t.Fatal("history contains secret")
	}
	entries, _, err := f.store.ListAudit(ctx, store.AuditFilter{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(string(entry.Detail), secret) {
			t.Fatal("audit contains secret")
		}
	}
}

func TestScriptVariablesCMDRejectedAtDefinitionBoundary(t *testing.T) {
	s := &Server{}
	request := httptest.NewRequest("POST", "/api/scripts", nil)
	if _, err := s.prepareScriptVariables(request, "cmd", []string{"CUSTOM_KEY"}); err == nil {
		t.Fatal("CMD binding reached database validation")
	}
	names, err := s.prepareScriptVariables(request, "cmd", nil)
	if err != nil || names != nil {
		t.Fatal("ordinary CMD definition control refused")
	}
}

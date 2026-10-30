package ai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/settings"
	"github.com/p1ngu/rmm/internal/store"
)

// Risk-based autonomy against a real database (docs/23-ai-autonomy.md): what
// runs without approval, what becomes a proposal, and what is refused.

// agentStub stands in for a connected agent: it records what was dispatched and
// answers each job a moment later, as the agent would.
type agentStub struct {
	mu    sync.Mutex
	js    *jobs.Service
	sent  []protocol.JobMsg
	reply func(protocol.JobMsg) protocol.JobResultMsg
	down  bool // the connection is gone although the device still reads online
}

func (a *agentStub) Send(agentID string, frame []byte) bool {
	a.mu.Lock()
	down := a.down
	a.mu.Unlock()
	if down {
		return false
	}
	var env protocol.Envelope
	if json.Unmarshal(frame, &env) != nil || env.Type != protocol.MsgJob {
		return true
	}
	var msg protocol.JobMsg
	if json.Unmarshal(env.Data, &msg) != nil {
		return true
	}
	a.mu.Lock()
	a.sent = append(a.sent, msg)
	reply, js := a.reply, a.js
	a.mu.Unlock()
	if reply != nil && js != nil {
		go func() {
			time.Sleep(100 * time.Millisecond) // after the job is marked sent
			res := reply(msg)
			res.JobID = msg.JobID
			_ = js.OnResult(context.Background(), agentID, res)
		}()
	}
	return true
}

func (a *agentStub) dispatched() []protocol.JobMsg {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]protocol.JobMsg(nil), a.sent...)
}

type autonomyFixture struct {
	ctx      context.Context
	st       *store.Store
	js       *jobs.Service
	agentRPC *agentStub
	device   string // online, 0.10.0, inside the scoped user's client
	oldAgent string // online, 0.9.0
	offline  string // offline, 0.10.0
	outside  string // another client's device
	scoped   string // a user restricted to the device's client
	actor    string
}

func newAutonomyFixture(t *testing.T) autonomyFixture {
	t.Helper()
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	st, err := store.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAppSetting(ctx, settings.KeyAIAutonomyPaused, "false", false); err != nil {
		t.Fatal(err)
	}
	suffix := uuid.NewString()
	mine, err := st.CreateClient(ctx, "auto-mine-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := st.CreateClient(ctx, "auto-theirs-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	mineSite, err := st.CreateSite(ctx, mine.ID, "auto-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	theirSite, err := st.CreateSite(ctx, theirs.ID, "auto-their-site-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	agent := func(name, version, status string, site string) string {
		a, err := st.CreateAgent(ctx, name+"-"+suffix, "windows", "amd64", version, "tok-"+name+"-"+suffix, &site, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetAgentStatus(ctx, a.ID, status); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	scoped, err := st.CreateUserWithScopes(ctx, "auto-scoped-"+suffix, "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: mine.ID}})
	if err != nil {
		t.Fatal(err)
	}
	stub := &agentStub{reply: func(msg protocol.JobMsg) protocol.JobResultMsg {
		return protocol.JobResultMsg{ExitCode: 0, Success: true, Stdout: "ok password=hunter2"}
	}}
	js := jobs.New(st, stub)
	stub.js = js
	return autonomyFixture{
		ctx: ctx, st: st, js: js, agentRPC: stub,
		device:   agent("dev", "0.10.0", models.AgentOnline, mineSite.ID),
		oldAgent: agent("old", "0.9.0", models.AgentOnline, mineSite.ID),
		offline:  agent("off", "0.10.0", models.AgentOffline, mineSite.ID),
		outside:  agent("out", "0.10.0", models.AgentOnline, theirSite.ID),
		scoped:   scoped.ID,
		actor:    "auto-" + suffix,
	}
}

func (f autonomyFixture) tc(channel string, perms ...string) ToolContext {
	set := map[string]bool{models.PermDevicesView: true, models.PermJobsCreate: true, models.PermScriptsView: true}
	for _, p := range perms {
		set[p] = true
	}
	actor := f.actor
	if channel == ChannelMCP {
		actor = "mcp:" + f.actor
	}
	return ToolContext{Store: f.st, Jobs: f.js, Actor: actor, ScopeUserID: f.scoped, Permissions: set, Channel: channel}
}

func callTool(t *testing.T, tc ToolContext, name string, args map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	res := Execute(context.Background(), tc, name, raw)
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("%s output is not JSON: %s", name, res.Output)
	}
	return out
}

func (f autonomyFixture) job(t *testing.T, out map[string]any) *models.Job {
	t.Helper()
	id, _ := out["job_id"].(string)
	if id == "" {
		t.Fatalf("no job: %v", out)
	}
	j, err := f.st.GetJob(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestAutonomousQueryRunsAndIsRecorded(t *testing.T) {
	f := newAutonomyFixture(t)
	out := callTool(t, f.tc(ChannelChat, models.PermAIAutonomyQuery), "query_device",
		map[string]any{"agent_id": f.device, "query": "processes", "params": map[string]any{"sort": "memory", "limit": 5}})
	if out["ran_automatically"] != true || out["status"] != models.JobCompleted {
		t.Fatalf("out = %v", out)
	}
	dev, _ := out["device_output"].(map[string]any)
	if stdout, _ := dev["stdout"].(string); strings.Contains(stdout, "hunter2") || !strings.Contains(stdout, "ok") {
		t.Fatalf("output reached the model unredacted: %v", dev)
	}
	j := f.job(t, out)
	if j.Type != models.JobDeviceQuery || j.Autonomy != models.AutonomyQuery || j.RequiresApproval ||
		j.CreatedBy != "ai:"+f.actor || j.ApprovedBy == nil || *j.ApprovedBy != "autonomy: ai.autonomy.query" {
		t.Fatalf("job = %+v", j)
	}
	var req protocol.DeviceQueryRequest
	if json.Unmarshal([]byte(j.Script), &req) != nil || req.Query != "processes" || req.Params["limit"] != "5" {
		t.Fatalf("stored request = %s", j.Script)
	}
	sent := f.agentRPC.dispatched()
	if len(sent) != 1 || sent[0].Type != protocol.JobTypeDeviceQuery || sent[0].Shell != "" {
		t.Fatalf("dispatched = %+v", sent)
	}
}

func TestQueryWithoutThePermissionIsAProposal(t *testing.T) {
	f := newAutonomyFixture(t)
	out := callTool(t, f.tc(ChannelChat), "query_device", map[string]any{"agent_id": f.device, "query": "disk"})
	if out["ran_automatically"] != false || out["status"] != models.JobAwaitingApproval {
		t.Fatalf("out = %v", out)
	}
	if j := f.job(t, out); j.Status != models.JobAwaitingApproval || j.Autonomy != models.AutonomyNone || !j.RequiresApproval {
		t.Fatalf("job = %+v", j)
	}
	if len(f.agentRPC.dispatched()) != 0 {
		t.Fatal("a proposal reached the device")
	}
}

// The built-in chat and external MCP clients have separate switches.
func TestChatAndMCPSwitchesAreIndependent(t *testing.T) {
	f := newAutonomyFixture(t)
	out := callTool(t, f.tc(ChannelMCP, models.PermAIAutonomyQuery), "query_device", map[string]any{"agent_id": f.device, "query": "system"})
	if out["ran_automatically"] != false {
		t.Fatalf("the chat permission let MCP run: %v", out)
	}
	out = callTool(t, f.tc(ChannelMCP, models.PermMCPAutonomyQuery), "query_device", map[string]any{"agent_id": f.device, "query": "system"})
	if out["ran_automatically"] != true {
		t.Fatalf("mcp permission did not run: %v", out)
	}
	if j := f.job(t, out); j.CreatedBy != "mcp:"+f.actor || *j.ApprovedBy != "autonomy: mcp.autonomy.query" {
		t.Fatalf("job = %+v", j)
	}
}

func TestPauseTurnsEveryRoleBackToProposals(t *testing.T) {
	f := newAutonomyFixture(t)
	if err := f.st.UpsertAppSetting(f.ctx, settings.KeyAIAutonomyPaused, "true", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.st.UpsertAppSetting(context.Background(), settings.KeyAIAutonomyPaused, "false", false) })
	out := callTool(t, f.tc(ChannelChat, models.PermAIAutonomyQuery), "query_device", map[string]any{"agent_id": f.device, "query": "disk"})
	if out["ran_automatically"] != false || !strings.Contains(out["message"].(string), "paused") {
		t.Fatalf("out = %v", out)
	}
}

func TestQueriesAreRefusedOutsideScopeOfflineOrOnOldAgents(t *testing.T) {
	f := newAutonomyFixture(t)
	tc := f.tc(ChannelChat, models.PermAIAutonomyQuery)
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"outside the scope": {map[string]any{"agent_id": f.outside, "query": "disk"}, "outside your access scope"},
		"offline":           {map[string]any{"agent_id": f.offline, "query": "disk"}, "offline"},
		"old agent":         {map[string]any{"agent_id": f.oldAgent, "query": "disk"}, "0.10.0"},
		"unknown query":     {map[string]any{"agent_id": f.device, "query": "files"}, "unknown device query"},
		"injected filter":   {map[string]any{"agent_id": f.device, "query": "services", "params": map[string]any{"name": "x; Restart-Computer"}}, "may hold only"},
	} {
		out := callTool(t, tc, "query_device", c.args)
		if errText, _ := out["error"].(string); !strings.Contains(errText, c.want) {
			t.Errorf("%s: out = %v", name, out)
		}
	}
	if len(f.agentRPC.dispatched()) != 0 {
		t.Fatal("a refused query reached a device")
	}
}

// A released script: the release is bound to its content, parameters only take
// declared values, and the device history names who released it.
func (f autonomyFixture) releasedScript(t *testing.T, body string, params []models.ScriptParam) *models.Script {
	t.Helper()
	sc, err := f.st.CreateScript(f.ctx, &models.Script{Name: "auto-" + uuid.NewString(), Shell: models.ShellPowerShell, Body: body, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetScriptAIRelease(f.ctx, sc.ID, "admin-releaser", models.ScriptContentHash(sc.Shell, sc.Body, sc.Params), sc.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	sc, err = f.st.GetScript(f.ctx, sc.ID)
	if err != nil || !sc.AIReleased {
		t.Fatalf("release did not hold: %+v %v", sc, err)
	}
	return sc
}

var spoolerParam = []models.ScriptParam{{Name: "service", Kind: "enum", Values: []string{"Spooler", "W32Time"}}}

func TestPreapprovedActionRunsWithItsDeclaredParameters(t *testing.T) {
	f := newAutonomyFixture(t)
	sc := f.releasedScript(t, "Restart-Service -Name $env:OPENRMM_PARAM_SERVICE", spoolerParam)
	tc := f.tc(ChannelChat, models.PermAIAutonomyRun)
	listed := callTool(t, tc, "list_preapproved_actions", map[string]any{})
	if listed["runs_without_approval"] != true || !strings.Contains(mustJSON(listed), sc.ID) {
		t.Fatalf("listed = %v", listed)
	}
	out := callTool(t, tc, "run_preapproved_action", map[string]any{"agent_id": f.device, "script_id": sc.ID, "params": map[string]any{"service": "Spooler"}})
	if out["ran_automatically"] != true || out["status"] != models.JobCompleted {
		t.Fatalf("out = %v", out)
	}
	j := f.job(t, out)
	if j.Autonomy != models.AutonomyPreapproved || j.ScriptID == nil || *j.ScriptID != sc.ID || j.Params["service"] != "Spooler" ||
		!strings.HasPrefix(*j.ApprovedBy, "pre-approved by admin-releaser (hash "+sc.AIReleasedHash[:12]) {
		t.Fatalf("job = %+v approved_by=%v", j, j.ApprovedBy)
	}
	sent := f.agentRPC.dispatched()
	if len(sent) != 1 || sent[0].Params["service"] != "Spooler" || strings.Contains(sent[0].Script, "Spooler") {
		t.Fatalf("dispatched = %+v", sent)
	}
	for name, params := range map[string]map[string]any{
		"value not declared":  {"service": "WinRM"},
		"injected":            {"service": "Spooler; Restart-Computer"},
		"extra parameter":     {"service": "Spooler", "extra": "1"},
		"missing a parameter": {},
	} {
		out := callTool(t, tc, "run_preapproved_action", map[string]any{"agent_id": f.device, "script_id": sc.ID, "params": params})
		if _, refused := out["error"]; !refused {
			t.Errorf("%s: out = %v", name, out)
		}
	}
}

func TestEditingAReleasedScriptRevokesIt(t *testing.T) {
	f := newAutonomyFixture(t)
	sc := f.releasedScript(t, "Restart-Service -Name $env:OPENRMM_PARAM_SERVICE", spoolerParam)
	edited := *sc
	edited.Body = sc.Body + "\nGet-Service"
	revoked, err := f.st.UpdateScript(f.ctx, &edited)
	if err != nil || !revoked {
		t.Fatalf("revoked=%v err=%v", revoked, err)
	}
	// Renaming keeps a release; only what runs is bound.
	sc2 := f.releasedScript(t, "Get-Date", nil)
	renamed := *sc2
	renamed.Name = sc2.Name + " (renamed)"
	if revoked, err := f.st.UpdateScript(f.ctx, &renamed); err != nil || revoked {
		t.Fatalf("a rename revoked the release: %v %v", revoked, err)
	}
	out := callTool(t, f.tc(ChannelChat, models.PermAIAutonomyRun), "run_preapproved_action",
		map[string]any{"agent_id": f.device, "script_id": sc.ID, "params": map[string]any{"service": "Spooler"}})
	if errText, _ := out["error"].(string); !strings.Contains(errText, "not released") {
		t.Fatalf("out = %v", out)
	}
	// A release made against content that changed since it was read is refused.
	stale := f.releasedScript(t, "Get-Date", nil)
	if _, err := f.st.UpdateScript(f.ctx, &models.Script{ID: stale.ID, Name: stale.Name, Shell: stale.Shell, Body: "Get-Date -Format o"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetScriptAIRelease(f.ctx, stale.ID, "admin", models.ScriptContentHash(stale.Shell, stale.Body, nil), stale.UpdatedAt); err != store.ErrConflict {
		t.Fatalf("stale release = %v", err)
	}
}

// The deny list holds at run time too: a release that slipped past it (written
// straight to the database here) still does not run.
func TestDenyListHoldsAtRunTime(t *testing.T) {
	f := newAutonomyFixture(t)
	sc := f.releasedScript(t, "Restart-Computer -Force", nil)
	out := callTool(t, f.tc(ChannelChat, models.PermAIAutonomyRun), "run_preapproved_action", map[string]any{"agent_id": f.device, "script_id": sc.ID})
	if errText, _ := out["error"].(string); !strings.Contains(errText, "cannot be released") {
		t.Fatalf("out = %v", out)
	}
	if len(f.agentRPC.dispatched()) != 0 {
		t.Fatal("a denied script reached the device")
	}
}

// Enough failed runs in a row withdraw the release.
func TestRepeatedFailuresWithdrawTheRelease(t *testing.T) {
	f := newAutonomyFixture(t)
	f.agentRPC.mu.Lock()
	f.agentRPC.reply = func(protocol.JobMsg) protocol.JobResultMsg {
		return protocol.JobResultMsg{ExitCode: 1, Success: false, Stderr: "failed"}
	}
	f.agentRPC.mu.Unlock()
	sc := f.releasedScript(t, "Get-Date", nil)
	tc := f.tc(ChannelChat, models.PermAIAutonomyRun)
	for i := 0; i < jobs.ReleaseFailureLimit; i++ {
		out := callTool(t, tc, "run_preapproved_action", map[string]any{"agent_id": f.device, "script_id": sc.ID})
		if out["status"] != models.JobFailed {
			t.Fatalf("run %d: %v", i, out)
		}
	}
	// The outcome is recorded right after the result, so give it a moment.
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := f.st.GetScript(f.ctx, sc.ID)
		if err == nil && !got.AIReleased && got.AIReleasedHash == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("release survived %d failures: %+v %v", jobs.ReleaseFailureLimit, got, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Past the limit, an action goes back to approval instead of running.
func TestRateLimitSendsActionsBackToApproval(t *testing.T) {
	f := newAutonomyFixture(t)
	sc := f.releasedScript(t, "Get-Date", nil)
	tc := f.tc(ChannelChat, models.PermAIAutonomyRun)
	for i := 0; i < runLimits.PerDevice; i++ {
		if out := callTool(t, tc, "run_preapproved_action", map[string]any{"agent_id": f.device, "script_id": sc.ID}); out["ran_automatically"] != true {
			t.Fatalf("run %d: %v", i, out)
		}
	}
	out := callTool(t, tc, "run_preapproved_action", map[string]any{"agent_id": f.device, "script_id": sc.ID})
	if out["ran_automatically"] != false || out["status"] != models.JobAwaitingApproval || !strings.Contains(out["message"].(string), "limit") {
		t.Fatalf("out = %v", out)
	}
}

// A role without the run permission still gets the action, as a proposal that
// carries the same parameters for the approver to see.
func TestPreapprovedActionWithoutThePermissionIsAProposal(t *testing.T) {
	f := newAutonomyFixture(t)
	sc := f.releasedScript(t, "Restart-Service -Name $env:OPENRMM_PARAM_SERVICE", spoolerParam)
	out := callTool(t, f.tc(ChannelChat, models.PermAIAutonomyQuery), "run_preapproved_action",
		map[string]any{"agent_id": f.device, "script_id": sc.ID, "params": map[string]any{"service": "W32Time"}})
	if out["ran_automatically"] != false || out["status"] != models.JobAwaitingApproval {
		t.Fatalf("out = %v", out)
	}
	if j := f.job(t, out); j.Params["service"] != "W32Time" || j.Autonomy != models.AutonomyNone || j.ScriptID == nil {
		t.Fatalf("job = %+v", j)
	}
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// An autonomous action runs now or not at all: never on an offline device, and
// a job that could not be delivered fails at once instead of waiting to run
// whenever the device comes back (after a pause or a withdrawn release).
func TestAutonomousActionsDoNotWaitForADevice(t *testing.T) {
	f := newAutonomyFixture(t)
	sc := f.releasedScript(t, "Get-Date", nil)
	tc := f.tc(ChannelChat, models.PermAIAutonomyRun, models.PermAIAutonomyQuery)
	out := callTool(t, tc, "run_preapproved_action", map[string]any{"agent_id": f.offline, "script_id": sc.ID})
	if errText, _ := out["error"].(string); !strings.Contains(errText, "offline") {
		t.Fatalf("offline device: %v", out)
	}
	f.agentRPC.mu.Lock()
	f.agentRPC.down = true
	f.agentRPC.mu.Unlock()
	out = callTool(t, tc, "query_device", map[string]any{"agent_id": f.device, "query": "disk"})
	if out["status"] != models.JobFailed {
		t.Fatalf("undelivered query: %v", out)
	}
	j := f.job(t, out)
	if j.Status != models.JobFailed || !strings.Contains(j.Stderr, "offline") {
		t.Fatalf("job = %+v", j)
	}
	pending, err := f.st.ListDispatchableJobs(f.ctx, f.device)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pending {
		if p.ID == j.ID {
			t.Fatal("the failed autonomous job is still waiting to be sent")
		}
	}
}

// get_job_status shows the model the same bounded, redacted output.
func TestJobStatusDoesNotLeakWhatAutonomousToolsRedact(t *testing.T) {
	f := newAutonomyFixture(t)
	out := callTool(t, f.tc(ChannelChat, models.PermAIAutonomyQuery, models.PermJobsView), "query_device",
		map[string]any{"agent_id": f.device, "query": "system"})
	status := callTool(t, f.tc(ChannelChat, models.PermJobsView), "get_job_status", map[string]any{"job_id": out["job_id"]})
	if raw := mustJSON(status); strings.Contains(raw, "hunter2") || !strings.Contains(raw, "device_output") {
		t.Fatalf("status = %s", raw)
	}
}

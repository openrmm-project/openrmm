package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

func TestServerRuleEvaluationLifecycleIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := storetest.NewSchema(t, "server_rules", url)
	a, err := st.CreateAgent(ctx, "rule-device", "linux", "amd64", "0.9.5", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{store: st, serverVersion: "0.10.0"}
	now := time.Now().UTC()
	alerts := func() []models.Alert {
		t.Helper()
		got, err := st.ListAlerts(ctx, "", 20)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	s.evaluateServerRules(ctx, []models.Agent{*a}, now)
	if len(alerts()) != 0 {
		t.Fatal("upgrade enabled a new rule without opt-in")
	}
	c := &models.Check{AgentID: &a.ID, Name: "outdated", Type: models.CheckAgentOutdated, Config: json.RawMessage(`{"behind":"minor"}`), Enabled: true}
	if err := models.ValidateCheck(c); err != nil {
		t.Fatal(err)
	}
	c, err = st.CreateCheck(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	s.evaluateServerRules(ctx, []models.Agent{*a}, now)
	got := alerts()
	if len(got) != 1 || got[0].Status != models.AlertNew || got[0].RuleID == nil || *got[0].RuleID != c.ID {
		t.Fatalf("missing rule incident: %+v", got)
	}
	s.evaluateServerRules(ctx, []models.Agent{*a}, now.Add(time.Minute))
	if repeated := alerts(); len(repeated) != 1 || repeated[0].OccurrenceCount != got[0].OccurrenceCount {
		t.Fatalf("repeated evaluation duplicated observation: %+v", repeated)
	}
	a.Version = "dev"
	s.evaluateServerRules(ctx, []models.Agent{*a}, now.Add(2*time.Minute))
	if alerts()[0].Status == models.AlertResolved {
		t.Fatal("unknown agent version declared recovery")
	}
	a.Version = "0.9.5"
	s.serverVersion = "dev"
	s.evaluateServerRules(ctx, []models.Agent{*a}, now.Add(3*time.Minute))
	if alerts()[0].Status == models.AlertResolved {
		t.Fatal("unknown server version declared recovery")
	}
	s.serverVersion = "0.10.0"
	c.Config = json.RawMessage(`{"mode":"ignore"}`)
	if err := models.ValidateCheck(c); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	s.evaluateServerRules(ctx, []models.Agent{*a}, now.Add(4*time.Minute))
	if got := alerts(); got[0].Status != models.AlertResolved || got[0].ResolutionKind != "inapplicable" {
		t.Fatalf("ignore did not retire incident: %+v", got)
	}
	// Control: an active rule really recovers once the device updates.
	c.Config = json.RawMessage(`{"behind":"minor"}`)
	if err := models.ValidateCheck(c); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateCheck(ctx, c); err != nil {
		t.Fatal(err)
	}
	s.evaluateServerRules(ctx, []models.Agent{*a}, now.Add(5*time.Minute))
	a.Version = "0.10.0"
	s.evaluateServerRules(ctx, []models.Agent{*a}, now.Add(6*time.Minute))
	for _, alert := range alerts() {
		if alert.Status != models.AlertResolved {
			t.Fatalf("updated device still unhealthy: %+v", alert)
		}
	}
}

func TestPatchJobAndRebootRuleLifecycleIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := storetest.NewSchema(t, "rule_conditions", url)
	a, err := st.CreateAgent(ctx, "conditions", "linux", "amd64", "0.10.0", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	set := func(kind, raw string) {
		t.Helper()
		cfg, err := models.ParseServerRuleConfig(kind, json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetAlertRuleDefault(ctx, kind, cfg, "operator"); err != nil {
			t.Fatal(err)
		}
	}
	s := &Scheduler{store: st, serverVersion: "0.10.0"}
	run := func() { s.evaluateServerRules(ctx, []models.Agent{*a}, time.Now()) }
	open := func() map[string]models.Alert {
		t.Helper()
		alerts, err := st.ListAlerts(ctx, "", 20)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]models.Alert{}
		for _, alert := range alerts {
			if alert.Status != models.AlertResolved {
				out[alert.ConditionKey] = alert
			}
		}
		return out
	}
	if err := st.UpsertPatchReport(ctx, a.ID, &protocol.PatchReportMsg{OS: "linux", Patches: []protocol.PatchItem{{Identifier: "test-update", Title: "test update"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyPatchResult(ctx, a.ID, &protocol.PatchResultMsg{Results: []protocol.PatchResultItem{{Identifier: "test-update", Success: false}}, RebootRequired: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateJob(ctx, &models.Job{AgentID: a.ID, Type: "script", Shell: "bash", Script: "exit 1", Status: models.JobFailed, Description: "failed ad-hoc job"}); err != nil {
		t.Fatal(err)
	}
	run()
	if len(open()) != 0 {
		t.Fatal("default ignores changed fleet behavior")
	}
	set(models.CheckPatchFailure, `{"mode":"alert"}`)
	set(models.CheckRebootPending, `{"mode":"alert","after_days":0}`)
	set(models.CheckJobFailure, `{"mode":"alert","scope":"tasks"}`)
	run()
	if got := open(); len(got) != 2 || got[models.CheckPatchFailure].ID == "" || got[models.CheckRebootPending].ID == "" {
		t.Fatalf("patch/reboot or tasks-only rule incorrect: %+v", got)
	}
	set(models.CheckJobFailure, `{"mode":"alert","scope":"all"}`)
	run()
	if got := open(); len(got) != 3 || got["job:adhoc"].ID == "" {
		t.Fatalf("failed job missing: %+v", got)
	}
	// Later success, installed update and cleared reboot flag recover independently.
	if _, err := st.CreateJob(ctx, &models.Job{AgentID: a.ID, Type: "script", Shell: "bash", Script: "exit 0", Status: models.JobCompleted}); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyPatchResult(ctx, a.ID, &protocol.PatchResultMsg{Results: []protocol.PatchResultItem{{Identifier: "test-update", Success: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRebootPending(ctx, a.ID, false); err != nil {
		t.Fatal(err)
	}
	run()
	if got := open(); len(got) != 0 {
		t.Fatalf("healthy conditions did not recover: %+v", got)
	}
}

func TestConnectivityRuleDelayAndIgnoreIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := storetest.NewSchema(t, "connectivity_rule", url)
	a, err := st.CreateAgent(ctx, "sleeping-laptop", "linux", "amd64", "0.10.0", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{store: st, hub: hub.New()}
	now := time.Now()
	seen := now.Add(-10 * time.Minute)
	a.LastSeen = &seen
	rule := models.GoverningRule{Type: models.CheckConnectivity, Config: models.ServerRuleConfig{Mode: models.RuleModeAlert, Severity: models.SeverityWarning, AfterMin: 60}}
	s.evaluateOffline(ctx, a, now, rule)
	alerts, err := st.ListAlerts(ctx, "", 20)
	if err != nil || len(alerts) != 0 {
		t.Fatalf("delay ignored: %+v %v", alerts, err)
	}
	rule.Config.AfterMin = 3
	s.evaluateOffline(ctx, a, now, rule)
	alerts, err = st.ListAlerts(ctx, "", 20)
	if err != nil || len(alerts) != 1 || alerts[0].Severity != models.SeverityWarning {
		t.Fatalf("connectivity rule missing: %+v %v", alerts, err)
	}
	rule.Config.Mode = models.RuleModeIgnore
	s.evaluateOffline(ctx, a, now.Add(time.Minute), rule)
	alerts, err = st.ListAlerts(ctx, "", 20)
	if err != nil || alerts[0].Status != models.AlertResolved || alerts[0].ResolutionKind != "inapplicable" || alerts[0].NotificationPending {
		t.Fatalf("ignored outage generated recovery mail: %+v %v", alerts, err)
	}
}

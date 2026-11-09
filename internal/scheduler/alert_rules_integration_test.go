package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
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

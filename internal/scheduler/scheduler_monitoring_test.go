package scheduler

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

func float64Ptr(v float64) *float64 { return &v }

func TestComputeStatusWithPrevious(t *testing.T) {
	lowWater := models.Check{
		Type: models.CheckDiskSpace, WarnThreshold: float64Ptr(20),
		ErrThreshold: float64Ptr(10), Hysteresis: 2,
	}
	highWater := models.Check{
		Type: models.CheckCPULoad, WarnThreshold: float64Ptr(80),
		ErrThreshold: float64Ptr(90), Hysteresis: 2,
	}
	tests := []struct {
		name     string
		check    models.Check
		value    *float64
		success  bool
		previous string
		want     string
	}{
		{"disk missing value fails closed", lowWater, nil, false, models.CheckUnknown, models.CheckFailing},
		{"disk endpoint failure fails closed", lowWater, float64Ptr(50), false, models.CheckPassing, models.CheckFailing},
		{"disk negative value fails closed", lowWater, float64Ptr(-1), true, models.CheckPassing, models.CheckFailing},
		{"disk oversized value fails closed", lowWater, float64Ptr(101), true, models.CheckPassing, models.CheckFailing},
		{"disk NaN fails closed", lowWater, float64Ptr(math.NaN()), true, models.CheckPassing, models.CheckFailing},
		{"disk error", lowWater, float64Ptr(9), true, models.CheckPassing, models.CheckFailing},
		{"disk error hysteresis holds", lowWater, float64Ptr(11), true, models.CheckFailing, models.CheckFailing},
		{"disk leaves error into warning", lowWater, float64Ptr(13), true, models.CheckFailing, models.CheckWarning},
		{"disk warning hysteresis holds", lowWater, float64Ptr(21), true, models.CheckWarning, models.CheckWarning},
		{"disk clears warning", lowWater, float64Ptr(23), true, models.CheckWarning, models.CheckPassing},
		{"cpu error", highWater, float64Ptr(91), true, models.CheckPassing, models.CheckFailing},
		{"cpu error hysteresis holds", highWater, float64Ptr(89), true, models.CheckFailing, models.CheckFailing},
		{"cpu leaves error into warning", highWater, float64Ptr(87), true, models.CheckFailing, models.CheckWarning},
		{"cpu warning hysteresis holds", highWater, float64Ptr(79), true, models.CheckWarning, models.CheckWarning},
		{"cpu clears warning", highWater, float64Ptr(77), true, models.CheckWarning, models.CheckPassing},
		{"cpu infinity fails closed", highWater, float64Ptr(math.Inf(1)), true, models.CheckPassing, models.CheckFailing},
		{"ping success", models.Check{Type: models.CheckPing}, nil, true, models.CheckUnknown, models.CheckPassing},
		{"ping failure", models.Check{Type: models.CheckPing}, nil, false, models.CheckPassing, models.CheckFailing},
		{"service failure", models.Check{Type: models.CheckWinService}, nil, false, models.CheckPassing, models.CheckFailing},
		{"script zero passes", models.Check{Type: models.CheckScript}, float64Ptr(0), true, models.CheckUnknown, models.CheckPassing},
		{"script one warns", models.Check{Type: models.CheckScript}, float64Ptr(1), false, models.CheckUnknown, models.CheckWarning},
		{"script nonzero fails", models.Check{Type: models.CheckScript}, float64Ptr(2), false, models.CheckUnknown, models.CheckFailing},
		{"script fractional fails closed", models.Check{Type: models.CheckScript}, float64Ptr(0.5), true, models.CheckUnknown, models.CheckFailing},
		{"unknown type fails closed", models.Check{Type: "future-check"}, float64Ptr(0), true, models.CheckUnknown, models.CheckFailing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeStatusWithPrevious(&tt.check, protocol.CheckResultMsg{
				Value: tt.value, Success: tt.success,
			}, tt.previous)
			if got != tt.want {
				t.Fatalf("status = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOnCheckResultRejectsMalformedIdentifiersWithoutStoreAccess(t *testing.T) {
	s := &Scheduler{}
	if ack, err := s.OnCheckResult(context.Background(), "agent", protocol.CheckResultMsg{
		CheckID: "not-a-uuid", ExecutionID: "also-not-a-uuid",
	}); !ack || err == nil {
		t.Fatalf("invalid check id: ack=%v err=%v", ack, err)
	}
	if ack, err := s.OnCheckResult(context.Background(), "agent", protocol.CheckResultMsg{
		CheckID: "00000000-0000-0000-0000-000000000001", ExecutionID: "not-a-uuid",
	}); !ack || err == nil {
		t.Fatalf("invalid execution id: ack=%v err=%v", ack, err)
	}
}

func TestTruncateCheckOutputBoundsRunes(t *testing.T) {
	input := strings.Repeat("á", maxCheckOutputRunes+25)
	got := truncateCheckOutput(input)
	if !strings.HasSuffix(got, "\n[output truncated]") {
		t.Fatalf("truncated output has no marker: %q", got[len(got)-30:])
	}
	if body := strings.TrimSuffix(got, "\n[output truncated]"); len([]rune(body)) != maxCheckOutputRunes {
		t.Fatalf("retained runes = %d, want %d", len([]rune(body)), maxCheckOutputRunes)
	}
}

func TestFailureAndRecoveryCountsGateAlertTransitions(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := storetest.NewSchema(t, "scheduler_alert_state", databaseURL)
	ctx := context.Background()
	agent, err := st.CreateAgent(ctx, "counted-host", "linux", "amd64", "test", "hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	check, err := st.CreateCheck(ctx, &models.Check{
		AgentID: &agent.ID, Type: models.CheckScript, Name: "counted check",
		Config:      json.RawMessage(`{"shell":"bash","script":"exit 1"}`),
		IntervalSec: 30, FailureCount: 2, RecoveryCount: 2,
		TimeoutSec: 30, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, nil, nil, nil)
	now := time.Now().UTC()

	apply := func(status string, failures, successes int, at time.Time) {
		t.Helper()
		if err := s.applyCheckState(ctx, check, agent.ID, &models.CheckState{
			CheckID: check.ID, AgentID: agent.ID, Status: status,
			ConsecutiveFailures: failures, ConsecutiveSuccesses: successes,
		}, nil, status, at); err != nil {
			t.Fatal(err)
		}
	}
	alerts := func() []models.Alert {
		t.Helper()
		rows, err := st.ListAlerts(ctx, "", 20)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	apply(models.CheckFailing, 1, 0, now)
	if got := alerts(); len(got) != 0 {
		t.Fatalf("first failure opened %d alerts, want none", len(got))
	}
	apply(models.CheckFailing, 2, 0, now.Add(time.Second))
	got := alerts()
	if len(got) != 1 || got[0].Status != models.AlertNew {
		t.Fatalf("second failure alerts = %#v, want one new alert", got)
	}
	apply(models.CheckPassing, 0, 1, now.Add(2*time.Second))
	if got := alerts(); got[0].Status == models.AlertResolved {
		t.Fatal("first success resolved the alert before recovery_count")
	}
	apply(models.CheckPassing, 0, 2, now.Add(3*time.Second))
	if got := alerts(); len(got) != 1 || got[0].Status != models.AlertResolved {
		t.Fatalf("second success alerts = %#v, want the incident resolved", got)
	}
}

func TestGroupPolicyCheckTargetsAndTrustBoundary(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := storetest.NewSchema(t, "scheduler_group_policy", databaseURL)
	ctx := context.Background()
	client, err := st.CreateClient(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, "site")
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateGroup(ctx, "group", &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, "group-host", "linux", "amd64", "test", "hash", &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentGroup(ctx, agent.ID, &group.ID); err != nil {
		t.Fatal(err)
	}
	policy, err := st.CreatePolicy(ctx, "group policy", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.AssignPolicy(ctx, policy.ID, models.ScopeGroup, group.ID); err != nil {
		t.Fatal(err)
	}
	check, err := st.CreateCheck(ctx, &models.Check{
		PolicyID: &policy.ID, Type: models.CheckPing, Name: "group ping",
		Config: json.RawMessage(`{"host":"127.0.0.1"}`), IntervalSec: 30,
		FailureCount: 1, RecoveryCount: 1, TimeoutSec: 30, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	applies, err := st.CheckAppliesToAgent(ctx, check.ID, agent.ID)
	if err != nil || !applies {
		t.Fatalf("group policy applies = %v, err=%v", applies, err)
	}
	checks, err := st.ChecksForAgent(ctx, agent.ID, &site.ID, &client.ID)
	if err != nil || len(checks) != 1 || checks[0].ID != check.ID {
		t.Fatalf("effective checks = %#v, err=%v", checks, err)
	}
	instances, err := st.CheckInstances(ctx, check.ID, "", false)
	if err != nil || len(instances) != 1 || instances[0].AgentID != agent.ID || instances[0].Status != models.CheckUnknown {
		t.Fatalf("instances = %#v, err=%v", instances, err)
	}
	if err := st.RevokeAgent(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	applies, err = st.CheckAppliesToAgent(ctx, check.ID, agent.ID)
	if err != nil || applies {
		t.Fatalf("revoked device applies = %v, err=%v", applies, err)
	}
	ids, err := st.AgentIDsForPolicy(ctx, policy.ID)
	if err != nil || len(ids) != 0 {
		t.Fatalf("revoked policy targets = %#v, err=%v", ids, err)
	}
}

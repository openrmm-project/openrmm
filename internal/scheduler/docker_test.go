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

func TestDockerUnknownCannotBecomeRecovery(t *testing.T) {
	c := &models.Check{Type: models.CheckDockerContainer}
	for _, previous := range []string{models.CheckUnknown, models.CheckPassing, models.CheckFailing} {
		if got := computeStatusWithPrevious(c, protocol.CheckResultMsg{Success: true, Unknown: true}, previous); got != models.CheckUnknown {
			t.Fatalf("unknown becomes %s", got)
		}
	}
	if computeStatus(c, protocol.CheckResultMsg{Success: true}) != models.CheckPassing || computeStatus(c, protocol.CheckResultMsg{}) != models.CheckFailing {
		t.Fatal("Docker boolean state")
	}
}

func TestDockerCapabilityDispatchAndAlertLifecycle(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := storetest.NewSchema(t, "docker_checks", url)
	live := hub.New()
	s := New(st, live, nil, nil)
	a, err := st.CreateAgent(ctx, "docker-agent", "linux", "amd64", "0.1.0", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := st.CreateCheck(ctx, &models.Check{Attachments: []models.Attachment{{AgentID: &a.ID}}, Type: models.CheckDockerContainer, Name: "Web health", Config: json.RawMessage(`{"container":"web","mode":"healthy"}`), IntervalSec: 30, TimeoutSec: 10, FailureCount: 2, RecoveryCount: 2, Enabled: true, CooldownSec: 300})
	if err != nil {
		t.Fatal(err)
	}
	ch := live.Add(a.ID, nil)
	s.dispatchChecks(ctx, a.ID, nil, nil, time.Now())
	select {
	case <-ch:
		t.Fatal("old connection received Docker check")
	default:
	}
	state, err := st.GetCheckState(ctx, c.ID, a.ID)
	if err != nil || state.Status != models.CheckUnknown {
		t.Fatalf("old agent state: %+v %v", state, err)
	}
	live.SetDockerSupport(a.ID, ch, true)
	now := time.Now()
	s.dispatchChecks(ctx, a.ID, nil, nil, now)
	var message protocol.RunCheckMsg
	select {
	case raw := <-ch:
		var env protocol.Envelope
		_ = json.Unmarshal(raw, &env)
		_ = json.Unmarshal(env.Data, &message)
		if message.Type != models.CheckDockerContainer || message.ExecutionID == "" {
			t.Fatalf("dispatch %+v", message)
		}
	default:
		t.Fatal("capable agent not dispatched")
	}
	// A replacement cannot inherit either capability or the old pending run.
	newer := live.Add(a.ID, nil)
	s.dispatchChecks(ctx, a.ID, nil, nil, now.Add(time.Minute))
	select {
	case <-newer:
		t.Fatal("replacement received unsupported type")
	default:
	}
	if pending, err := st.CancelPendingCheckExecutions(ctx, c.ID, &a.ID); err != nil || pending != 0 {
		t.Fatalf("pending run survived capability loss: %d %v", pending, err)
	}
	live.SetDockerSupport(a.ID, newer, true)
	// Use separate durable executions, as actual check runs do. Unknown breaks
	// consecutiveness but must leave the incident open and send no recovery.
	report := func(success, unknown bool) {
		t.Helper()
		execution, created, err := st.CreateCheckExecution(ctx, c.ID, a.ID, time.Now(), 10*time.Second)
		if err != nil || !created {
			t.Fatalf("execution %v %v", created, err)
		}
		ack, err := s.OnCheckResult(ctx, a.ID, protocol.CheckResultMsg{CheckID: c.ID, ExecutionID: execution.ID, Success: success, Unknown: unknown, Output: "Docker observation"})
		if err != nil || !ack {
			t.Fatalf("result ack=%v err=%v", ack, err)
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
	report(false, false)
	if len(alerts()) != 0 {
		t.Fatal("first failure alerted before threshold")
	}
	report(false, false)
	rows := alerts()
	if len(rows) != 1 || rows[0].Status == models.AlertResolved {
		t.Fatalf("missing incident: %+v", rows)
	}
	report(true, false)
	if alerts()[0].Status == models.AlertResolved {
		t.Fatal("first success recovered early")
	}
	report(false, true)
	if alerts()[0].Status == models.AlertResolved {
		t.Fatal("unknown falsely recovered")
	}
	report(true, false)
	if alerts()[0].Status == models.AlertResolved {
		t.Fatal("unknown did not break consecutive successes")
	}
	report(true, false)
	if alerts()[0].Status != models.AlertResolved {
		t.Fatal("two fresh successes did not recover")
	}
	windows, err := st.CreateAgent(ctx, "windows", "windows", "amd64", "new", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wc, err := st.CreateCheck(ctx, &models.Check{Attachments: []models.Attachment{{AgentID: &windows.ID}}, Type: models.CheckDockerContainer, Name: "Linux-only", Config: json.RawMessage(`{"container":"web"}`), IntervalSec: 30, TimeoutSec: 10, FailureCount: 1, RecoveryCount: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if checks, err := st.ChecksForAgent(ctx, windows.ID, nil, nil); err != nil || len(checks) != 0 {
		t.Fatalf("Docker check in non-Linux effective checks: %+v %v", checks, err)
	}
	wch := live.Add(windows.ID, nil)
	live.SetDockerSupport(windows.ID, wch, true)
	s.dispatchChecks(ctx, windows.ID, nil, nil, time.Now())
	select {
	case <-wch:
		t.Fatal("non-Linux agent received Docker check")
	default:
	}
	if applies, err := st.CheckAppliesToAgent(ctx, wc.ID, windows.ID); err != nil || applies {
		t.Fatalf("non-Linux inbound result accepted: %v %v", applies, err)
	}
}

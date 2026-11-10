package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

func TestPlatformServiceCheckStatus(t *testing.T) {
	for _, kind := range []string{models.CheckLinuxService, models.CheckMacService, models.CheckWinService} {
		for _, success := range []bool{true, false} {
			want := models.CheckFailing
			if success {
				want = models.CheckPassing
			}
			if got := computeStatus(&models.Check{Type: kind}, protocol.CheckResultMsg{Success: success}); got != want {
				t.Errorf("%s/%v=%s", kind, success, got)
			}
		}
	}
}

func TestPlatformServiceCheckDispatchVersionGateIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := storetest.NewSchema(t, "service_dispatch", url)
	live := hub.New()
	s := New(st, live, nil, nil)
	for _, version := range []string{"0.9.5", "0.10.0"} {
		a, err := st.CreateAgent(ctx, "service-agent-"+version, "linux", "amd64", version, "hash", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		c, err := st.CreateCheck(ctx, &models.Check{AgentID: &a.ID, Type: models.CheckLinuxService, Name: "sshd", Config: json.RawMessage(`{"service":"sshd.service"}`), Enabled: true, IntervalSec: 300, TimeoutSec: 30, FailureCount: 2, RecoveryCount: 2})
		if err != nil {
			t.Fatal(err)
		}
		ch := live.Add(a.ID, func() {})
		s.dispatchChecks(ctx, a.ID, nil, nil, time.Now())
		if version == "0.9.5" {
			select {
			case <-ch:
				t.Fatal("old agent received unsupported check")
			default:
			}
			state, err := st.GetCheckState(ctx, c.ID, a.ID)
			if err != nil || state.Status != models.CheckUnknown || !strings.Contains(state.LastOutput, "0.10.0") {
				t.Fatalf("old agent state=%+v %v", state, err)
			}
			if count, err := st.CancelPendingCheckExecutions(ctx, c.ID, &a.ID); err != nil || count != 0 {
				t.Fatalf("old agent execution created: %d %v", count, err)
			}
		} else {
			select {
			case frame := <-ch:
				var env protocol.Envelope
				if err := json.Unmarshal(frame, &env); err != nil {
					t.Fatal(err)
				}
				var msg protocol.RunCheckMsg
				if err := json.Unmarshal(env.Data, &msg); err != nil {
					t.Fatal(err)
				}
				if env.Type != protocol.MsgRunCheck || msg.Type != models.CheckLinuxService || msg.ExecutionID == "" {
					t.Fatalf("bad dispatch=%+v", msg)
				}
			default:
				t.Fatal("new agent did not receive service check")
			}
		}
	}
}

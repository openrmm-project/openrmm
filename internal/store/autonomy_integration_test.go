package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// The database side of AI autonomy (docs/23-ai-autonomy.md): limits applied
// inside the transaction, releases bound to content, the circuit breaker, and
// no late redelivery of an autonomous job.
func TestAutonomyStoreIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	st := newIsolatedStore(t, "store_autonomy", url)
	c, err := st.CreateClient(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, c.ID, "A")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, "host", "windows", "amd64", "0.10.0", "tok-host", &site.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	basis := "autonomy: ai.autonomy.query"
	newJob := func() *models.Job {
		return &models.Job{AgentID: agent.ID, Type: models.JobDeviceQuery, Script: `{"query":"disk"}`,
			CreatedBy: "ai:tester", ApprovedBy: &basis, Autonomy: models.AutonomyQuery}
	}

	t.Run("limits are enforced in the transaction", func(t *testing.T) {
		limits := AutonomyLimits{Window: time.Hour, PerCreator: 2}
		for i := 0; i < 2; i++ {
			if _, err := st.CreateAutonomousJob(ctx, newJob(), limits); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.CreateAutonomousJob(ctx, newJob(), limits); err != ErrRateLimited {
			t.Fatalf("third job = %v, want ErrRateLimited", err)
		}
		if _, err := st.CreateAutonomousJob(ctx, &models.Job{AgentID: agent.ID, CreatedBy: "x"}, limits); err == nil {
			t.Fatal("a job without an autonomy marker was accepted")
		}
	})

	t.Run("an old autonomous job is not redelivered", func(t *testing.T) {
		j, err := st.CreateAutonomousJob(ctx, newJob(), AutonomyLimits{})
		if err != nil {
			t.Fatal(err)
		}
		has := func() bool {
			list, err := st.ListDispatchableJobs(ctx, agent.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range list {
				if x.ID == j.ID {
					return true
				}
			}
			return false
		}
		if !has() {
			t.Fatal("a fresh autonomous job is not dispatchable")
		}
		if _, err := st.pool.Exec(ctx, `UPDATE jobs SET created_at = now() - interval '10 minutes' WHERE id=$1`, j.ID); err != nil {
			t.Fatal(err)
		}
		if has() {
			t.Fatal("a 10-minute-old autonomous job would be sent to a reconnecting agent")
		}
	})

	t.Run("a release is bound to content, and repeated failures withdraw it", func(t *testing.T) {
		sc, err := st.CreateScript(ctx, &models.Script{Name: "flush", Shell: models.ShellPowerShell, Body: "Clear-DnsClientCache"})
		if err != nil {
			t.Fatal(err)
		}
		hash := models.ScriptContentHash(sc.Shell, sc.Body, sc.Params)
		if err := st.SetScriptAIRelease(ctx, sc.ID, "admin", hash, sc.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 2; i++ {
			if revoked, err := st.RecordAIScriptOutcome(ctx, sc.ID, false, 3); err != nil || revoked {
				t.Fatalf("failure %d: revoked=%v err=%v", i, revoked, err)
			}
		}
		if revoked, _ := st.RecordAIScriptOutcome(ctx, sc.ID, true, 3); revoked {
			t.Fatal("a success revoked the release")
		}
		for i := 1; i <= 3; i++ {
			revoked, err := st.RecordAIScriptOutcome(ctx, sc.ID, false, 3)
			if err != nil || revoked != (i == 3) {
				t.Fatalf("failure %d after a success: revoked=%v err=%v", i, revoked, err)
			}
		}
		got, err := st.GetScript(ctx, sc.ID)
		if err != nil || got.AIReleased || got.AIReleasedBy != nil {
			t.Fatalf("after the breaker: %+v %v", got, err)
		}
		if had, err := st.ClearScriptAIRelease(ctx, sc.ID); err != nil || had {
			t.Fatalf("clearing an absent release: had=%v err=%v", had, err)
		}
	})
}

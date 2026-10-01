package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/jobs"
	"github.com/p1ngu/rmm/internal/models"
)

type countingSender struct{ n atomic.Int64 }

func (c *countingSender) Send(string, []byte) bool { c.n.Add(1); return true }

// Approve and reject used to be a status check followed by an unconditional
// write, so two operators deciding at once both "succeeded": the job ended up
// recorded as rejected and was sent to the device anyway.
func TestApproveAndRejectRaceHasOneWinnerIntegration(t *testing.T) {
	e := newSMTPE2E(t)
	agent, err := e.store.CreateAgent(e.ctx, "race", "linux", "amd64", "1", "tok-race-"+uuid.NewString()[:8], nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		sender := &countingSender{}
		svc := jobs.New(e.store, sender)
		j, err := e.store.CreateJob(e.ctx, &models.Job{AgentID: agent.ID, Type: models.JobRunScript, Shell: "bash",
			Script: "echo hi", Description: "race", Status: models.JobAwaitingApproval, CreatedBy: "ai:assistant"})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var approveErr, rejectErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, approveErr = svc.Approve(context.Background(), j.ID, "alice") }()
		go func() { defer wg.Done(); _, rejectErr = svc.Reject(context.Background(), j.ID, "bob") }()
		wg.Wait()

		if (approveErr == nil) == (rejectErr == nil) {
			t.Fatalf("round %d: approve err=%v, reject err=%v; want exactly one to succeed", i, approveErr, rejectErr)
		}
		final, err := e.store.GetJob(e.ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rejectErr == nil {
			if final.Status != models.JobRejected || sender.n.Load() != 0 {
				t.Fatalf("round %d: reject won, yet status=%s and %d dispatch(es)", i, final.Status, sender.n.Load())
			}
		} else if final.Status == models.JobRejected || sender.n.Load() != 1 {
			t.Fatalf("round %d: approve won, yet status=%s and %d dispatch(es)", i, final.Status, sender.n.Load())
		}
	}
}

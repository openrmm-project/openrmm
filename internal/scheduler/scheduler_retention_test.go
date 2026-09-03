package scheduler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/store"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

// PurgeAuditLog is covered thoroughly on its own. What was not covered is
// whether the scheduler ever CALLS it — and that is the half that makes it
// happen. A wrong argument, or the call sitting in a branch that never runs,
// would leave every existing test green while the purge never fired. The
// symptom would be a disk filling months later, with nothing pointing at the
// cause.
func TestTickRunsAuditRetentionIntegration(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := storetest.NewSchema(t, "scheduler_audit_retention", databaseURL)
	ctx := context.Background()

	// Old machine entries, which retention exists to remove, and an old human
	// one, which it must never touch whatever its age.
	old := time.Now().AddDate(0, 0, -200)
	write := func(actor, action string) {
		t.Helper()
		if err := st.AddAuditAt(ctx, actor, action, "", nil, old); err != nil {
			t.Fatal(err)
		}
	}
	write("scheduler", "job.automated")
	write("agent", "job.result")
	write("alice", "job.approve")

	s := New(st, nil, nil, nil).WithAuditRetention(90)
	// lastRetention is the zero time on a fresh scheduler, so the first tick is
	// already due — which is also the real behaviour on a server restart.
	s.tick(ctx)

	entries, total, err := st.ListAudit(ctx, store.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byAction := map[string]int{}
	for _, e := range entries {
		byAction[e.Action]++
	}
	if byAction["job.automated"] != 0 || byAction["job.result"] != 0 {
		t.Errorf("the tick did not purge the machine entries: %v", byAction)
	}
	if byAction["job.approve"] != 1 {
		t.Errorf("the tick removed a human entry; it must never do that: %v", byAction)
	}
	// The purge records itself, so its own entry is expected in the total.
	if byAction["audit.purge"] != 1 {
		t.Errorf("the purge left no record of itself: %v (total %d)", byAction, total)
	}
}

// Retention disabled means the tick leaves the trail alone. Without this, a
// deployment that set 0 to keep everything could still be quietly pruned.
func TestTickHonoursDisabledAuditRetentionIntegration(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := storetest.NewSchema(t, "scheduler_audit_keep", databaseURL)
	ctx := context.Background()

	if err := st.AddAuditAt(ctx, "scheduler", "job.automated", uuid.NewString(), nil,
		time.Now().AddDate(0, 0, -3650)); err != nil {
		t.Fatal(err)
	}

	New(st, nil, nil, nil).WithAuditRetention(0).tick(ctx)

	// Asserting on the ACTION, not on a count. A purge that ran would delete the
	// entry and write its own audit.purge record in its place, leaving the total
	// at 1 either way — a test that only counted would pass while the guarantee
	// was broken.
	entries, _, err := st.ListAudit(ctx, store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var kept, purged int
	for _, e := range entries {
		switch e.Action {
		case "job.automated":
			kept++
		case "audit.purge":
			purged++
		}
	}
	if kept != 1 {
		t.Errorf("the ten-year-old entry was removed; retention was disabled")
	}
	if purged != 0 {
		t.Errorf("a purge ran with retention disabled (%d purge records)", purged)
	}
}

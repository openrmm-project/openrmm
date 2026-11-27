package scheduler

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/p1ngu/rmm/internal/hub"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/patches"
	"github.com/p1ngu/rmm/internal/protocol"
	"github.com/p1ngu/rmm/internal/store"
)

func TestWindowState(t *testing.T) {
	// A fixed Wednesday at 10:00.
	now := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	if now.Weekday() != time.Wednesday {
		t.Fatalf("fixture weekday changed: %v", now.Weekday())
	}
	anyDayNoReboot := models.PatchPolicy{WindowStartMin: 0, WindowEndMin: 1439, Reboot: models.RebootNever}
	wedWithReboot := models.PatchPolicy{WindowDays: []int{int(time.Wednesday)}, WindowStartMin: 540, WindowEndMin: 660, Reboot: models.RebootAlways}
	mondayOnly := models.PatchPolicy{WindowDays: []int{int(time.Monday)}, WindowStartMin: 0, WindowEndMin: 1439, Reboot: models.RebootIfRequired}
	earlyWindow := models.PatchPolicy{WindowStartMin: 0, WindowEndMin: 300, Reboot: models.RebootAlways} // 00:00–05:00

	tests := []struct {
		name              string
		pols              []models.PatchPolicy
		wantOpen, wantReb bool
	}{
		{"any day, no reboot", []models.PatchPolicy{anyDayNoReboot}, true, false},
		{"matching day + reboot", []models.PatchPolicy{wedWithReboot}, true, true},
		{"wrong day", []models.PatchPolicy{mondayOnly}, false, false},
		{"outside hours", []models.PatchPolicy{earlyWindow}, false, false},
		{"one open grants reboot", []models.PatchPolicy{mondayOnly, wedWithReboot}, true, true},
		{"none", nil, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			open, reb := windowState(tc.pols, now)
			if open != tc.wantOpen || reb != tc.wantReb {
				t.Fatalf("windowState = (%v,%v), want (%v,%v)", open, reb, tc.wantOpen, tc.wantReb)
			}
		})
	}
}

func TestSchedulerPatchTickDoesNotRebootWithLiveRemoteSession(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st := newSchedulerTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	agent, err := st.CreateAgent(ctx, "scheduled-agent", "linux", "amd64", "1.0.0", "token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := st.CreatePatchPolicy(ctx, &models.PatchPolicy{
		Name:           "always-open",
		Enabled:        true,
		WindowStartMin: 0,
		WindowEndMin:   1439,
		Reboot:         models.RebootAlways,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddPatchPolicyAssignment(ctx, policy.ID, models.ScopeAgent, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.ObserveReboot(ctx, agent.ID, &protocol.RebootStatus{Status: "required", BootID: "boot-one"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRemoteSession(ctx, &models.RemoteSession{
		AgentID: agent.ID, Provider: "native", Status: "active", Token: "session-token",
		CreatedBy: "operator", ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	liveHub := hub.New()
	outbound := liveHub.Add(agent.ID, nil)
	scheduler := New(st, liveHub, nil, patches.New(st, liveHub))
	scheduler.patchTick(ctx, []models.Agent{*agent}, time.Now())

	for {
		select {
		case raw := <-outbound:
			var frame protocol.Envelope
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatal(err)
			}
			if frame.Type == protocol.MsgReboot {
				t.Fatal("scheduler requested a reboot while a native remote session was active")
			}
		default:
			pending, err := st.RebootPendingAgents(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range pending {
				if id == agent.ID {
					return
				}
			}
			t.Fatal("scheduler cleared reboot_pending despite a live remote session")
		}
	}
}

func newSchedulerTestStore(t *testing.T, databaseURL string) *store.Store {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schemaName := "scheduler_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	database, err := url.Parse(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	query := database.Query()
	query.Set("search_path", schemaName)
	database.RawQuery = query.Encode()
	st, err := store.New(ctx, database.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
		admin.Close()
	})
	return st
}

func TestSchedulerRebootRequiresFreshObservationAndDoesNotRepeat(t *testing.T) {
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newSchedulerTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAgent(ctx, "reboot-scheduler", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.CreatePatchPolicy(ctx, &models.PatchPolicy{Name: "always-open", Enabled: true, WindowStartMin: 0, WindowEndMin: 1439, Reboot: models.RebootAlways})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.AddPatchPolicyAssignment(ctx, p.ID, models.ScopeAgent, a.ID); err != nil {
		t.Fatal(err)
	}
	h := hub.New()
	out := h.Add(a.ID, nil)
	s := New(st, h, nil, patches.New(st, h))
	tick := func() int {
		t.Helper()
		s.patchTick(ctx, []models.Agent{*a}, time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC))
		count := 0
		for len(out) > 0 {
			var e protocol.Envelope
			if err := json.Unmarshal(<-out, &e); err != nil {
				t.Fatal(err)
			}
			if e.Type == protocol.MsgReboot {
				count++
			}
		}
		return count
	}
	if err = st.SetRebootPending(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if n := tick(); n != 0 {
		t.Fatalf("legacy flag triggered %d reboots", n)
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if n := tick(); n != 0 {
		t.Fatalf("unknown triggered %d reboots", n)
	}
	if err = st.ObserveReboot(ctx, a.ID, &protocol.RebootStatus{Status: "required", BootID: "boot-one"}); err != nil {
		t.Fatal(err)
	}
	if n := tick(); n != 1 {
		t.Fatalf("fresh positive triggered %d reboots", n)
	}
	if n := tick(); n != 0 {
		t.Fatalf("duplicate tick triggered %d reboots", n)
	}
	current, err := st.GetAgent(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !current.RebootPending || current.RebootRequestedAt == nil || current.RebootObservedAt != nil {
		t.Fatalf("request treated as confirmation: %+v", current)
	}
}

package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// TestNotificationRecipientsIntegration pins the rule that decides who gets an
// alert email. It runs against a real Postgres because the rule *is* the query:
// a scope join and a permission check, neither of which a fake can stand in for.
func TestNotificationRecipientsIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `TRUNCATE users, clients, alerts, notifications CASCADE`); err != nil {
		t.Fatal(err)
	}

	clientA, err := st.CreateClient(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	clientB, err := st.CreateClient(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	siteA, err := st.CreateSite(ctx, clientA.ID, "A1")
	if err != nil {
		t.Fatal(err)
	}
	siteB, err := st.CreateSite(ctx, clientB.ID, "B1")
	if err != nil {
		t.Fatal(err)
	}
	agentA, err := st.CreateAgent(ctx, "host-a", "linux", "amd64", "1", "tok-a", &siteA.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	mkUser := func(name, role string, scopes []models.UserScope, email string) *models.User {
		t.Helper()
		u, err := st.CreateUserWithScopes(ctx, name, "hash", role, scopes)
		if err != nil {
			t.Fatal(err)
		}
		if email != "" {
			if _, err := st.UpdateUserEmail(ctx, u.ID, email); err != nil {
				t.Fatalf("set email for %s: %v", name, err)
			}
		}
		return u
	}
	unscoped := mkUser("global-tech", models.RoleTech, nil, "global@example.test")
	inScope := mkUser("site-a-tech", models.RoleTech, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: siteA.ID}}, "sitea@example.test")
	mkUser("site-b-tech", models.RoleTech, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: siteB.ID}}, "siteb@example.test")
	mkUser("no-address", models.RoleTech, nil, "")
	// readonly lacks alerts.view: mailing an alert this account cannot open in
	// the console would be a leak, not a notification.
	mkUser("viewer", models.RoleReadOnly, nil, "viewer@example.test")

	emails := func(eventType string) map[string]bool {
		t.Helper()
		got, err := st.RecipientsForAgentEvent(ctx, agentA.ID, eventType, DefaultSubscribed(eventType))
		if err != nil {
			t.Fatalf("recipients: %v", err)
		}
		out := map[string]bool{}
		for _, r := range got {
			out[r.Email] = true
		}
		return out
	}

	got := emails(NotifyCheckFailed)
	want := map[string]bool{"global@example.test": true, "sitea@example.test": true}
	if len(got) != len(want) {
		t.Fatalf("check_failed recipients = %v, want %v", got, want)
	}
	for addr := range want {
		if !got[addr] {
			t.Errorf("expected %s to receive check_failed", addr)
		}
	}
	if got["siteb@example.test"] {
		t.Error("a user scoped to another site must never receive this device's alerts")
	}
	if got["viewer@example.test"] {
		t.Error("a role without alerts.view must not receive alert email")
	}

	// job_failed is default-off, so nobody is mailed until they opt in.
	if addrs := emails(NotifyJobFailed); len(addrs) != 0 {
		t.Fatalf("job_failed should reach nobody by default, got %v", addrs)
	}
	if err := st.SetNotificationSubscription(ctx, inScope.ID, NotifyJobFailed, true); err != nil {
		t.Fatal(err)
	}
	if addrs := emails(NotifyJobFailed); !addrs["sitea@example.test"] || len(addrs) != 1 {
		t.Fatalf("after opting in, job_failed = %v, want only sitea@example.test", addrs)
	}

	// An explicit opt-out beats a default-on event.
	if err := st.SetNotificationSubscription(ctx, unscoped.ID, NotifyCheckFailed, false); err != nil {
		t.Fatal(err)
	}
	if emails(NotifyCheckFailed)["global@example.test"] {
		t.Error("an explicit opt-out should stop a default-on event")
	}
	subs, err := st.ListNotificationSubscriptions(ctx, unscoped.ID)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, ok := subs[NotifyCheckFailed]; !ok || enabled {
		t.Errorf("subscriptions = %v, want an explicit check_failed=false", subs)
	}
}

// TestNotificationOutboxIntegration covers the queue itself: claiming increments
// attempts, retries are deferred, and giving up is terminal.
func TestNotificationOutboxIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `TRUNCATE users, notifications CASCADE`); err != nil {
		t.Fatal(err)
	}

	enqueue := func(to string) {
		t.Helper()
		if err := st.EnqueueNotification(ctx, &models.Notification{
			EventType: NotifyAgentOffline, Recipient: to, Subject: "s", Body: "b",
		}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue("one@example.test")
	enqueue("two@example.test")

	claimed, err := st.ClaimPendingNotifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 2 {
		t.Fatalf("claimed %d messages, want 2", len(claimed))
	}
	for _, n := range claimed {
		if n.Attempts != 1 {
			t.Errorf("%s: attempts = %d, want 1 after the first claim", n.Recipient, n.Attempts)
		}
	}

	// A send failure is rescheduled, so the next drain must not pick it up again.
	if claimed[0].LeaseToken == nil || claimed[1].LeaseToken == nil {
		t.Fatal("claimed notifications have no fencing lease")
	}
	if err := st.MarkNotificationFailedLeased(ctx, claimed[0].ID, *claimed[0].LeaseToken,
		1, 5, "greylisted", 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkNotificationSentLeased(ctx, claimed[1].ID, *claimed[1].LeaseToken); err != nil {
		t.Fatal(err)
	}
	again, err := st.ClaimPendingNotifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("a backed-off message and a sent one should both be skipped, got %d", len(again))
	}

	// Exhausting the attempt budget is terminal: a permanently bouncing address
	// must not keep the worker busy forever.
	if _, err := st.pool.Exec(ctx, `UPDATE notifications SET next_attempt_at=now() WHERE id=$1`, claimed[0].ID); err != nil {
		t.Fatal(err)
	}
	exhausted, err := st.ClaimPendingNotifications(ctx, 10)
	if err != nil || len(exhausted) != 1 || exhausted[0].LeaseToken == nil {
		t.Fatalf("reclaim for terminal failure = %#v, err=%v", exhausted, err)
	}
	if err := st.MarkNotificationFailedLeased(ctx, exhausted[0].ID, *exhausted[0].LeaseToken,
		5, 5, "550 no such user", time.Minute); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	if err := st.pool.QueryRow(ctx, `SELECT status, last_error FROM notifications WHERE id=$1`, claimed[0].ID).
		Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || lastError != "550 no such user" {
		t.Fatalf("status=%q last_error=%q, want failed with the reason recorded", status, lastError)
	}

	// The gauges behind /metrics. These are what make a stopped outbox visible
	// from outside: alert delivery failing is the failure that hides every other
	// failure, and it is otherwise noticed only by not receiving mail.
	stats, err := st.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 {
		t.Errorf("failed = %d, want the abandoned message counted", stats.Failed)
	}
	if stats.Pending != 0 {
		t.Errorf("pending = %d, want 0 (one sent, one failed)", stats.Pending)
	}
	if stats.OldestPendingAgeSec != 0 {
		t.Errorf("oldest pending age = %v, want 0 for an empty pending set", stats.OldestPendingAgeSec)
	}

	// A fresh pending row makes the age gauge move off zero, which is the signal
	// worth alerting on: a healthy outbox drains within a worker tick.
	enqueue("three@example.test")
	stats, err = st.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 {
		t.Errorf("pending = %d, want 1", stats.Pending)
	}
	if stats.OldestPendingAgeSec < 0 {
		t.Errorf("oldest pending age = %v, want a non-negative age", stats.OldestPendingAgeSec)
	}
}

// TestUserEmailUniquenessIntegration pins the partial unique index: addresses
// collide case-insensitively, but address-less accounts never collide.
func TestUserEmailUniquenessIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx, `TRUNCATE users CASCADE`); err != nil {
		t.Fatal(err)
	}

	first, err := st.CreateUser(ctx, "first", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateUser(ctx, "second", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	third, err := st.CreateUser(ctx, "third", "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}

	if u, err := st.UpdateUserEmail(ctx, first.ID, "  Ops@Example.Test "); err != nil {
		t.Fatal(err)
	} else if u.Email != "ops@example.test" {
		t.Errorf("email = %q, want it trimmed and lowercased", u.Email)
	}
	if _, err := st.UpdateUserEmail(ctx, second.ID, "OPS@example.test"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second claim of the same address: %v, want ErrConflict", err)
	}
	// Two accounts without an address must coexist — the index is partial.
	if _, err := st.UpdateUserEmail(ctx, second.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, third.ID, ""); err != nil {
		t.Fatalf("a second address-less account: %v", err)
	}
	// Clearing the first address frees it for someone else.
	if _, err := st.UpdateUserEmail(ctx, first.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, second.ID, "ops@example.test"); err != nil {
		t.Fatalf("reclaiming a released address: %v", err)
	}
}

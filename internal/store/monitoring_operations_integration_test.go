package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

func newOperationalTestStore(t *testing.T) *Store {
	t.Helper()
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	st := newMigrationTestStore(t, databaseURL)
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestNotificationOutboxDedupeLeaseReclaimAndFencing(t *testing.T) {
	st := newOperationalTestStore(t)
	ctx := context.Background()
	n := &models.Notification{
		EventType: NotifyCheckFailed, Recipient: "operator@example.test",
		Subject: "subject", Body: "body", DedupeKey: "incident:1:user:1",
	}
	if err := st.EnqueueNotification(ctx, n); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueNotification(ctx, n); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := st.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("deduplicated rows = %d, err=%v", rows, err)
	}

	claimed, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
	if err != nil || len(claimed) != 1 || claimed[0].LeaseToken == nil || claimed[0].Attempts != 1 {
		t.Fatalf("first claim = %#v, err=%v", claimed, err)
	}
	firstToken := *claimed[0].LeaseToken
	if again, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("claim during live lease = %#v, err=%v", again, err)
	}
	if err := st.MarkNotificationSentLeased(ctx, claimed[0].ID, uuid.NewString()); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong lease token error = %v, want ErrConflict", err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE notifications SET lease_until=now()-interval '1 second' WHERE id=$1`, claimed[0].ID); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
	if err != nil || len(reclaimed) != 1 || reclaimed[0].LeaseToken == nil ||
		reclaimed[0].Attempts != 2 || *reclaimed[0].LeaseToken == firstToken {
		t.Fatalf("reclaimed = %#v, err=%v", reclaimed, err)
	}
	if err := st.MarkNotificationSentLeased(ctx, reclaimed[0].ID, firstToken); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired worker fencing error = %v, want ErrConflict", err)
	}
	currentToken := *reclaimed[0].LeaseToken
	if err := st.MarkNotificationFailedLeased(ctx, reclaimed[0].ID, currentToken,
		reclaimed[0].Attempts, 5, "temporary", time.Minute); err != nil {
		t.Fatal(err)
	}
	if again, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("claim during backoff = %#v, err=%v", again, err)
	}
	if _, err := st.pool.Exec(ctx, `UPDATE notifications SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, reclaimed[0].ID); err != nil {
		t.Fatal(err)
	}
	finalClaim, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
	if err != nil || len(finalClaim) != 1 || finalClaim[0].LeaseToken == nil || finalClaim[0].Attempts != 3 {
		t.Fatalf("final claim = %#v, err=%v", finalClaim, err)
	}
	if err := st.MarkNotificationSentLeased(ctx, finalClaim[0].ID, *finalClaim[0].LeaseToken); err != nil {
		t.Fatal(err)
	}
	stats, err := st.OutboxStats(ctx)
	if err != nil || stats.Pending != 0 || stats.Processing != 0 || stats.Failed != 0 {
		t.Fatalf("outbox stats = %#v, err=%v", stats, err)
	}
}

func TestAlertNotificationSequenceCannotClearNewerTransition(t *testing.T) {
	f := newAlertReliabilityFixture(t, "notification-sequence")
	ctx := context.Background()
	base := time.Now().UTC()
	opened, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityWarning, base))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, opened.Alert.ID,
		opened.Alert.NotificationSeq-1, base); err != nil {
		t.Fatal(err)
	}
	stillPending, err := f.store.GetAlert(ctx, opened.Alert.ID)
	if err != nil || !stillPending.NotificationPending {
		t.Fatalf("stale completion cleared pending transition: %#v err=%v", stillPending, err)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, opened.Alert.ID,
		opened.Alert.NotificationSeq, base); err != nil {
		t.Fatal(err)
	}
	escalated, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityError, base.Add(time.Minute)))
	if err != nil || !escalated.Alert.NotificationPending || escalated.Alert.NotificationSeq <= opened.Alert.NotificationSeq {
		t.Fatalf("escalated transition = %#v, err=%v", escalated, err)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, opened.Alert.ID,
		opened.Alert.NotificationSeq, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	stillPending, err = f.store.GetAlert(ctx, opened.Alert.ID)
	if err != nil || !stillPending.NotificationPending ||
		stillPending.NotificationSeq != escalated.Alert.NotificationSeq {
		t.Fatalf("older enqueue completion hid escalation: %#v err=%v", stillPending, err)
	}
}

func TestAlertRecipientsUseIncidentScopeSnapshot(t *testing.T) {
	f := newAlertReliabilityFixture(t, "recipient-snapshot")
	ctx := context.Background()
	otherClient, err := f.store.CreateClient(ctx, "recipient other client")
	if err != nil {
		t.Fatal(err)
	}
	otherSite, err := f.store.CreateSite(ctx, otherClient.ID, "recipient other site")
	if err != nil {
		t.Fatal(err)
	}
	makeUser := func(name, email, siteID string) *models.User {
		t.Helper()
		user, err := f.store.CreateUserWithScopes(ctx, name, "hash", models.RoleTech,
			[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: siteID}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.UpdateUserEmail(ctx, user.ID, email); err != nil {
			t.Fatal(err)
		}
		return user
	}
	original := makeUser("recipient-original", "original@example.test", f.site.ID)
	makeUser("recipient-new", "new@example.test", otherSite.ID)
	opened, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityError, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetAgentSite(ctx, f.agent.ID, &otherSite.ID); err != nil {
		t.Fatal(err)
	}

	assertRecipients := func() {
		t.Helper()
		recipients, err := f.store.RecipientsForAlertEvent(ctx, opened.Alert.ID,
			NotifyCheckFailed, DefaultSubscribed(NotifyCheckFailed))
		if err != nil {
			t.Fatal(err)
		}
		if len(recipients) != 1 || recipients[0].UserID != original.ID ||
			recipients[0].Email != "original@example.test" {
			t.Fatalf("snapshot recipients = %#v, want only original site", recipients)
		}
	}
	assertRecipients()
	if err := f.store.DeleteAgent(ctx, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	assertRecipients()
	resolved, err := f.store.ResolveRetiredAgentAlerts(ctx, time.Now().UTC())
	if err != nil || resolved != 1 {
		t.Fatalf("retired-device reconciliation resolved=%d err=%v", resolved, err)
	}
	closed, err := f.store.GetAlert(ctx, opened.Alert.ID)
	if err != nil || closed.Status != models.AlertResolved || closed.ResolutionKind != "retired" {
		t.Fatalf("retired check incident = %#v, err=%v", closed, err)
	}
}

func TestCheckResultScopeIsAppliedBeforeLimit(t *testing.T) {
	f := newAlertReliabilityFixture(t, "result-scope-limit")
	ctx := context.Background()
	otherClient, err := f.store.CreateClient(ctx, "history other client")
	if err != nil {
		t.Fatal(err)
	}
	otherSite, err := f.store.CreateSite(ctx, otherClient.ID, "history other site")
	if err != nil {
		t.Fatal(err)
	}
	otherAgent, err := f.store.CreateAgent(ctx, "history-other", "linux", "amd64", "test", "hash", &otherSite.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	user, err := f.store.CreateUserWithScopes(ctx, "history-scoped", "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: f.site.ID}})
	if err != nil {
		t.Fatal(err)
	}
	older, newer := time.Now().UTC().Add(-time.Minute), time.Now().UTC()
	if _, err := f.store.pool.Exec(ctx, `INSERT INTO check_results
		(id, check_id, agent_id, status, output, ran_at) VALUES
		($1,$3,$4,'passing','visible',$6),
		($2,$3,$5,'failing','outside scope',$7)`,
		uuid.NewString(), uuid.NewString(), f.check.ID, f.agent.ID, otherAgent.ID, older, newer); err != nil {
		t.Fatal(err)
	}
	results, err := f.store.ListCheckResultsForUser(ctx, f.check.ID, "", user.ID, true, 1)
	if err != nil || len(results) != 1 || results[0].AgentID != f.agent.ID || results[0].Output != "visible" {
		t.Fatalf("scoped limited results = %#v, err=%v", results, err)
	}
}

func TestMonitoringRetentionPurgesOnlyTerminalExpiredData(t *testing.T) {
	f := newAlertReliabilityFixture(t, "retention")
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -60)
	recent := now.AddDate(0, 0, -1)

	insertAlert := func(status string, resolvedAt *time.Time, pending bool) string {
		t.Helper()
		id := uuid.NewString()
		kind := ""
		seq := int64(0)
		if pending {
			kind, seq = models.AlertEventResolved, 1
		}
		_, err := f.store.pool.Exec(ctx, `INSERT INTO alerts (
			id, source_type, condition_key, severity, message, status,
			created_at, first_seen_at, last_seen_at, updated_at, resolved_at,
			notification_pending, notification_kind, notification_seq
		) VALUES ($1,'system',$2,'warning','retention test',$3,$4,$4,$4,$4,$5,$6,$7,$8)`,
			id, id, status, old, resolvedAt, pending, kind, seq)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	activeAlertID := insertAlert(models.AlertNew, nil, false)
	expiredResolvedID := insertAlert(models.AlertResolved, &old, false)
	pendingResolvedID := insertAlert(models.AlertResolved, &old, true)

	oldExecutionID := uuid.NewString()
	recentExecutionID := uuid.NewString()
	pendingExecutionID := uuid.NewString()
	if _, err := f.store.pool.Exec(ctx, `INSERT INTO check_executions
		(id, check_id, agent_id, status, dispatched_at, deadline_at, completed_at)
		VALUES
		($1,$4,$5,'completed',$6,$6,$6),
		($2,$4,$5,'completed',$7,$7,$7),
		($3,$4,$5,'pending',$7,$8,NULL)`,
		oldExecutionID, recentExecutionID, pendingExecutionID, f.check.ID, f.agent.ID,
		old, recent, recent.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	oldResultID, recentResultID := uuid.NewString(), uuid.NewString()
	if _, err := f.store.pool.Exec(ctx, `INSERT INTO check_results
		(id, check_id, agent_id, execution_id, status, output, ran_at)
		VALUES ($1,$3,$4,$5,'passing','old',$7),
		       ($2,$3,$4,$6,'passing','recent',$8)`,
		oldResultID, recentResultID, f.check.ID, f.agent.ID,
		oldExecutionID, recentExecutionID, old, recent); err != nil {
		t.Fatal(err)
	}

	notificationIDs := map[string]string{}
	for _, status := range []string{"sent", "failed", "pending", "processing"} {
		id := uuid.NewString()
		notificationIDs[status] = id
		var sentAt *time.Time
		if status == "sent" {
			sentAt = &old
		}
		var leaseToken *string
		var leaseUntil *time.Time
		if status == "processing" {
			token := uuid.NewString()
			leaseToken, leaseUntil = &token, &recent
		}
		if _, err := f.store.pool.Exec(ctx, `INSERT INTO notifications (
			id, event_type, recipient, subject, body, status, created_at, updated_at,
			sent_at, lease_token, lease_until
		) VALUES ($1,'check_failed','operator@example.test','s','b',$2,$3,$3,$4,$5,$6)`,
			id, status, old, sentAt, leaseToken, leaseUntil); err != nil {
			t.Fatal(err)
		}
	}

	oldSuppressionID, activeSuppressionID := uuid.NewString(), uuid.NewString()
	if _, err := f.store.pool.Exec(ctx, `INSERT INTO alert_suppressions
		(id, kind, scope_type, scope_id, starts_at, ends_at, created_by)
		VALUES ($1,'silence','agent',$3,$4,$5,'test'),
		       ($2,'maintenance','agent',$3,$6,$7,'test')`,
		oldSuppressionID, activeSuppressionID, f.agent.ID, old.Add(-time.Hour), old,
		now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	purged, err := f.store.PurgeMonitoringData(ctx, now, 30, 30, 30)
	if err != nil {
		t.Fatal(err)
	}
	if purged != (RetentionResult{Alerts: 1, Results: 1, Executions: 1, Notifications: 2, Suppressions: 1}) {
		t.Fatalf("retention result = %#v", purged)
	}
	assertExists := func(table, id string, want bool) {
		t.Helper()
		var exists bool
		query := `SELECT EXISTS (SELECT 1 FROM ` + table + ` WHERE id=$1)`
		if err := f.store.pool.QueryRow(ctx, query, id).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Errorf("%s %s exists=%v, want %v", table, id, exists, want)
		}
	}
	assertExists("alerts", expiredResolvedID, false)
	assertExists("alerts", activeAlertID, true)
	assertExists("alerts", pendingResolvedID, true)
	assertExists("check_results", oldResultID, false)
	assertExists("check_results", recentResultID, true)
	assertExists("check_executions", oldExecutionID, false)
	assertExists("check_executions", recentExecutionID, true)
	assertExists("check_executions", pendingExecutionID, true)
	assertExists("notifications", notificationIDs["sent"], false)
	assertExists("notifications", notificationIDs["failed"], false)
	assertExists("notifications", notificationIDs["pending"], true)
	assertExists("notifications", notificationIDs["processing"], true)
	assertExists("alert_suppressions", oldSuppressionID, false)
	assertExists("alert_suppressions", activeSuppressionID, true)
}

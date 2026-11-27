package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func deliveryUser(t *testing.T, f alertReliabilityFixture, name string, scopes []models.UserScope) *models.User {
	t.Helper()
	ctx := context.Background()
	u, err := f.store.CreateUserWithScopes(ctx, name, "hash", models.RoleTech, scopes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateUserEmail(ctx, u.ID, name+"@example.test"); err != nil {
		t.Fatal(err)
	}
	return u
}
func deliveryOpen(t *testing.T, f alertReliabilityFixture) *models.Alert {
	t.Helper()
	o := observationForFixture(f, models.SeverityWarning, time.Now().UTC())
	o.RuleID = &f.check.ID
	a, err := f.store.ObserveAlert(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return &a.Alert
}
func TestDeliveryFanoutAtomicSnapshotAndRetryIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "fanout")
	ctx := context.Background()
	deliveryUser(t, f, "one", nil)
	deliveryUser(t, f, "two", nil)
	a := deliveryOpen(t, f)
	// Failure after one successful insert must roll back the whole fanout AND leave pending.
	_, err := f.store.pool.Exec(ctx, `CREATE FUNCTION fail_second_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF (SELECT count(*) FROM notifications)>0 THEN RAISE EXCEPTION 'injected second insertion failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_second_delivery BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION fail_second_delivery()`)
	if err != nil {
		t.Fatal(err)
	}
	queue := func(a *models.Alert) error {
		return f.store.QueueAlertNotifications(ctx, a, NotifyCheckFailed, "subject", a.Message, false, false)
	}
	if err = queue(a); err == nil {
		t.Fatal("expected fanout failure")
	}
	var n int
	f.store.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n)
	current, _ := f.store.GetAlert(ctx, a.ID)
	if n != 0 || !current.NotificationPending {
		t.Fatalf("partial fanout: n=%d pending=%v", n, current.NotificationPending)
	}
	if _, err = f.store.pool.Exec(ctx, `DROP TRIGGER fail_second_delivery ON notifications`); err != nil {
		t.Fatal(err)
	}
	// Same seq/version but changed message must refuse the old rendered content.
	obs := observationForFixture(f, models.SeverityWarning, time.Now().Add(time.Second))
	obs.RuleID = &f.check.ID
	obs.Message = "new content"
	changed, err := f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Alert.Version != a.Version || changed.Alert.NotificationSeq != a.NotificationSeq {
		t.Fatal("fixture must change content without version/seq")
	}
	if err = queue(a); !errors.Is(err, ErrConflict) {
		t.Fatalf("old message accepted: %v", err)
	}
	// A rule switch can leave seq intact; it too must refuse the old model.
	old := changed.Alert
	obs.RuleID = nil
	obs.ObservedAt = obs.ObservedAt.Add(time.Second)
	obs.ObservationID += "next"
	changed, err = f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Alert.NotificationSeq != old.NotificationSeq {
		t.Fatal("fixture seq changed")
	}
	if err = queue(&old); !errors.Is(err, ErrConflict) {
		t.Fatalf("old rule accepted: %v", err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); failures <- queue(&changed.Alert) }()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.store.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n)
	current, _ = f.store.GetAlert(ctx, a.ID)
	if n != 2 || current.NotificationPending {
		t.Fatalf("fanout retry n=%d pending=%v", n, current.NotificationPending)
	}
	var contextRaw []byte
	if err = f.store.pool.QueryRow(ctx, `SELECT delivery_context FROM notifications LIMIT 1`).Scan(&contextRaw); err != nil {
		t.Fatal(err)
	}
	d, err := decodeDeliveryContext(contextRaw)
	if err != nil || d.RuleID != "" || d.SiteID != f.site.ID || d.NotificationSeq != a.NotificationSeq {
		t.Fatalf("snapshot=%+v err=%v", d, err)
	}
}

func TestDeliveryAuthorizationUsesFrozenScopeAndRuleIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "frozen")
	ctx := context.Background()
	u := deliveryUser(t, f, "site-user", []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: f.site.ID}})
	if err := f.store.SetNotificationRuleFilter(ctx, u.ID, NotificationRuleFilter{Mode: "selected", CheckIDs: []string{f.check.ID}}); err != nil {
		t.Fatal(err)
	}
	a := deliveryOpen(t, f)
	if err := f.store.QueueAlertNotifications(ctx, a, NotifyCheckFailed, "s", "b", false, false); err != nil {
		t.Fatal(err)
	}
	queued, err := f.store.ClaimPendingNotifications(ctx, 10)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queue=%v err=%v", queued, err)
	}
	n := queued[0]
	otherClient, _ := f.store.CreateClient(ctx, "other")
	otherSite, _ := f.store.CreateSite(ctx, otherClient.ID, "other")
	// Mutable live location and rule may change, while queued provenance must stay fixed.
	if err = f.store.SetAgentSite(ctx, f.agent.ID, &otherSite.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(ctx, `UPDATE alerts SET rule_id=NULL WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	allowed, _, err := f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || !allowed {
		t.Fatalf("frozen context denied: %v %v", allowed, err)
	}
	if err = f.store.ReplaceUserScopes(ctx, u.ID, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: otherSite.ID}}); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || allowed {
		t.Fatalf("new site's rights authorize old content: %v %v", allowed, err)
	}
	if err = f.store.ReplaceUserScopes(ctx, u.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.store.SetNotificationRuleFilter(ctx, u.ID, NotificationRuleFilter{Mode: "selected", IncludeDefaults: true}); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || allowed {
		t.Fatalf("new default rule misused for queued check: %v %v", allowed, err)
	}
	// Legacy incident has historical scope but no provable rule; selected cannot pass.
	n.DeliveryContext = nil
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || allowed {
		t.Fatalf("legacy guessed rule: %v %v", allowed, err)
	}
	if err = f.store.SetNotificationRuleFilter(ctx, u.ID, NotificationRuleFilter{Mode: "all"}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.ReplaceUserScopes(ctx, u.ID, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: f.site.ID}}); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || !allowed {
		t.Fatalf("provable legacy scope denied: %v %v", allowed, err)
	}
	n.AlertID = nil
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || allowed {
		t.Fatalf("legacy nonincident guessed live scope: %v %v", allowed, err)
	}
	if err = f.store.ReplaceUserScopes(ctx, u.ID, nil); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || !allowed {
		t.Fatalf("unrestricted legacy denied: %v %v", allowed, err)
	}
	n.DeliveryContext = json.RawMessage(`{"version":99}`)
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || allowed {
		t.Fatalf("unsupported provenance allowed: %v %v", allowed, err)
	}
	n = queued[0]
	// Outbox provenance survives deleted live rows (foreign keys become NULL).
	if _, err = f.store.pool.Exec(ctx, `DELETE FROM alerts WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.pool.Exec(ctx, `DELETE FROM agents WHERE id=$1`, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	n.AlertID = nil
	n.AgentID = nil
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err != nil || !allowed {
		t.Fatalf("frozen context after deletion denied: %v %v", allowed, err)
	}
	// Database failures are not deterministic denials and cannot become sends.
	if _, err = f.store.pool.Exec(ctx, `ALTER TABLE notification_subscriptions RENAME TO unavailable_subscriptions`); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = f.store.NotificationDeliveryAllowed(ctx, &n, false)
	if err == nil || allowed {
		t.Fatal("database error must fail closed and remain retryable")
	}
}

func TestDeliveryNonincidentFilterAndDisabledTransportIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "nonincident")
	ctx := context.Background()
	u := deliveryUser(t, f, "operator", nil)
	if err := f.store.SetNotificationRuleFilter(ctx, u.ID, NotificationRuleFilter{Mode: "selected"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetNotificationSubscription(ctx, u.ID, NotifyJobFailed, true); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{NotifyJobFailed, NotifyAIReleaseRevoked} {
		if err := f.store.QueueAgentNotifications(ctx, f.agent.ID, event, "s", "b", event, false); err != nil {
			t.Fatal(err)
		}
	}
	queued, err := f.store.ClaimPendingNotifications(ctx, 10)
	if err != nil || len(queued) != 2 {
		t.Fatalf("queue=%v err=%v", queued, err)
	}
	for _, n := range queued {
		ok, _, err := f.store.NotificationDeliveryAllowed(ctx, &n, false)
		if err != nil || !ok {
			t.Fatalf("nonincident rule filter regression: %v %v", ok, err)
		}
	}
	a := deliveryOpen(t, f)
	if err := f.store.QueueAlertNotifications(ctx, a, "", "", "", false, true); err != nil {
		t.Fatal(err)
	}
	current, err := f.store.GetAlert(ctx, a.ID)
	if err != nil || current.NotificationPending {
		t.Fatalf("disabled transition not consumed: %v", err)
	}
	var count int
	f.store.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count)
	if count != 2 {
		t.Fatalf("disabled transport banked stale mail: %d", count)
	}
}

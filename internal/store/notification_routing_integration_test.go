package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/models"
)

func routeSave(t *testing.T, f alertReliabilityFixture, scope, id string, version int64, mode string, users ...string) *models.NotificationRoutingView {
	t.Helper()
	v, err := f.store.UpdateNotificationRouting(context.Background(), scope, id, "operator", version, models.NotificationRouteUpdate{Mode: mode, UserIDs: users})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestNotificationRoutingInheritanceAndEligibilityIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "routing")
	ctx := context.Background()
	local := deliveryUser(t, f, "local", []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: f.site.ID}})
	global := deliveryUser(t, f, "global", nil)
	emptySite, err := f.store.CreateSite(ctx, f.client.ID, "empty")
	if err != nil {
		t.Fatal(err)
	}
	empty := deliveryUser(t, f, "empty-site", []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: emptySite.ID}})
	group, err := f.store.CreateGroup(ctx, "empty-group", &emptySite.ID)
	if err != nil {
		t.Fatal(err)
	}
	groupUser := deliveryUser(t, f, "empty-group", []models.UserScope{{ScopeType: models.ScopeGroup, ScopeID: group.ID}})
	agentUser := deliveryUser(t, f, "agent-only", []models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: f.agent.ID}})
	options, _, err := f.store.NotificationRouteRecipientOptions(ctx, "client", f.client.ID, "")
	if err != nil || len(options) != 5 {
		t.Fatalf("structural options=%+v err=%v", options, err)
	}
	initial, err := f.store.GetNotificationRouting(ctx, "site", f.site.ID)
	if err != nil || initial.Local.Version != 0 || initial.Effective.Mode != "automatic" {
		t.Fatalf("default=%+v err=%v", initial, err)
	}
	routeSave(t, f, "client", f.client.ID, 0, "selected", local.ID, global.ID, empty.ID, groupUser.ID, agentUser.ID, local.ID)
	inherited, err := f.store.GetNotificationRouting(ctx, "site", f.site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inherited.Effective.ScopeType != "client" || len(inherited.Effective.Recipients) != 3 || inherited.Effective.UnavailableCount != 2 {
		t.Fatalf("inherited privacy=%+v", inherited)
	}
	raw, _ := json.Marshal(inherited)
	if strings.Contains(string(raw), empty.ID) || strings.Contains(string(raw), "empty-site") || strings.Contains(string(raw), groupUser.ID) {
		t.Fatalf("sibling identities leaked: %s", raw)
	}
	selected := routeSave(t, f, "site", f.site.ID, 0, "selected", global.ID)
	if selected.Effective.ScopeType != "site" || len(selected.Effective.Recipients) != 1 || selected.Effective.Recipients[0].ID != global.ID {
		t.Fatalf("site replacement=%+v", selected)
	}
	inherited = routeSave(t, f, "site", f.site.ID, 1, "inherit")
	if inherited.Local.Version != 2 || inherited.Effective.ScopeType != "client" {
		t.Fatalf("return inherit=%+v", inherited)
	}
	if _, err = f.store.UpdateNotificationRouting(ctx, "site", f.site.ID, "operator", 0, models.NotificationRouteUpdate{Mode: "selected", UserIDs: []string{local.ID}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("ABA accepted: %v", err)
	}
	if _, err = f.store.UpdateNotificationRouting(ctx, "site", f.site.ID, "operator", 2, models.NotificationRouteUpdate{Mode: "selected", UserIDs: []string{empty.ID}}); !errors.Is(err, ErrNotificationRecipients) {
		t.Fatalf("outscope accepted: %v", err)
	}
	if _, err = f.store.SetUserDisabled(ctx, local.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateNotificationRouting(ctx, "site", f.site.ID, "operator", 2, models.NotificationRouteUpdate{Mode: "selected", UserIDs: []string{local.ID}}); !errors.Is(err, ErrNotificationRecipients) {
		t.Fatalf("disabled accepted: %v", err)
	}
	// An account limited to a single device remains only a candidate for that device.
	routeSave(t, f, "site", f.site.ID, 2, "selected", agentUser.ID)
	a := deliveryOpen(t, f)
	if err = f.store.QueueAlertNotifications(ctx, a, NotifyCheckFailed, "s", "b", false, false); err != nil {
		t.Fatal(err)
	}
	queued, err := f.store.ClaimPendingNotifications(ctx, 20)
	if err != nil || len(queued) != 1 || *queued[0].UserID != agentUser.ID {
		t.Fatalf("agent intersection=%+v err=%v", queued, err)
	}
}

func TestNotificationRoutingIncidentSnapshotLifecycleIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "route-lifecycle")
	ctx := context.Background()
	aUser := deliveryUser(t, f, "a", nil)
	bUser := deliveryUser(t, f, "b", nil)
	routeSave(t, f, "client", f.client.ID, 0, "selected", aUser.ID)
	a := deliveryOpen(t, f)
	if a.NotificationRouteSnapshot.Mode != "selected" || a.NotificationRouteSnapshot.UserIDs[0] != aUser.ID {
		t.Fatalf("opening route=%+v", a.NotificationRouteSnapshot)
	}
	raw, _ := json.Marshal(a)
	if strings.Contains(string(raw), aUser.ID) {
		t.Fatal("private route leaked into alert JSON")
	}
	routeSave(t, f, "client", f.client.ID, 1, "selected", bUser.ID)
	if err := f.store.QueueAlertNotifications(ctx, a, NotifyCheckFailed, "open", "body", false, false); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.store.ResolveAlertSource(ctx, a.SourceType, a.SourceID, &f.agent.ID, "system", "automatic", "healthy", time.Now())
	if err != nil || len(resolved) != 1 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	if !resolved[0].NotificationPending {
		t.Fatal("automatic recovery must be pending")
	}
	if err = f.store.QueueAlertNotifications(ctx, &resolved[0], NotifyCheckRecovered, "resolved", "body", false, false); err != nil {
		t.Fatal(err)
	}
	obs := observationForFixture(f, models.SeverityWarning, time.Now().Add(time.Second))
	obs.RuleID = &f.check.ID
	obs.Cooldown = 0
	reopened, err := f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Alert.ID == a.ID || reopened.Alert.NotificationRouteSnapshot.UserIDs[0] != bUser.ID {
		t.Fatalf("reopened route=%+v", reopened)
	}
	if err = f.store.QueueAlertNotifications(ctx, &reopened.Alert, NotifyCheckFailed, "reopened", "body", false, false); err != nil {
		t.Fatal(err)
	}
	queued, err := f.store.ClaimPendingNotifications(ctx, 20)
	if err != nil || len(queued) != 3 {
		t.Fatalf("queued=%+v err=%v", queued, err)
	}
	for _, n := range queued {
		want := aUser.ID
		if n.Subject == "reopened" {
			want = bUser.ID
		}
		if *n.UserID != want {
			t.Fatalf("wrong historical route: %+v", n)
		}
		ok, _, err := f.store.NotificationDeliveryAllowed(ctx, &n, false)
		if err != nil || !ok {
			t.Fatalf("delivery=%v %v", ok, err)
		}
	}
	// Deleted selected recipients do not turn an explicit route into automatic.
	if _, err = f.store.pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, bUser.ID); err != nil {
		t.Fatal(err)
	}
	effective, _, err := effectiveNotificationRoute(ctx, f.store.pool, f.client.ID, f.site.ID)
	if err != nil || effective.Mode != "selected" || len(effective.UserIDs) != 0 {
		t.Fatalf("deleted recipient fallback: %+v %v", effective, err)
	}
}

func TestNotificationRoutingMalformedSnapshotAndMaximumIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "route-bounds")
	ctx := context.Background()
	a := deliveryOpen(t, f)
	if _, err := f.store.pool.Exec(ctx, `UPDATE alerts SET notification_route_snapshot='{"mode":"automatic","unexpected_restriction":["secret"]}' WHERE id=$1`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.QueueAlertNotifications(ctx, a, NotifyCheckFailed, "s", "b", false, false); err == nil {
		t.Fatal("unknown persisted route field silently became automatic")
	}
	var pending bool
	var count int
	f.store.pool.QueryRow(ctx, `SELECT notification_pending FROM alerts WHERE id=$1`, a.ID).Scan(&pending)
	f.store.pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&count)
	if !pending || count != 0 {
		t.Fatalf("malformed route consumed pending: pending=%v rows=%d", pending, count)
	}
	ids := make([]string, models.MaxNotificationRouteRecipients)
	for i := range ids {
		ids[i] = uuid.NewString()
	}
	route := models.NotificationRouteSnapshot{Mode: "selected", ScopeType: "client", ScopeID: f.client.ID, Version: 1, UserIDs: ids}
	context := alertDeliveryContext(a, NotifyCheckFailed)
	context.Route = route
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeDeliveryContext(raw); err != nil {
		t.Fatalf("maximum snapshot rejected: %d bytes %v", len(raw), err)
	}
	if len(raw) >= 16384 {
		t.Fatalf("snapshot exceeds decoder: %d", len(raw))
	}
	for _, update := range []models.NotificationRouteUpdate{{Mode: "selected"}, {Mode: "silent"}, {Mode: "inherit", UserIDs: ids[:1]}, {Mode: "selected", UserIDs: append(ids, uuid.NewString())}, {Mode: "selected", UserIDs: []string{"bad"}}} {
		if err := update.Validate(); err == nil {
			t.Fatalf("bad config accepted %+v", update)
		}
	}
}

func TestNotificationRoutingConcurrentSaveAuditAndRollbackIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "route-audit")
	ctx := context.Background()
	u := deliveryUser(t, f, "oncall", nil)
	lock, err := f.store.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(ctx)
	if _, err = lock.Exec(ctx, `SELECT id FROM clients WHERE id=$1 FOR UPDATE`, f.client.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.store.UpdateNotificationRouting(ctx, "client", f.client.ID, "operator", 0, models.NotificationRouteUpdate{Mode: "selected", UserIDs: []string{u.ID}})
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		err = f.store.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT name FROM clients%')`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("route did not block on client lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = f.store.AddAudit(ctx, "other", "ordinary.update", f.client.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = lock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.store.UpdateNotificationRouting(ctx, "client", f.client.ID, "operator", 1, models.NotificationRouteUpdate{Mode: "inherit"})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	conflicts := 0
	for err := range results {
		if errors.Is(err, ErrConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatalf("concurrent saves conflicts=%d", conflicts)
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if err := f.store.AddAudit(ctx, "other", "ordinary.update", f.client.ID, nil); err != nil {
					t.Error(err)
				}
			} else {
				v, err := f.store.GetNotificationRouting(ctx, "client", f.client.ID)
				if err != nil {
					t.Error(err)
					return
				}
				_, err = f.store.UpdateNotificationRouting(ctx, "client", f.client.ID, "operator", v.Local.Version, models.NotificationRouteUpdate{Mode: "inherit"})
				if err != nil && !errors.Is(err, ErrConflict) {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	broken, err := f.store.VerifyAuditChain(ctx)
	if err != nil || broken != nil {
		t.Fatalf("audit chain=%v err=%v", broken, err)
	}
	before, err := f.store.GetNotificationRouting(ctx, "client", f.client.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.store.pool.Exec(ctx, `CREATE FUNCTION reject_route_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='notification.routing.update' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_route_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_route_audit()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.UpdateNotificationRouting(ctx, "client", f.client.ID, "operator", before.Local.Version, models.NotificationRouteUpdate{Mode: "selected", UserIDs: []string{u.ID}}); err == nil {
		t.Fatal("audit failure did not roll back route")
	}
	after, err := f.store.GetNotificationRouting(ctx, "client", f.client.ID)
	if err != nil || after.Local.Version != before.Local.Version || after.Local.Mode != before.Local.Mode {
		t.Fatalf("rollback failed %+v %v", after, err)
	}
	if _, err = f.store.pool.Exec(ctx, `UPDATE audit_log SET actor='edited'`); err == nil {
		t.Fatal("append-only audit protection lost")
	}
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/p1ngu/rmm/internal/models"
)

type alertReliabilityFixture struct {
	store  *Store
	client *models.Client
	site   *models.Site
	group  *models.DeviceGroup
	agent  *models.Agent
	check  *models.Check
}

func newAlertReliabilityFixture(t *testing.T, prefix string) alertReliabilityFixture {
	t.Helper()
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := newMigrationTestStore(t, databaseURL)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	client, err := st.CreateClient(ctx, prefix+" client")
	if err != nil {
		t.Fatal(err)
	}
	site, err := st.CreateSite(ctx, client.ID, prefix+" site")
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateGroup(ctx, prefix+" group", &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := st.CreateAgent(ctx, prefix+"-host", "linux", "amd64", "test", "hash", &site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAgentGroup(ctx, agent.ID, &group.ID); err != nil {
		t.Fatal(err)
	}
	check, err := st.CreateCheck(ctx, &models.Check{
		AgentID: &agent.ID, Type: models.CheckScript, Name: prefix + " check",
		Config:      json.RawMessage(`{"shell":"bash","script":"exit 1"}`),
		IntervalSec: 30, FailureCount: 2, RecoveryCount: 2,
		CooldownSec: 300, TimeoutSec: 30, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return alertReliabilityFixture{store: st, client: client, site: site, group: group, agent: agent, check: check}
}

func observationForFixture(f alertReliabilityFixture, severity string, at time.Time) AlertObservation {
	value, threshold := 95.0, 90.0
	return AlertObservation{
		AgentID: &f.agent.ID, SourceType: models.AlertSourceCheck, SourceID: &f.check.ID,
		ConditionKey: "health", Severity: severity, Message: "health threshold exceeded",
		ObservedValue: &value, Threshold: &threshold, Output: "sample output",
		Cooldown: 5 * time.Minute, ObservedAt: at,
	}
}

func eventTypes(events []models.AlertEvent) map[string]int {
	out := map[string]int{}
	for i := range events {
		out[events[i].EventType]++
	}
	return out
}

func TestAlertLifecycleEscalationConcurrencyAndSnapshotScope(t *testing.T) {
	f := newAlertReliabilityFixture(t, "lifecycle")
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)

	opened, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityWarning, base))
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Opened || !opened.Notify || opened.Alert.Status != models.AlertNew {
		t.Fatalf("opening transition = %#v", opened)
	}
	if opened.Alert.AgentSnapshotID == nil || *opened.Alert.AgentSnapshotID != f.agent.ID ||
		opened.Alert.AgentHostname != f.agent.Hostname || opened.Alert.SiteID == nil ||
		*opened.Alert.SiteID != f.site.ID || opened.Alert.ClientID == nil ||
		*opened.Alert.ClientID != f.client.ID || opened.Alert.GroupID == nil ||
		*opened.Alert.GroupID != f.group.ID {
		t.Fatalf("alert snapshot = %#v", opened.Alert)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, opened.Alert.ID, opened.Alert.NotificationSeq, base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	retry, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityWarning, base))
	if err != nil {
		t.Fatal(err)
	}
	if retry.Alert.OccurrenceCount != 1 || retry.Notify {
		t.Fatalf("same-state reconciliation retry = %#v, want no synthetic occurrence", retry)
	}

	version := opened.Alert.Version
	acked, err := f.store.TransitionAlert(ctx, opened.Alert.ID, AlertAction{
		Action: "ack", Actor: "operator", ExpectedVersion: &version,
	})
	if err != nil || acked.Status != models.AlertAcked || acked.AckedBy != "operator" {
		t.Fatalf("acknowledge = %#v, err=%v", acked, err)
	}
	if _, err := f.store.TransitionAlert(ctx, opened.Alert.ID, AlertAction{
		Action: "resolve", Actor: "stale-client", ExpectedVersion: &version,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version error = %v, want ErrConflict", err)
	}

	repeated, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityWarning, base.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Alert.ID != opened.Alert.ID || repeated.Alert.Status != models.AlertAcked ||
		repeated.Alert.OccurrenceCount != 2 || repeated.Notify {
		t.Fatalf("repeated warning = %#v", repeated)
	}
	escalated, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityError, base.Add(2*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if !escalated.Escalated || !escalated.Notify || escalated.Alert.Status != models.AlertNew ||
		escalated.Alert.AckedAt != nil || escalated.Alert.AckedBy != "" ||
		escalated.Alert.Severity != models.SeverityError {
		t.Fatalf("escalation = %#v", escalated)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, escalated.Alert.ID,
		escalated.Alert.NotificationSeq, base.Add(2*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.store.ResolveAlertSource(ctx, models.AlertSourceCheck, &f.check.ID,
		&f.agent.ID, "system", "automatic", "healthy again", base.Add(3*time.Minute))
	if err != nil || len(resolved) != 1 || resolved[0].Status != models.AlertResolved ||
		!resolved[0].NotificationPending || resolved[0].NotificationKind != models.AlertEventResolved {
		t.Fatalf("resolution = %#v, err=%v", resolved, err)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, resolved[0].ID,
		resolved[0].NotificationSeq, base.Add(3*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}

	reopened, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityWarning, base.Add(4*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Opened || !reopened.Reopened || reopened.Notify || reopened.Alert.ID == opened.Alert.ID ||
		reopened.Alert.ReopenedFrom == nil || *reopened.Alert.ReopenedFrom != opened.Alert.ID {
		t.Fatalf("cooldown reopen = %#v", reopened)
	}
	stillFailing, err := f.store.ObserveAlert(ctx,
		observationForFixture(f, models.SeverityWarning, base.Add(9*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if !stillFailing.Notify || !stillFailing.Alert.NotificationPending ||
		stillFailing.Alert.NotificationSeq != 1 {
		t.Fatalf("persistent cooldown reopen = %#v, want notification after cooldown", stillFailing)
	}
	events, err := f.store.ListAlertEvents(ctx, opened.Alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	for _, typ := range []string{models.AlertEventOpened, models.AlertEventOccurrence,
		models.AlertEventAcknowledged, models.AlertEventEscalated, models.AlertEventResolved} {
		if types[typ] == 0 {
			t.Errorf("timeline lacks %q: %#v", typ, types)
		}
	}

	inScope, err := f.store.CreateUserWithScopes(ctx, "lifecycle-in-scope", "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: f.site.ID}})
	if err != nil {
		t.Fatal(err)
	}
	otherClient, err := f.store.CreateClient(ctx, "other client")
	if err != nil {
		t.Fatal(err)
	}
	otherSite, err := f.store.CreateSite(ctx, otherClient.ID, "other site")
	if err != nil {
		t.Fatal(err)
	}
	outOfScope, err := f.store.CreateUserWithScopes(ctx, "lifecycle-out-of-scope", "hash", models.RoleTech,
		[]models.UserScope{{ScopeType: models.ScopeSite, ScopeID: otherSite.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.DeleteAgent(ctx, f.agent.ID); err != nil {
		t.Fatal(err)
	}
	preserved, err := f.store.GetAlert(ctx, reopened.Alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.AgentID != nil || preserved.AgentSnapshotID == nil || *preserved.AgentSnapshotID != f.agent.ID {
		t.Fatalf("snapshot after device deletion = %#v", preserved)
	}
	allowed, err := f.store.AlertAllowedForUser(ctx, inScope.ID, preserved.ID)
	if err != nil || !allowed {
		t.Fatalf("in-scope historical alert allowed=%v err=%v", allowed, err)
	}
	allowed, err = f.store.AlertAllowedForUser(ctx, outOfScope.ID, preserved.ID)
	if err != nil || allowed {
		t.Fatalf("out-of-scope historical alert allowed=%v err=%v", allowed, err)
	}
	rows, err := f.store.ListAlertsPage(ctx, AlertListOptions{Limit: 20, UserID: inScope.ID, Scoped: true})
	if err != nil || len(rows) != 2 {
		t.Fatalf("in-scope historical alerts = %#v, err=%v", rows, err)
	}
	rows, err = f.store.ListAlertsPage(ctx, AlertListOptions{Limit: 20, UserID: outOfScope.ID, Scoped: true})
	if err != nil || len(rows) != 0 {
		t.Fatalf("out-of-scope alerts = %#v, err=%v", rows, err)
	}
	if n, err := f.store.ResolveInapplicableCheckAlerts(ctx, base.Add(5*time.Minute)); err != nil || n != 1 {
		t.Fatalf("resolved orphan alerts = %d, err=%v", n, err)
	}
}

func TestObserveAlertSerializesNullSafeIdentity(t *testing.T) {
	f := newAlertReliabilityFixture(t, "concurrent")
	ctx := context.Background()
	const workers = 12
	start := make(chan struct{})
	results := make(chan *AlertTransition, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			tr, err := f.store.ObserveAlert(ctx, AlertObservation{
				SourceType: models.AlertSourceSystem, ConditionKey: "database",
				Severity: models.SeverityError, Message: "database unavailable",
			})
			if err != nil {
				errs <- err
				return
			}
			results <- tr
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	opened := 0
	for result := range results {
		ids[result.Alert.ID] = true
		if result.Opened {
			opened++
		}
	}
	if len(ids) != 1 || opened != 1 {
		t.Fatalf("ids=%v opened=%d, want one serialized incident", ids, opened)
	}
	alerts, err := f.store.ListAlertsPage(ctx, AlertListOptions{Limit: 20, SourceType: models.AlertSourceSystem})
	if err != nil || len(alerts) != 1 || alerts[0].OccurrenceCount != workers {
		t.Fatalf("alerts=%#v err=%v, want one incident with %d occurrences", alerts, err, workers)
	}
}

func TestDurableCheckExecutionDeduplicationAndStaleResults(t *testing.T) {
	f := newAlertReliabilityFixture(t, "execution")
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	e1, created, err := f.store.CreateCheckExecution(ctx, f.check.ID, f.agent.ID, base, 10*time.Second)
	if err != nil || !created {
		t.Fatalf("first execution = %#v created=%v err=%v", e1, created, err)
	}
	again, created, err := f.store.CreateCheckExecution(ctx, f.check.ID, f.agent.ID, base.Add(time.Second), 10*time.Second)
	if err != nil || created || again.ID != e1.ID {
		t.Fatalf("deduplicated execution = %#v created=%v err=%v", again, created, err)
	}
	firstResult := &models.CheckResult{
		CheckID: f.check.ID, AgentID: f.agent.ID, ExecutionID: &e1.ID,
		Status: models.CheckPassing, Output: "first",
	}
	state, disposition, err := f.store.RecordCheckResult(ctx, firstResult)
	if err != nil || disposition != CheckResultApplied || state.ConsecutiveSuccesses != 1 {
		t.Fatalf("first result state=%#v disposition=%q err=%v", state, disposition, err)
	}
	state, appliedTimeout, err := f.store.RecordCheckTimeout(ctx, e1, "racing timeout")
	if err != nil || appliedTimeout || state.LastOutput != "first" || state.Status != models.CheckPassing {
		t.Fatalf("timeout after completed result state=%#v applied=%v err=%v", state, appliedTimeout, err)
	}
	state, disposition, err = f.store.RecordCheckResult(ctx, &models.CheckResult{
		CheckID: f.check.ID, AgentID: f.agent.ID, ExecutionID: &e1.ID,
		Status: models.CheckFailing, Output: "duplicate must not overwrite",
	})
	if err != nil || disposition != CheckResultDuplicate || state.ConsecutiveSuccesses != 1 || state.LastOutput != "first" {
		t.Fatalf("duplicate state=%#v disposition=%q err=%v", state, disposition, err)
	}

	e2, created, err := f.store.CreateCheckExecution(ctx, f.check.ID, f.agent.ID, base.Add(20*time.Second), 10*time.Second)
	if err != nil || !created {
		t.Fatalf("second execution = %#v created=%v err=%v", e2, created, err)
	}
	expired, err := f.store.ExpireCheckExecutions(ctx, base.Add(31*time.Second), 10)
	if err != nil || len(expired) != 1 || expired[0].ID != e2.ID {
		t.Fatalf("expired executions = %#v, err=%v", expired, err)
	}
	state, appliedTimeout, err = f.store.RecordCheckTimeout(ctx, &expired[0], "deadline exceeded")
	if err != nil || !appliedTimeout || state.Status != models.CheckFailing || state.LastOutput != "deadline exceeded" {
		t.Fatalf("timeout state=%#v applied=%v err=%v", state, appliedTimeout, err)
	}
	timeoutStateAt := state.UpdatedAt
	e3, created, err := f.store.CreateCheckExecution(ctx, f.check.ID, f.agent.ID, base.Add(32*time.Second), 10*time.Second)
	if err != nil || !created {
		t.Fatalf("third execution = %#v created=%v err=%v", e3, created, err)
	}
	state, disposition, err = f.store.RecordCheckResult(ctx, &models.CheckResult{
		CheckID: f.check.ID, AgentID: f.agent.ID, ExecutionID: &e2.ID,
		Status: models.CheckFailing, Output: "late",
	})
	if err != nil || disposition != CheckResultStale || state.LastOutput != "deadline exceeded" {
		t.Fatalf("late result state=%#v disposition=%q err=%v", state, disposition, err)
	}
	state, disposition, err = f.store.RecordCheckResult(ctx, &models.CheckResult{
		CheckID: f.check.ID, AgentID: f.agent.ID, ExecutionID: &e3.ID,
		Status: models.CheckFailing, Output: "newest",
	})
	if err != nil || disposition != CheckResultApplied || state.LastOutput != "newest" || state.ConsecutiveFailures != 2 {
		t.Fatalf("newest result state=%#v disposition=%q err=%v", state, disposition, err)
	}
	if marked, err := f.store.TryMarkCheckStateReconciled(ctx, f.check.ID, f.agent.ID, timeoutStateAt); err != nil || marked {
		t.Fatalf("stale reconciliation completion marked=%v err=%v", marked, err)
	}
	if marked, err := f.store.TryMarkCheckStateReconciled(ctx, f.check.ID, f.agent.ID, state.UpdatedAt); err != nil || !marked {
		t.Fatalf("current reconciliation completion marked=%v err=%v", marked, err)
	}
	results, err := f.store.ListCheckResults(ctx, f.check.ID, f.agent.ID, 20)
	if err != nil || len(results) != 3 {
		t.Fatalf("result history = %#v, err=%v", results, err)
	}
}

func TestScheduledAndCanceledSuppressionsReconcileActiveAlert(t *testing.T) {
	f := newAlertReliabilityFixture(t, "suppression")
	ctx := context.Background()
	base := time.Now().UTC()
	opened, err := f.store.ObserveAlert(ctx, observationForFixture(f, models.SeverityError, base))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.MarkAlertNotificationQueued(ctx, opened.Alert.ID,
		opened.Alert.NotificationSeq, base); err != nil {
		t.Fatal(err)
	}
	scheduled := &models.AlertSuppression{
		Kind: models.SuppressionMaintenance, ScopeType: models.ScopeAgent, ScopeID: f.agent.ID,
		StartsAt: base.Add(time.Hour), EndsAt: base.Add(2 * time.Hour),
		Reason: "planned work", CreatedBy: "operator",
	}
	if err := f.store.CreateAlertSuppression(ctx, scheduled); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.GetAlert(ctx, opened.Alert.ID)
	if err != nil || before.Status != models.AlertNew {
		t.Fatalf("future suppression changed alert early: %#v err=%v", before, err)
	}
	applied, err := f.store.ApplyActiveAlertSuppressions(ctx, base.Add(90*time.Minute), 20)
	if err != nil || len(applied) != 1 || applied[0].Status != models.AlertSuppressed {
		t.Fatalf("scheduled suppression applied=%#v err=%v", applied, err)
	}
	if got, err := f.store.ReactivateExpiredSuppressedAlerts(ctx, base.Add(100*time.Minute), 20); err != nil || len(got) != 0 {
		t.Fatalf("reactivated before expiry=%#v err=%v", got, err)
	}
	reactivated, err := f.store.ReactivateExpiredSuppressedAlerts(ctx, base.Add(2*time.Hour+time.Second), 20)
	if err != nil || len(reactivated) != 1 || reactivated[0].Status != models.AlertNew ||
		!reactivated[0].NotificationPending {
		t.Fatalf("reactivated=%#v err=%v", reactivated, err)
	}

	active := &models.AlertSuppression{
		Kind: models.SuppressionSilence, ScopeType: models.ScopeAgent, ScopeID: f.agent.ID,
		StartsAt: base.Add(-time.Minute), EndsAt: base.Add(10 * time.Minute),
		Reason: "temporary silence", CreatedBy: "operator",
	}
	if err := f.store.CreateAlertSuppression(ctx, active); err != nil {
		t.Fatal(err)
	}
	suppressed, err := f.store.GetAlert(ctx, opened.Alert.ID)
	if err != nil || suppressed.Status != models.AlertSuppressed {
		t.Fatalf("active suppression alert=%#v err=%v", suppressed, err)
	}
	if err := f.store.CancelAlertSuppression(ctx, active.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	reactivated, err = f.store.ReactivateExpiredSuppressedAlerts(ctx, base.Add(time.Second), 20)
	if err != nil || len(reactivated) != 1 || reactivated[0].Status != models.AlertNew {
		t.Fatalf("reactivation after cancel=%#v err=%v", reactivated, err)
	}
	events, err := f.store.ListAlertEvents(ctx, opened.Alert.ID)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	if types[models.AlertEventSuppressed] < 2 || types[models.AlertEventUnsuppressed] < 2 {
		t.Fatalf("suppression timeline = %#v", types)
	}
}

func TestAlertPaginationUsesStableTupleCursor(t *testing.T) {
	f := newAlertReliabilityFixture(t, "pagination")
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		sourceID := uuid.NewString()
		if _, err := f.store.ObserveAlert(ctx, AlertObservation{
			SourceType: models.AlertSourceSystem, SourceID: &sourceID,
			ConditionKey: "page", Severity: models.SeverityWarning,
			Message: "page alert", ObservedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := f.store.ListAlertsPage(ctx, AlertListOptions{Limit: 1, SourceType: models.AlertSourceSystem})
	if err != nil || len(first) != 2 {
		t.Fatalf("first page with sentinel = %#v, err=%v", first, err)
	}
	cursorTime, cursorID := first[0].CreatedAt, first[0].ID
	second, err := f.store.ListAlertsPage(ctx, AlertListOptions{
		Limit: 1, SourceType: models.AlertSourceSystem,
		BeforeTime: &cursorTime, BeforeID: cursorID,
	})
	if err != nil || len(second) != 2 || second[0].ID == first[0].ID {
		t.Fatalf("second page with sentinel = %#v, err=%v", second, err)
	}
}

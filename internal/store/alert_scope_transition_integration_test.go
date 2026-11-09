package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestMovedDeviceIncidentCannotAcceptDestinationMetadataIntegration(t *testing.T) {
	for _, move := range []string{"site", "group", "bulk"} {
		t.Run(move, func(t *testing.T) {
			f := newAlertReliabilityFixture(t, "scope boundary")
			ctx := context.Background()
			obs := AlertObservation{AgentID: &f.agent.ID, SourceType: models.AlertSourceRule, SourceID: &f.agent.ID,
				ConditionKey: "job:adhoc", Severity: models.SeverityWarning, Message: "old-scope-canary", ObservationID: "job:first", ObservedAt: time.Now()}
			first, err := f.store.ObserveAlert(ctx, obs)
			if err != nil {
				t.Fatal(err)
			}
			// Control: a same-scope observation retains its incident identity.
			obs.ObservationID = "job:second"
			control, err := f.store.ObserveAlert(ctx, obs)
			if err != nil || control.Alert.ID != first.Alert.ID {
				t.Fatalf("same-scope control: %+v %v", control, err)
			}
			client, err := f.store.CreateClient(ctx, "destination")
			if err != nil {
				t.Fatal(err)
			}
			site, err := f.store.CreateSite(ctx, client.ID, "destination")
			if err != nil {
				t.Fatal(err)
			}
			switch move {
			case "site":
				err = f.store.SetAgentSite(ctx, f.agent.ID, &site.ID)
			case "bulk":
				err = f.store.MoveAgents(ctx, []string{f.agent.ID}, site.ID, nil)
			case "group":
				group, e := f.store.CreateGroup(ctx, "destination", &f.site.ID)
				if e != nil {
					t.Fatal(e)
				}
				err = f.store.SetAgentGroup(ctx, f.agent.ID, &group.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			obs.Message, obs.Output, obs.ObservationID = "destination-canary", "destination-output", "job:third"
			second, err := f.store.ObserveAlert(ctx, obs)
			if err != nil {
				t.Fatal(err)
			}
			if second.Alert.ID == first.Alert.ID || second.Alert.ReopenedFrom != nil || !second.Opened {
				t.Fatalf("incident crossed scopes/history: %+v", second)
			}
			old, err := f.store.GetAlert(ctx, first.Alert.ID)
			if err != nil {
				t.Fatal(err)
			}
			if old.Status != models.AlertResolved || old.ResolutionKind != "inapplicable" || old.NotificationPending || old.Message != "old-scope-canary" || strings.Contains(old.ResolutionNote, "destination") {
				t.Fatalf("old incident contaminated: %+v", old)
			}
			events, err := f.store.ListAlertEvents(ctx, first.Alert.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if strings.Contains(event.Message, "destination") {
					t.Fatalf("old timeline contaminated: %+v", event)
				}
			}
			if move != "group" {
				reader, err := f.store.CreateUserWithScopes(ctx, "former-reader", "hash", models.RoleTech,
					[]models.UserScope{{ScopeType: models.ScopeClient, ScopeID: f.client.ID}})
				if err != nil {
					t.Fatal(err)
				}
				if allowed, err := f.store.AlertAllowedForUser(ctx, reader.ID, first.Alert.ID); err != nil || !allowed {
					t.Fatalf("old history inaccessible: %v %v", allowed, err)
				}
				if allowed, err := f.store.AlertAllowedForUser(ctx, reader.ID, second.Alert.ID); err != nil || allowed {
					t.Fatalf("destination exposed to old reader: %v %v", allowed, err)
				}
			}
		})
	}
}

func TestStaleRuleObservationsAndHistoricalJobsAreRejectedIntegration(t *testing.T) {
	f := newAlertReliabilityFixture(t, "scope provenance")
	ctx := context.Background()
	obs := AlertObservation{AgentID: &f.agent.ID, SourceType: models.AlertSourceRule, SourceID: &f.agent.ID,
		ConditionKey: models.CheckPatchFailure, Severity: models.SeverityWarning, Message: "old scope", ObservedAt: time.Now(), MonitoringScopeSince: &f.agent.MonitoringScopeSince}
	first, err := f.store.ObserveAlert(ctx, obs)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.store.CreateJob(ctx, &models.Job{AgentID: f.agent.ID, Type: "script", Shell: "bash", Script: "exit 1", Description: "old job canary", Status: models.JobFailed})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := f.store.LatestJobOutcomes(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(outcomes) != 1 || outcomes[0].JobID != job.ID {
		t.Fatalf("current job missing: %+v %v", outcomes, err)
	}
	if err := f.store.SetAgentSite(ctx, f.agent.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ObserveAlert(ctx, obs); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale observation accepted: %v", err)
	}
	if _, err := f.store.ResolveAlertCondition(ctx, obs.SourceType, obs.SourceID, obs.AgentID, obs.ConditionKey, "system", "automatic", "stale canary", time.Now(), obs.MonitoringScopeSince); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale resolution accepted: %v", err)
	}
	if _, err := f.store.ResolveAlertSource(ctx, obs.SourceType, obs.SourceID, obs.AgentID, "system", "automatic", "stale canary", time.Now(), obs.MonitoringScopeSince); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale source resolution accepted: %v", err)
	}
	outcomes, err = f.store.LatestJobOutcomes(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("old-scope job exposed: %+v %v", outcomes, err)
	}
	// Moving away and back must also invalidate the old provenance marker.
	if err := f.store.SetAgentSite(ctx, f.agent.ID, &f.site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ObserveAlert(ctx, obs); !errors.Is(err, ErrConflict) {
		t.Fatalf("move-away-and-back accepted old metadata: %v", err)
	}
	current, err := f.store.GetAgent(ctx, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A no-op move preserves the marker and valid observations.
	if err := f.store.SetAgentSite(ctx, f.agent.ID, &f.site.ID); err != nil {
		t.Fatal(err)
	}
	// The first move cleared its department, so retire the old group snapshot.
	obs.MonitoringScopeSince = &current.MonitoringScopeSince
	if _, err := f.store.ObserveAlert(ctx, obs); err != nil {
		t.Fatalf("current observation refused: %v", err)
	}
	fresh, err := f.store.CreateJob(ctx, &models.Job{AgentID: f.agent.ID, Type: "script", Shell: "bash", Script: "exit 1", Description: "current job", Status: models.JobFailed})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err = f.store.LatestJobOutcomes(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(outcomes) != 1 || outcomes[0].JobID != fresh.ID {
		t.Fatalf("current job control: %+v %v", outcomes, err)
	}
	old, err := f.store.GetAlert(ctx, first.Alert.ID)
	if err != nil || strings.Contains(old.ResolutionNote, "stale canary") {
		t.Fatalf("stale resolution contaminated history: %+v %v", old, err)
	}
}

func TestDestinationSuppressionDoesNotWriteFormerScopeHistoryIntegration(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "scheduled"}[scheduled], func(t *testing.T) {
			f := newAlertReliabilityFixture(t, "scope suppression")
			ctx := context.Background()
			obs := AlertObservation{AgentID: &f.agent.ID, SourceType: models.AlertSourceRule, SourceID: &f.agent.ID, ConditionKey: models.CheckPatchFailure,
				Severity: models.SeverityWarning, Message: "old incident", ObservedAt: time.Now()}
			first, err := f.store.ObserveAlert(ctx, obs)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.SetAgentSite(ctx, f.agent.ID, nil); err != nil {
				t.Fatal(err)
			}
			at := time.Now()
			if scheduled {
				at = at.Add(time.Hour)
			}
			sup := &models.AlertSuppression{Kind: "maintenance", ScopeType: models.ScopeAgent, ScopeID: f.agent.ID,
				StartsAt: at, EndsAt: at.Add(time.Hour), Reason: "destination-canary", CreatedBy: "operator"}
			if err := f.store.CreateAlertSuppression(ctx, sup); err != nil {
				t.Fatal(err)
			}
			if scheduled {
				if _, err := f.store.ApplyActiveAlertSuppressions(ctx, at.Add(time.Second), 100); err != nil {
					t.Fatal(err)
				}
			}
			old, err := f.store.GetAlert(ctx, first.Alert.ID)
			if err != nil || old.SilenceReason != "" {
				t.Fatalf("destination silence leaked: %+v %v", old, err)
			}
			events, err := f.store.ListAlertEvents(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				if strings.Contains(e.Message, "destination-canary") {
					t.Fatal("destination reason leaked into history")
				}
			}
			// Control: the same destination suppression can affect its own incident.
			second, err := f.store.ObserveAlert(ctx, obs)
			if err != nil {
				t.Fatal(err)
			}
			if scheduled {
				if _, err := f.store.ApplyActiveAlertSuppressions(ctx, at.Add(time.Second), 100); err != nil {
					t.Fatal(err)
				}
			}
			current, err := f.store.GetAlert(ctx, second.Alert.ID)
			if err != nil || current.Status != models.AlertSuppressed || current.SilenceReason != sup.Reason {
				t.Fatalf("same-scope suppression refused: %+v %v", current, err)
			}
		})
	}
}

func TestDeviceSuppressionCannotFollowDeviceIntoAnotherScopeIntegration(t *testing.T) {
	for _, scopeType := range []string{models.ScopeAgent, "check"} {
		t.Run(scopeType, func(t *testing.T) {
			f := newAlertReliabilityFixture(t, "suppression provenance")
			ctx := context.Background()
			id := f.agent.ID
			if scopeType == "check" {
				id = f.check.ID
			}
			sup := &models.AlertSuppression{Kind: "maintenance", ScopeType: scopeType, ScopeID: id, StartsAt: time.Now().Add(-time.Minute), EndsAt: time.Now().Add(time.Hour), Reason: "old-scope-private-canary", CreatedBy: "old operator"}
			if err := f.store.CreateAlertSuppression(ctx, sup); err != nil {
				t.Fatal(err)
			}
			obs := AlertObservation{AgentID: &f.agent.ID, SourceType: models.AlertSourceRule, SourceID: &f.agent.ID, RuleID: &f.check.ID,
				ConditionKey: models.CheckPatchFailure, Severity: models.SeverityWarning, Message: "condition", ObservedAt: time.Now()}
			first, err := f.store.ObserveAlert(ctx, obs)
			if err != nil || first.Alert.Status != models.AlertSuppressed {
				t.Fatalf("same-scope control failed: %+v %v", first, err)
			}
			if err := f.store.SetAgentSite(ctx, f.agent.ID, nil); err != nil {
				t.Fatal(err)
			}
			second, err := f.store.ObserveAlert(ctx, obs)
			if err != nil || second.Alert.Status == models.AlertSuppressed || second.Alert.SilenceReason != "" {
				t.Fatalf("old silence transferred: %+v %v", second, err)
			}
			if _, err := f.store.ApplyActiveAlertSuppressions(ctx, time.Now(), 100); err != nil {
				t.Fatal(err)
			}
			secondAlert, err := f.store.GetAlert(ctx, second.Alert.ID)
			if err != nil || secondAlert.SilenceReason != "" {
				t.Fatalf("scheduled old silence transferred: %+v %v", secondAlert, err)
			}
			reader, err := f.store.CreateUserWithScopes(ctx, "new-reader", "hash", models.RoleTech,
				[]models.UserScope{{ScopeType: models.ScopeAgent, ScopeID: f.agent.ID}})
			if err != nil {
				t.Fatal(err)
			}
			visible, err := f.store.ListAlertSuppressions(ctx, reader.ID, true, false)
			if err != nil || len(visible) != 0 {
				t.Fatalf("old suppression metadata visible in destination: %+v %v", visible, err)
			}
			// A request authorized on the previous revision cannot insert after a move.
			stale := &models.AlertSuppression{Kind: "maintenance", ScopeType: scopeType, ScopeID: id, StartsAt: time.Now(), EndsAt: time.Now().Add(time.Hour), Reason: "stale canary", MonitoringScopeSince: &f.agent.MonitoringScopeSince}
			if err := f.store.CreateAlertSuppression(ctx, stale); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale suppression inserted: %v", err)
			}
		})
	}
}

func TestMovedDeviceResolutionCannotAppendDestinationRuleNamesIntegration(t *testing.T) {
	for _, conditionOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "source", true: "condition"}[conditionOnly], func(t *testing.T) {
			f := newAlertReliabilityFixture(t, "scope resolution")
			ctx := context.Background()
			obs := AlertObservation{AgentID: &f.agent.ID, SourceType: models.AlertSourceRule, SourceID: &f.agent.ID,
				ConditionKey: models.CheckPatchFailure, Severity: models.SeverityWarning, Message: "old incident", ObservedAt: time.Now()}
			first, err := f.store.ObserveAlert(ctx, obs)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.store.SetAgentSite(ctx, f.agent.ID, nil); err != nil {
				t.Fatal(err)
			}
			resolve := func(note string) ([]models.Alert, error) {
				if conditionOnly {
					return f.store.ResolveAlertCondition(ctx, obs.SourceType, obs.SourceID, obs.AgentID, obs.ConditionKey, "system", "inapplicable", note, time.Now())
				}
				return f.store.ResolveAlertSource(ctx, obs.SourceType, obs.SourceID, obs.AgentID, "system", "inapplicable", note, time.Now())
			}
			resolved, err := resolve("destination-private-rule-and-policy-canary")
			if err != nil || len(resolved) != 0 {
				t.Fatalf("old incident accepted destination resolution: %+v %v", resolved, err)
			}
			old, err := f.store.GetAlert(ctx, first.Alert.ID)
			if err != nil || old.Status != models.AlertResolved || strings.Contains(old.ResolutionNote, "canary") {
				t.Fatalf("destination note leaked: %+v %v", old, err)
			}
			events, err := f.store.ListAlertEvents(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if strings.Contains(event.Message, "canary") {
					t.Fatal("destination note leaked into old timeline")
				}
			}
			// Control: a destination incident can be resolved with its own note.
			if _, err := f.store.ObserveAlert(ctx, obs); err != nil {
				t.Fatal(err)
			}
			resolved, err = resolve("destination control")
			if err != nil || len(resolved) != 1 || resolved[0].ResolutionNote != "destination control" {
				t.Fatalf("same-scope resolution denied: %+v %v", resolved, err)
			}
		})
	}
}

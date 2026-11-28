package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/p1ngu/rmm/internal/models"
)

func TestSameNotificationModelTimeEquality(t *testing.T) {
	base := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	sameInFixedZone := base.In(time.FixedZone("BRT", -3*3600))
	later := base.Add(time.Second)

	agentID := uuid.NewString()
	alertID := uuid.NewString()
	mk := func(ts *time.Time) *models.Alert {
		return &models.Alert{
			ID:                        alertID,
			AgentSnapshotID:           &agentID,
			Version:                   1,
			NotificationSeq:           1,
			NotificationPending:       true,
			NotificationKind:          models.AlertEventOpened,
			Status:                    models.AlertNew,
			SourceType:                models.AlertSourceCheck,
			Severity:                  models.SeverityWarning,
			Message:                   "disk high",
			AgentMonitoringScopeSince: ts,
		}
	}

	if !sameNotificationModel(mk(&base), mk(&sameInFixedZone)) {
		t.Fatal("same instant in different time zones must match")
	}
	nowMono := time.Now()
	nowNoMono := nowMono.Round(0)
	if !sameNotificationModel(mk(&nowMono), mk(&nowNoMono)) {
		t.Fatal("same instant with and without monotonic reading must match")
	}
	if sameNotificationModel(mk(&base), mk(&later)) {
		t.Fatal("different monitoring scope instants must not match")
	}
	if sameNotificationModel(mk(&base), mk(nil)) {
		t.Fatal("nil and non-nil monitoring scope timestamps must not match")
	}
	if !sameNotificationModel(mk(nil), mk(nil)) {
		t.Fatal("both nil monitoring scope timestamps must match")
	}
}

func TestIsIncidentNotificationEvent(t *testing.T) {
	want := map[string]bool{
		NotifyCheckFailed:      true,
		NotifyCheckRecovered:   true,
		NotifyAgentOffline:     true,
		NotifyAgentRecovered:   true,
		NotifyJobFailed:        false,
		NotifyAIReleaseRevoked: false,
		NotifyDeviceKeyIssue:   false,
	}
	for _, ev := range NotificationEventTypes {
		if got := isIncidentNotificationEvent(ev); got != want[ev] {
			t.Errorf("isIncidentNotificationEvent(%q) = %v, want %v", ev, got, want[ev])
		}
	}
}

func TestDecodeDeliveryContextValidation(t *testing.T) {
	agentID := uuid.NewString()
	alertID := uuid.NewString()
	ruleID := uuid.NewString()

	validIncident, _ := json.Marshal(models.NotificationDeliveryContext{
		Version:         1,
		EventType:       NotifyCheckFailed,
		AgentID:         agentID,
		AlertID:         alertID,
		NotificationSeq: 1,
		SourceType:      models.AlertSourceCheck,
		RuleID:          ruleID,
		Route:           models.NotificationRouteSnapshot{Mode: "automatic"},
	})
	if _, err := decodeDeliveryContext(validIncident); err != nil {
		t.Fatalf("valid incident context rejected: %v", err)
	}

	validNonIncident, _ := json.Marshal(models.NotificationDeliveryContext{
		Version:   1,
		EventType: NotifyDeviceKeyIssue,
		AgentID:   agentID,
		Route:     models.NotificationRouteSnapshot{Mode: "automatic"},
	})
	if _, err := decodeDeliveryContext(validNonIncident); err != nil {
		t.Fatalf("valid non-incident context rejected: %v", err)
	}

	oversized := json.RawMessage(`{"version":1,"event_type":"` + strings.Repeat("a", 16385) + `"}`)
	cases := []struct {
		name string
		raw  json.RawMessage
	}{
		{"oversized", oversized},
		{"unknown_field", json.RawMessage(`{"version":1,"event_type":"device_key_issue","agent_id":"` + agentID + `","route":{"mode":"automatic"},"extra":1}`)},
		{"trailing_json", json.RawMessage(string(validNonIncident) + `{}`)},
		{"bad_version", json.RawMessage(`{"version":2,"event_type":"device_key_issue","agent_id":"` + agentID + `","route":{"mode":"automatic"}}`)},
		{"bad_route", json.RawMessage(`{"version":1,"event_type":"device_key_issue","agent_id":"` + agentID + `","route":{"mode":"selected"}}`)},
		{"bad_uuid", json.RawMessage(`{"version":1,"event_type":"device_key_issue","agent_id":"not-a-uuid","route":{"mode":"automatic"}}`)},
		{"nonincident_with_rule", json.RawMessage(`{"version":1,"event_type":"device_key_issue","agent_id":"` + agentID + `","rule_id":"` + ruleID + `","route":{"mode":"automatic"}}`)},
		{"incident_missing_seq", json.RawMessage(`{"version":1,"event_type":"check_failed","agent_id":"` + agentID + `","alert_id":"` + alertID + `","source_type":"check","route":{"mode":"automatic"}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeDeliveryContext(tc.raw); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

package scheduler

import (
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

func TestAlertDeviceDescriptionRespectsScopeHistory(t *testing.T) {
	now := time.Now()
	id := "agent-id"
	for _, tc := range []struct {
		name                  string
		id                    string
		scopeSince, firstSeen time.Time
		want                  string
	}{
		{"same scope", id, now.Add(-time.Hour), now, "Friendly device"},
		{"same timestamp", id, now, now, "Friendly device"},
		{"moved scope", id, now.Add(time.Second), now, ""},
		{"unknown scope", id, time.Time{}, now, ""},
		{"unknown incident time", id, now, time.Time{}, ""},
		{"wrong device", "another-agent", now.Add(-time.Hour), now, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alert := &models.Alert{AgentSnapshotID: &id, FirstSeenAt: tc.firstSeen}
			agent := &models.Agent{ID: tc.id, Description: "Friendly device", MonitoringScopeSince: tc.scopeSince}
			if got := alertDeviceDescription(alert, agent); got != tc.want {
				t.Fatalf("description = %q, want %q", got, tc.want)
			}
		})
	}
	if alertDeviceDescription(nil, nil) != "" || alertDeviceDescription(&models.Alert{}, &models.Agent{}) != "" {
		t.Fatal("missing device should fall back to the snapshot hostname")
	}
}

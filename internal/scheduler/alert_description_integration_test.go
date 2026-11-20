package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify"
	"github.com/p1ngu/rmm/internal/store"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

func TestAlertDescriptionOutboxIntegration(t *testing.T) {
	url := os.Getenv("RMM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	st := storetest.NewSchema(t, "alert_description", url)
	u, err := st.CreateUser(ctx, "operator", "unused", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateUserEmail(ctx, u.ID, "operator@example.test"); err != nil {
		t.Fatal(err)
	}
	a, err := st.CreateAgent(ctx, "technical-host", "linux", "amd64", "test", "hash", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SetAgentMeta(ctx, a.ID, "Friendly device", ""); err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{store: st, notifier: notify.New(st, func(context.Context) (notify.Config, string, error) {
		return notify.Config{Host: "relay.example.test", From: "rmm@example.test"}, "", nil
	})}
	initialTemplates := notify.DefaultAlertTemplates()
	initialTemplates.DeviceName = "description"
	initialStored, _ := json.Marshal(initialTemplates)
	if err := st.UpsertAppSetting(ctx, notify.AlertTemplatesKey, string(initialStored), false); err != nil {
		t.Fatal(err)
	}
	opened, err := st.ObserveAlert(ctx, store.AlertObservation{
		AgentID: &a.ID, SourceType: models.AlertSourceAgent, SourceID: &a.ID,
		ConditionKey: "offline", Severity: models.SeverityError, Message: "Offline", ObservedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.queueAlertNotification(ctx, &opened.Alert, ""); err != nil {
		t.Fatal(err)
	}
	// A description edit during a retry must not duplicate the transition.
	if err = st.SetAgentMeta(ctx, a.ID, "Renamed device", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.queueAlertNotification(ctx, &opened.Alert, ""); err != nil {
		t.Fatal(err)
	}
	queued, err := st.ClaimPendingNotifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Subject != "[OpenRMM] Agent offline — Friendly device" || !strings.Contains(queued[0].Body, "Device: technical-host\n") {
		t.Fatalf("unexpected opening notifications: %+v", queued)
	}
	templates := notify.DefaultAlertTemplates()
	templates.DeviceName = "both"
	templates.Recovery.Subject = "Retour {{device}}"
	templates.Recovery.Body = "{{description}}|{{hostname}}|{{details}}"
	stored, _ := json.Marshal(templates)
	if err := st.UpsertAppSetting(ctx, notify.AlertTemplatesKey, string(stored), false); err != nil {
		t.Fatal(err)
	}
	resolved, err := st.ResolveAlertCondition(ctx, models.AlertSourceAgent, &a.ID, &a.ID, "offline", "system", "automatic", "Online", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved = %+v", resolved)
	}
	if err = s.queueAlertNotification(ctx, &resolved[0], ""); err != nil {
		t.Fatal(err)
	}
	queued, err = st.ClaimPendingNotifications(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Subject != "Retour Renamed device (technical-host)" || queued[0].Body != "Renamed device|technical-host|Online" {
		t.Fatalf("unexpected recovery notifications: %+v", queued)
	}
}

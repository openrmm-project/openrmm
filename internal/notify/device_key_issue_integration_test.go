package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/e2e"
	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

// The alert for a device key issue names the device and both fingerprints,
// never a key, and reaches the people who can see the device.
func TestDeviceKeyIssueAlertIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	agent, err := st.CreateAgent(ctx, "srv-files", "linux", "amd64", "0.13.0", "tok-keys", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	recipientID := newRecipient(t, ctx, st, "oncall@example.test", true)
	if err := st.SetNotificationRuleFilter(ctx, recipientID, store.NotificationRuleFilter{Mode: "selected"}); err != nil {
		t.Fatal(err)
	}
	relay := Config{Host: "127.0.0.1", Port: 1, From: "alerts@notify.test", TLSMode: "none"}
	svc := New(st, func(context.Context) (Config, string, error) { return relay, "https://rmm.example.test", nil })

	_, pinned, _ := e2e.GenerateKeyPair()
	_, presented, _ := e2e.GenerateKeyPair()
	pinnedFP, presentedFP := e2e.DisplayFingerprint(pinned), e2e.DisplayFingerprint(presented)
	svc.QueueDeviceKeyIssue(ctx, agent.ID, "srv-files", models.DeviceKeyChanged, pinnedFP, presentedFP, "198.51.100.20")

	queued, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
	if err != nil || len(queued) != 1 {
		t.Fatalf("queued %d notifications (%v), want 1", len(queued), err)
	}
	n := queued[0]
	if n.Subject != "[OpenRMM] Device encryption key needs review — srv-files" {
		t.Fatalf("subject = %q", n.Subject)
	}
	for _, want := range []string{pinnedFP, presentedFP, "198.51.100.20", "paused", "do not approve", "https://rmm.example.test"} {
		if !strings.Contains(n.Body, want) {
			t.Fatalf("body lacks %q:\n%s", want, n.Body)
		}
	}
	if strings.Contains(n.Body, pinned) || strings.Contains(n.Body, presented) {
		t.Fatal("the alert carries a key, not only its fingerprint")
	}
	if !store.ValidNotificationEvent(store.NotifyDeviceKeyIssue) || !store.DefaultSubscribed(store.NotifyDeviceKeyIssue) {
		t.Fatal("device key issues are not a subscribable, default-on event")
	}
	if len(n.DeliveryContext) == 0 {
		t.Fatal("a queued device key alert lacks delivery provenance")
	}
	if ok, reason, err := st.NotificationDeliveryAllowed(ctx, &n, relay.Operator); err != nil || !ok {
		t.Fatalf("fresh device key notification authorized=%v, reason=%q, error=%v", ok, reason, err)
	}
	n.DeliveryContext = nil // Existing rows predate the delivery context migration.
	if ok, reason, err := st.NotificationDeliveryAllowed(ctx, &n, relay.Operator); err != nil || !ok {
		t.Fatalf("legacy device key notification authorized=%v, reason=%q, error=%v", ok, reason, err)
	}
}

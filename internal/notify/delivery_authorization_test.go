package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify/smtptest"
	"github.com/p1ngu/rmm/internal/store"
)

func TestDrainRechecksEveryRecipientForBothTransportsIntegration(t *testing.T) {
	for _, operator := range []bool{false, true} {
		for _, change := range []string{"none", "disabled", "address", "permission", "scope", "subscription", "rule", "confirmation"} {
			t.Run(fmt.Sprintf("operator=%v/%s", operator, change), func(t *testing.T) {
				ctx, st := drainTestStore(t)
				relay := smtptest.New(t)
				client, err := st.CreateClient(ctx, "client")
				if err != nil {
					t.Fatal(err)
				}
				site, err := st.CreateSite(ctx, client.ID, "site")
				if err != nil {
					t.Fatal(err)
				}
				other, err := st.CreateSite(ctx, client.ID, "other")
				if err != nil {
					t.Fatal(err)
				}
				agent, err := st.CreateAgent(ctx, "host", "linux", "amd64", "1", "token", &site.ID, nil)
				if err != nil {
					t.Fatal(err)
				}
				userID := newRecipient(t, ctx, st, "oncall@example.test", true)
				if err = st.ReplaceUserScopes(ctx, userID, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: site.ID}}); err != nil {
					t.Fatal(err)
				}
				check, err := st.CreateCheck(ctx, &models.Check{Attachments: []models.Attachment{{AgentID: &agent.ID}}, Type: models.CheckScript, Name: "health", Config: json.RawMessage(`{"shell":"bash","script":"exit 1"}`), Enabled: true, IntervalSec: 60, TimeoutSec: 10, FailureCount: 1, RecoveryCount: 1})
				if err != nil {
					t.Fatal(err)
				}
				transition, err := st.ObserveAlert(ctx, store.AlertObservation{AgentID: &agent.ID, SourceType: models.AlertSourceCheck, SourceID: &check.ID, RuleID: &check.ID, ConditionKey: "health", Severity: models.SeverityWarning, Message: "failed", ObservedAt: time.Now()})
				if err != nil {
					t.Fatal(err)
				}
				cfg := Config{Host: "127.0.0.1", Port: relay.Port, From: "rmm@example.test", TLSMode: "none", Operator: operator, HourlyLimit: 100}
				svc := New(st, func(context.Context) (Config, string, error) { return cfg, "https://console.example.test", nil })
				event := AlertOpenedEvent(store.NotifyCheckFailed, agent.ID, agent.Hostname, agent.Description, transition.Alert.ID, "failed", "https://console.example.test")
				event.AlertSnapshot = &transition.Alert
				if err = svc.Queue(ctx, event); err != nil {
					t.Fatal(err)
				}
				switch change {
				case "disabled":
					_, err = st.SetUserDisabled(ctx, userID, true)
				case "address":
					_, err = st.UpdateUserEmail(ctx, userID, "different@example.test")
				case "permission":
					_, err = st.UpdateUserRole(ctx, userID, models.RoleReadOnly)
				case "scope":
					err = st.ReplaceUserScopes(ctx, userID, []models.UserScope{{ScopeType: models.ScopeSite, ScopeID: other.ID}})
				case "subscription":
					err = st.SetNotificationSubscription(ctx, userID, store.NotifyCheckFailed, false)
				case "rule":
					err = st.SetNotificationRuleFilter(ctx, userID, store.NotificationRuleFilter{Mode: "selected"})
				case "confirmation":
					_, err = st.UpdateUserEmail(ctx, userID, "temporary@example.test")
					if err == nil {
						_, err = st.UpdateUserEmail(ctx, userID, "oncall@example.test")
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				svc.drain(ctx)
				want := 0
				if change == "none" || (change == "confirmation" && !operator) {
					want = 1
				}
				if got := len(relay.Messages()); got != want {
					t.Fatalf("SMTP delivered=%d want=%d", got, want)
				}
				stats, err := st.OutboxStats(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if stats.Pending != 0 || stats.Processing != 0 || stats.Failed != 1-want {
					t.Fatalf("unexpected outbox %+v", stats)
				}
				svc.drain(ctx)
				if len(relay.Messages()) != want {
					t.Fatal("denied message retried or sent twice")
				}
			})
		}
	}
}

func TestLegacyRecipientAddressCaseAndAnonymousDenialIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	relay := smtptest.New(t)
	id := newRecipient(t, ctx, st, "case@example.test", false)
	if err := st.EnqueueNotification(ctx, &models.Notification{EventType: store.NotifyAgentOffline, UserID: &id, Recipient: "CASE@EXAMPLE.TEST", Subject: "case", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := st.EnqueueNotification(ctx, &models.Notification{EventType: store.NotifyAgentOffline, Recipient: "anonymous@example.test", Subject: "anonymous", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	svc := New(st, func(context.Context) (Config, string, error) {
		return Config{Host: "127.0.0.1", Port: relay.Port, From: "rmm@example.test", TLSMode: "none"}, "", nil
	})
	svc.drain(ctx)
	msgs := relay.Messages()
	if len(msgs) != 1 || !strings.EqualFold(msgs[0].To[0], "case@example.test") {
		t.Fatalf("unexpected legacy delivery %+v", msgs)
	}
}

func TestQueueTransportOffAndSettingsFailurePreserveTransitionSemanticsIntegration(t *testing.T) {
	for _, mode := range []string{"disabled", "read-error", "stale-disabled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, st := drainTestStore(t)
			agent, err := st.CreateAgent(ctx, "host", "linux", "amd64", "1", "token", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			newRecipient(t, ctx, st, "oncall@example.test", false)
			observation := store.AlertObservation{AgentID: &agent.ID, SourceType: models.AlertSourceAgent, ConditionKey: "offline", Severity: models.SeverityWarning, Message: "offline", ObservedAt: time.Now()}
			transition, err := st.ObserveAlert(ctx, observation)
			if err != nil {
				t.Fatal(err)
			}
			event := AlertOpenedEvent(store.NotifyAgentOffline, agent.ID, agent.Hostname, agent.Description, transition.Alert.ID, "offline", "")
			event.AlertSnapshot = &transition.Alert
			if mode == "stale-disabled" {
				observation.Message = "changed while composing"
				observation.ObservedAt = time.Now().Add(time.Second)
				if _, err = st.ObserveAlert(ctx, observation); err != nil {
					t.Fatal(err)
				}
			}
			svc := New(st, func(context.Context) (Config, string, error) {
				if mode == "read-error" {
					return Config{}, "", fmt.Errorf("temporary settings error")
				}
				return Config{}, "", nil
			})
			err = svc.Queue(ctx, event)
			if mode == "stale-disabled" {
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("stale disabled queue=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			current, err := st.GetAlert(ctx, transition.Alert.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.NotificationPending != (mode == "stale-disabled") {
				t.Fatalf("pending=%v mode=%s", current.NotificationPending, mode)
			}
			stats, err := st.OutboxStats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "read-error" {
				want = 1
			}
			if stats.Pending != want {
				t.Fatalf("queued=%d want=%d", stats.Pending, want)
			}
		})
	}
}

func TestDrainRechecksAuthorizationOnRetryIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	relay := smtptest.New(t)
	userID := newRecipient(t, ctx, st, "retry@example.test", true)
	agent, err := st.CreateAgent(ctx, "retry-host", "linux", "amd64", "1", "retry-token", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Host: "127.0.0.1", Port: relay.Port, From: "rmm@example.test", TLSMode: "none", Operator: true, HourlyLimit: 100}
	svc := New(st, func(context.Context) (Config, string, error) { return cfg, "", nil })
	if err = svc.Queue(ctx, Event{Type: store.NotifyAgentOffline, AgentID: agent.ID, Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	first, err := st.ClaimPendingNotifications(ctx, 1)
	if err != nil || len(first) != 1 || first[0].Attempts != 1 {
		t.Fatalf("first claim=%v err=%v", first, err)
	}
	// Simulate a transient first SMTP failure, preserving the real retry/lease path.
	if err = st.MarkNotificationFailedLeased(ctx, first[0].ID, *first[0].LeaseToken, 1, 5, "transient fixture failure", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = st.SetUserDisabled(ctx, userID, true); err != nil {
		t.Fatal(err)
	}
	svc.drain(ctx)
	if len(relay.Messages()) != 0 {
		t.Fatal("retry bypassed current account eligibility")
	}
	stats, err := st.OutboxStats(ctx)
	if err != nil || stats.Failed != 1 || stats.Pending != 0 {
		t.Fatalf("retry state=%+v err=%v", stats, err)
	}
}

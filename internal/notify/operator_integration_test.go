package notify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/notify/smtptest"
	"github.com/p1ngu/rmm/internal/store"
)

// The operator's shared alert service delivers only to confirmed addresses,
// within its hourly budget, and signs each alert with the instance it came from.
func TestOperatorServiceDeliversOnlyToConfirmedRecipientsIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	relay := smtptest.New(t)
	op := Config{Host: "127.0.0.1", Port: relay.Port, From: "alerts@notify.test", FromName: "OpenRMM Alerts", TLSMode: "none", Operator: true, HourlyLimit: 100}

	confirmedUser := newRecipient(t, ctx, st, "confirmed@example.test", true)
	unconfirmedUser := newRecipient(t, ctx, st, "unconfirmed@example.test", false)
	for _, u := range []struct{ id, email string }{{confirmedUser, "confirmed@example.test"}, {unconfirmedUser, "unconfirmed@example.test"}} {
		id := u.id
		if err := st.EnqueueNotification(ctx, &models.Notification{EventType: store.NotifyAgentOffline, UserID: &id,
			Recipient: u.email, Subject: "[OpenRMM] Offline", Body: "Device X is offline"}); err != nil {
			t.Fatal(err)
		}
	}

	svc := New(st, func(context.Context) (Config, string, error) { return op, "https://acme.openrmm.io", nil })
	svc.drain(ctx)

	msgs := relay.Messages()
	if len(msgs) != 1 || msgs[0].To[0] != "confirmed@example.test" {
		t.Fatalf("delivered %+v, want only the confirmed recipient", msgs)
	}
	if !strings.Contains(msgs[0].Data, "acme.openrmm.io") || !strings.Contains(msgs[0].Data, "comes from that instance") {
		t.Errorf("the alert lacks the instance footer: %q", msgs[0].Data)
	}
	// The unconfirmed one is given up on, not retried forever.
	again, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("%d message(s) still pending after the drain", len(again))
	}
}

func TestOperatorServiceKeepsToItsHourlyBudgetIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	relay := smtptest.New(t)
	op := Config{Host: "127.0.0.1", Port: relay.Port, From: "alerts@notify.test", TLSMode: "none", Operator: true, HourlyLimit: 2}
	user := newRecipient(t, ctx, st, "oncall@example.test", true)
	for i := 0; i < 5; i++ {
		id := user
		if err := st.EnqueueNotification(ctx, &models.Notification{EventType: store.NotifyAgentOffline, UserID: &id,
			Recipient: "oncall@example.test", Subject: "s", Body: "b", DedupeKey: time.Now().String() + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(st, func(context.Context) (Config, string, error) { return op, "https://acme.openrmm.io", nil })
	svc.drain(ctx)
	svc.drain(ctx)
	if n := len(relay.Messages()); n != 2 {
		t.Fatalf("sent %d in the hour, want the budget of 2", n)
	}
	// The rest waits for the next hour rather than being dropped.
	pending, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 {
		t.Errorf("%d waiting, want 3", len(pending))
	}
}

// newRecipient creates a user with an address, confirmed or not.
func newRecipient(t *testing.T, ctx context.Context, st *store.Store, email string, confirmed bool) string {
	t.Helper()
	u, err := st.CreateUser(ctx, strings.Split(email, "@")[0], "hash", models.RoleTech)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateUserEmail(ctx, u.ID, email); err != nil {
		t.Fatal(err)
	}
	if confirmed {
		if err := st.CreateEmailVerification(ctx, u.ID, email, "hash-"+u.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := st.ConfirmEmailVerification(ctx, "hash-"+u.ID); err != nil {
			t.Fatal(err)
		}
	}
	return u.ID
}

// Through the shared service an unconfirmed address is not even queued: the
// outbox never holds mail it could not send.
func TestOperatorServiceQueuesOnlyConfirmedRecipientsIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	agent, err := st.CreateAgent(ctx, "host-queue", "linux", "amd64", "1.0.0", "tok-queue", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	newRecipient(t, ctx, st, "yes@example.test", true)
	newRecipient(t, ctx, st, "no@example.test", false)
	op := Config{Host: "127.0.0.1", Port: 1, From: "alerts@notify.test", TLSMode: "none", Operator: true, HourlyLimit: 100}
	svc := New(st, func(context.Context) (Config, string, error) { return op, "https://acme.openrmm.io", nil })
	if err := svc.Queue(ctx, Event{Type: store.NotifyAgentOffline, AgentID: agent.ID, Subject: "s", Body: "b"}); err != nil {
		t.Fatal(err)
	}
	queued, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 || queued[0].Recipient != "yes@example.test" {
		got := []string{}
		for _, n := range queued {
			got = append(got, n.Recipient)
		}
		t.Fatalf("queued %v, want only the confirmed recipient", got)
	}
}

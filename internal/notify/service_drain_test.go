package notify

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
	"github.com/p1ngu/rmm/internal/store/storetest"
)

// drain is where the log decisions actually live. setQuiet, newError and
// quietReason are unit-tested, but nothing checked *when* drain calls them —
// inverting the first-attempt condition, or dropping the setQuiet call
// altogether, would leave every other test passing and put back the silence
// these lines exist to remove.

// deadSMTP is a listener that accepts a connection and immediately closes it, so
// a send fails fast and predictably. It doubles as a record of *which* transport
// was dialled: the port is the identity.
type deadSMTP struct {
	addr  string
	port  int
	mu    sync.Mutex
	conns int
}

func newDeadSMTP(t *testing.T) *deadSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	d := &deadSMTP{addr: ln.Addr().String(), port: ln.Addr().(*net.TCPAddr).Port}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			d.mu.Lock()
			d.conns++
			d.mu.Unlock()
			_ = conn.Close()
		}
	}()
	return d
}

func (d *deadSMTP) dialled() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns
}

func drainTestStore(t *testing.T) (context.Context, *store.Store) {
	t.Helper()
	databaseURL := os.Getenv("RMM_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("RMM_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx, storetest.NewSchema(t, "notify_test", databaseURL)
}

func captureDrainLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetFlags(flags) })
	return &buf
}

func enqueue(t *testing.T, ctx context.Context, st *store.Store, to string) {
	t.Helper()
	if err := st.EnqueueNotification(ctx, &models.Notification{
		EventType: store.NotifyAgentOffline, Recipient: to, Subject: "s", Body: "b",
	}); err != nil {
		t.Fatal(err)
	}
}

// An unusable transport leaves rows pending — and now says so, once. This is the
// case that used to be completely silent: a half-filled profile stopped every
// alert with nothing in the log to explain it.
func TestDrainSpeaksWhenNothingCanBeDeliveredIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	enqueue(t, ctx, st, "someone@example.test")
	buf := captureDrainLog(t)

	// A host with no sender: configured, but not deliverable.
	s := New(st, func(context.Context) (Config, string, error) {
		return Config{Host: "relay.example.test", Port: 587, TLSMode: "starttls"}, "", nil
	})
	s.drain(ctx)
	first := buf.String()
	if !strings.Contains(first, "no sender address") {
		t.Fatalf("the reason should name the misconfiguration, got %q", first)
	}
	if !strings.Contains(first, "relay.example.test") {
		t.Errorf("the reason should name the host, got %q", first)
	}

	// Every later tick stays quiet, or the one line that matters is buried.
	buf.Reset()
	s.drain(ctx)
	s.drain(ctx)
	if buf.Len() != 0 {
		t.Errorf("repeat drains should not log, got %q", buf.String())
	}

	// The message is untouched: not delivered, not failed, still due.
	stats, err := st.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Failed != 0 {
		t.Errorf("pending=%d failed=%d, want the row left pending", stats.Pending, stats.Failed)
	}
}

// The first failed send is logged, not only the fifth. With 2/4/8/16 minutes of
// backoff, waiting for the last attempt meant roughly half an hour in which a
// dead relay produced no log line at all.
func TestDrainLogsTheFirstFailureIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	relay := newDeadSMTP(t)
	enqueue(t, ctx, st, "someone@example.test")
	buf := captureDrainLog(t)

	s := New(st, func(context.Context) (Config, string, error) {
		return Config{
			Host: "127.0.0.1", Port: relay.port, From: "rmm@example.test",
			TLSMode: "none", Timeout: 5 * time.Second,
		}, "", nil
	})
	s.drain(ctx)

	logged := buf.String()
	if !strings.Contains(logged, "attempt 1/5") {
		t.Fatalf("the first attempt should be logged, got %q", logged)
	}
	if !strings.Contains(logged, "retrying in") {
		t.Errorf("the line should say when it will retry, got %q", logged)
	}
	if !strings.Contains(logged, "127.0.0.1") {
		t.Errorf("the line should name the transport, got %q", logged)
	}
	if strings.Contains(logged, "giving up") {
		t.Errorf("attempt 1 is not the last one, got %q", logged)
	}
	if relay.dialled() == 0 {
		t.Error("the configured relay was never dialled")
	}

	// The row is rescheduled rather than abandoned, and carries the reason.
	stats, err := st.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Failed != 0 {
		t.Errorf("pending=%d failed=%d, want it retried, not abandoned", stats.Pending, stats.Failed)
	}
}

// A relay down for hours produces one line, not one per attempt per message.
func TestDrainSuppressesRepeatedIdenticalFailuresIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	relay := newDeadSMTP(t)
	for i := 0; i < 3; i++ {
		enqueue(t, ctx, st, fmt.Sprintf("user%d@example.test", i))
	}
	buf := captureDrainLog(t)

	s := New(st, func(context.Context) (Config, string, error) {
		return Config{
			Host: "127.0.0.1", Port: relay.port, From: "rmm@example.test",
			TLSMode: "none", Timeout: 5 * time.Second,
		}, "", nil
	})
	s.drain(ctx)

	// Three messages, all failing the same way on their first attempt: the first
	// speaks, the rest are the same news.
	if got := strings.Count(buf.String(), "attempt 1/5"); got != 1 {
		t.Errorf("logged %d failure lines for one identical outage, want 1:\n%s", got, buf.String())
	}
}

// Recovery is worth one line too: an operator watching the log should see the
// outage close, not have to infer it from silence.
func TestDrainLogsRecoveryIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	buf := captureDrainLog(t)
	relay := newDeadSMTP(t)

	var usable bool
	s := New(st, func(context.Context) (Config, string, error) {
		if !usable {
			return Config{}, "", nil // nothing configured
		}
		return Config{
			Host: "127.0.0.1", Port: relay.port, From: "rmm@example.test",
			TLSMode: "none", Timeout: 5 * time.Second,
		}, "", nil
	})

	s.drain(ctx)
	if !strings.Contains(buf.String(), "no mail transport is configured") {
		t.Fatalf("expected the quiet notice, got %q", buf.String())
	}
	buf.Reset()

	usable = true
	s.drain(ctx)
	if !strings.Contains(buf.String(), "usable again") {
		t.Errorf("recovery should be logged, got %q", buf.String())
	}
}

// Exhausting the budget is terminal and says so, naming the relay — which
// matters now that alerts and account mail can leave through different ones.
func TestDrainGivingUpNamesTheRelayIntegration(t *testing.T) {
	ctx, st := drainTestStore(t)
	relay := newDeadSMTP(t)
	enqueue(t, ctx, st, "someone@example.test")
	buf := captureDrainLog(t)

	s := New(st, func(context.Context) (Config, string, error) {
		return Config{
			Host: "127.0.0.1", Port: relay.port, From: "rmm@example.test",
			TLSMode: "none", Timeout: 5 * time.Second,
		}, "", nil
	})
	// Spend the attempt budget without waiting out the real backoff. Every claim
	// now carries a durable lease, so release that lease through the same fenced
	// failure path the worker uses. The drain below is then the final attempt.
	for i := 0; i < maxAttempts-1; i++ {
		claimed, err := st.ClaimPendingNotificationsLeased(ctx, 10, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if len(claimed) != 1 || claimed[0].LeaseToken == nil {
			t.Fatalf("attempt %d claim = %#v", i+1, claimed)
		}
		if err := st.MarkNotificationFailedLeased(ctx, claimed[0].ID, *claimed[0].LeaseToken,
			claimed[0].Attempts, maxAttempts, "preloaded failure", 0); err != nil {
			t.Fatal(err)
		}
	}
	s.drain(ctx)

	logged := buf.String()
	if !strings.Contains(logged, "giving up") {
		t.Fatalf("the attempt budget should end in a give-up line, got %q", logged)
	}
	if !strings.Contains(logged, "127.0.0.1") {
		t.Errorf("the give-up line should name the relay, got %q", logged)
	}
	stats, err := st.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 || stats.Pending != 0 {
		t.Errorf("failed=%d pending=%d, want the message abandoned", stats.Failed, stats.Pending)
	}
}

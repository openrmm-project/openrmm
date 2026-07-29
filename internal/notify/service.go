package notify

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/p1ngu/rmm/internal/models"
	"github.com/p1ngu/rmm/internal/store"
)

const (
	maxAttempts   = 5
	claimBatch    = 25
	baseBackoff   = 2 * time.Minute
	workerCadence = 30 * time.Second
)

// ConfigFunc resolves the current SMTP settings. It is a function, not a value,
// because settings are DB-backed and editable at runtime: a worker that captured
// the config at startup would keep using the password an operator just fixed.
type ConfigFunc func(ctx context.Context) (Config, string, error) // config, console URL, error

// Service turns state transitions into queued mail and drains the outbox.
type Service struct {
	store  *store.Store
	config ConfigFunc
}

func New(st *store.Store, cfg ConfigFunc) *Service { return &Service{store: st, config: cfg} }

// Event describes something worth telling an operator about.
type Event struct {
	Type     string
	AgentID  string
	Hostname string
	AlertID  string
	Subject  string
	Body     string
}

// Queue fans an event out to every in-scope subscriber.
//
// Callers invoke this only on a transition (an alert that just opened or just
// resolved), never on every evaluation — see store.RaiseAlert's opened flag.
func (s *Service) Queue(ctx context.Context, ev Event) {
	if s == nil || ev.AgentID == "" {
		return
	}
	cfg, _, err := s.config(ctx)
	if err != nil || !cfg.Enabled() {
		return // email not configured: alerts still land in the console
	}
	recipients, err := s.store.RecipientsForAgentEvent(ctx, ev.AgentID, ev.Type, store.DefaultSubscribed(ev.Type))
	if err != nil {
		log.Printf("notify: recipients for %s: %v", ev.Type, err)
		return
	}
	for _, r := range recipients {
		userID, alertID := r.UserID, ev.AlertID
		n := &models.Notification{
			EventType: ev.Type,
			AgentID:   &ev.AgentID,
			UserID:    &userID,
			Recipient: r.Email,
			Subject:   ev.Subject,
			Body:      ev.Body,
		}
		if alertID != "" {
			n.AlertID = &alertID
		}
		if err := s.store.EnqueueNotification(ctx, n); err != nil {
			log.Printf("notify: enqueue for %s: %v", r.Email, err)
		}
	}
}

// RunWorker drains the outbox until ctx is done.
func (s *Service) RunWorker(ctx context.Context) {
	ticker := time.NewTicker(workerCadence)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.drain(ctx)
		}
	}
}

func (s *Service) drain(ctx context.Context) {
	cfg, _, err := s.config(ctx)
	if err != nil || !cfg.Enabled() {
		return // leave rows pending; they go out once SMTP is configured
	}
	pending, err := s.store.ClaimPendingNotifications(ctx, claimBatch)
	if err != nil {
		log.Printf("notify: claim: %v", err)
		return
	}
	for _, n := range pending {
		sendErr := Send(cfg, Message{To: n.Recipient, Subject: n.Subject, Body: n.Body})
		if sendErr == nil {
			if err := s.store.MarkNotificationSent(ctx, n.ID); err != nil {
				log.Printf("notify: mark sent %s: %v", n.ID, err)
			}
			continue
		}
		// Exponential backoff: a greylisting server (very common) rejects the
		// first attempt on purpose and accepts a later retry.
		backoff := baseBackoff * time.Duration(1<<min(n.Attempts, 4))
		reason := truncate(sendErr.Error(), 500)
		if err := s.store.MarkNotificationFailed(ctx, n.ID, n.Attempts, maxAttempts, reason, backoff); err != nil {
			log.Printf("notify: mark failed %s: %v", n.ID, err)
		}
		if n.Attempts >= maxAttempts {
			log.Printf("notify: giving up on %s after %d attempts: %s", n.ID, n.Attempts, reason)
		}
	}
}

// AlertOpenedEvent renders the mail for a newly opened alert.
func AlertOpenedEvent(eventType, agentID, hostname, alertID, detail, consoleURL string) Event {
	subject := fmt.Sprintf("[OpenRMM] %s — %s", labelFor(eventType), hostname)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", detail)
	fmt.Fprintf(&b, "Device: %s\n", hostname)
	fmt.Fprintf(&b, "Detected: %s\n", time.Now().UTC().Format(time.RFC3339))
	if consoleURL != "" {
		fmt.Fprintf(&b, "\nOpen the console: %s\n", strings.TrimRight(consoleURL, "/"))
	}
	b.WriteString("\nYou receive this because the device is inside your access scope.\n")
	b.WriteString("Change what you are notified about in Users & Settings.\n")
	return Event{Type: eventType, AgentID: agentID, Hostname: hostname, AlertID: alertID,
		Subject: subject, Body: b.String()}
}

// QueueJobFailed mails a failed job. It satisfies jobs.Notifier.
//
// Unlike a check, a job has no open/resolve cycle to key on — a failure is a
// one-off event — which is exactly why job_failed is opt-in by default: a bad
// script pushed to the fleet would otherwise mail once per device.
func (s *Service) QueueJobFailed(ctx context.Context, agentID, hostname, jobID, description string) {
	if s == nil || agentID == "" {
		return
	}
	if hostname == "" {
		hostname = agentID
	}
	if description == "" {
		description = "(no description)"
	}
	_, consoleURL, err := s.config(ctx)
	if err != nil {
		consoleURL = ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "A job failed on %s.\n\n", hostname)
	fmt.Fprintf(&b, "Job: %s\nJob ID: %s\nFailed: %s\n",
		description, jobID, time.Now().UTC().Format(time.RFC3339))
	if consoleURL != "" {
		fmt.Fprintf(&b, "\nOpen the console: %s\n", strings.TrimRight(consoleURL, "/"))
	}
	s.Queue(ctx, Event{
		Type:     store.NotifyJobFailed,
		AgentID:  agentID,
		Hostname: hostname,
		Subject:  fmt.Sprintf("[OpenRMM] %s — %s", labelFor(store.NotifyJobFailed), hostname),
		Body:     b.String(),
	})
}

// AlertResolvedEvent renders the recovery mail, which on-call values more than
// the original failure notice.
func AlertResolvedEvent(agentID, hostname, detail, consoleURL string) Event {
	subject := fmt.Sprintf("[OpenRMM] Recovered — %s", hostname)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nDevice: %s\nRecovered: %s\n",
		detail, hostname, time.Now().UTC().Format(time.RFC3339))
	if consoleURL != "" {
		fmt.Fprintf(&b, "\nOpen the console: %s\n", strings.TrimRight(consoleURL, "/"))
	}
	return Event{Type: store.NotifyCheckRecovered, AgentID: agentID, Hostname: hostname,
		Subject: subject, Body: b.String()}
}

func labelFor(eventType string) string {
	switch eventType {
	case store.NotifyCheckFailed:
		return "Check failing"
	case store.NotifyAgentOffline:
		return "Agent offline"
	case store.NotifyJobFailed:
		return "Job failed"
	case store.NotifyCheckRecovered:
		return "Recovered"
	}
	return "Alert"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

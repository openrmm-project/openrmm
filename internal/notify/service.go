package notify

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
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

	// Transition state for the worker's log lines. The worker runs every 30s, so
	// anything logged unconditionally would be noise; these let it speak once
	// when the situation changes and then stay quiet.
	mu        sync.Mutex
	quiet     bool // last drain found no usable transport
	lastError string
}

func New(st *store.Store, cfg ConfigFunc) *Service { return &Service{store: st, config: cfg} }

// Event describes something worth telling an operator about.
type Event struct {
	Type      string
	AgentID   string
	Hostname  string
	AlertID   string
	Subject   string
	Body      string
	DedupeKey string
}

// Queue fans an event out to every in-scope subscriber.
//
// Callers invoke this only on a transition (an alert that just opened or just
// resolved), never on every evaluation — see store.RaiseAlert's opened flag.
func (s *Service) Queue(ctx context.Context, ev Event) error {
	if s == nil || ev.AgentID == "" {
		return nil
	}
	var recipients []store.NotificationRecipient
	var err error
	if ev.AlertID != "" {
		recipients, err = s.store.RecipientsForAlertEvent(ctx, ev.AlertID, ev.Type, store.DefaultSubscribed(ev.Type))
	} else {
		recipients, err = s.store.RecipientsForAgentEvent(ctx, ev.AgentID, ev.Type, store.DefaultSubscribed(ev.Type))
	}
	if err != nil {
		log.Printf("notify: recipients for %s: %v", ev.Type, err)
		return err
	}
	dedupeKey := ev.DedupeKey
	if dedupeKey == "" {
		dedupeKey = eventDedupeKey(ev.Type, ev.AlertID, ev.Subject, ev.Body)
	}
	var queueErr error
	for _, r := range recipients {
		userID, alertID := r.UserID, ev.AlertID
		n := &models.Notification{
			EventType: ev.Type,
			AgentID:   &ev.AgentID,
			UserID:    &userID,
			Recipient: r.Email,
			Subject:   ev.Subject,
			Body:      ev.Body,
			DedupeKey: dedupeKey + ":" + r.UserID,
		}
		if alertID != "" {
			n.AlertID = &alertID
		}
		if err := s.store.EnqueueNotification(ctx, n); err != nil {
			log.Printf("notify: enqueue for %s: %v", r.Email, err)
			queueErr = errors.Join(queueErr, err)
		}
	}
	return queueErr
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
		// Rows are left pending — they go out once SMTP is configured — but this
		// used to be entirely silent, which meant a half-configured alerts
		// profile (a host with no sender, say) stopped all alert mail without
		// ever producing a log line. Said once, on the way in and on the way out.
		s.setQuiet(true, quietReason(cfg, err))
		return
	}
	s.setQuiet(false, "")
	pending, err := s.store.ClaimPendingNotificationsLeased(ctx, claimBatch, 5*time.Minute)
	if err != nil {
		log.Printf("notify: claim: %v", err)
		return
	}
	for _, n := range pending {
		if n.LeaseToken == nil {
			log.Printf("notify: claimed message %s without a lease token", n.ID)
			continue
		}
		sendErr := Send(cfg, Message{To: n.Recipient, Subject: n.Subject, Body: n.Body})
		if sendErr == nil {
			if err := s.store.MarkNotificationSentLeased(ctx, n.ID, *n.LeaseToken); err != nil {
				log.Printf("notify: mark sent %s: %v", n.ID, err)
			}
			// Mail is moving again, so the next failure is news rather than a
			// repeat of an outage that has since ended.
			s.clearError()
			continue
		}
		// Exponential backoff: a greylisting server (very common) rejects the
		// first attempt on purpose and accepts a later retry.
		backoff := baseBackoff * time.Duration(1<<min(max(n.Attempts-1, 0), 4))
		reason := truncate(sendErr.Error(), 500)
		if err := s.store.MarkNotificationFailedLeased(ctx, n.ID, *n.LeaseToken,
			n.Attempts, maxAttempts, reason, backoff); err != nil {
			log.Printf("notify: mark failed %s: %v", n.ID, err)
		}
		// The first failure of an outage is logged, not only the fifth attempt:
		// waiting for the last one meant ~30 minutes of backoff during which a
		// dead relay produced no log line at all.
		//
		// Keyed on the error rather than on the message's attempt count. One
		// outage hits every queued message at once, so "first attempt" would put
		// a line in the log per message — the flood this is meant to avoid. A
		// different error is different news and speaks again, as does the same
		// error after mail has flowed in between.
		if s.newError(reason) {
			log.Printf("notify: send via %s failed (attempt %d/%d, retrying in %s): %s",
				cfg.Host, n.Attempts, maxAttempts, backoff, reason)
		}
		if n.Attempts >= maxAttempts {
			log.Printf("notify: giving up on %s via %s after %d attempts: %s", n.ID, cfg.Host, n.Attempts, reason)
		}
	}
}

// setQuiet logs the transition into and out of "nothing can be delivered".
//
// Only the transition, because the worker ticks every 30s: a line per tick would
// bury the one that matters, and no line at all is what let a misconfiguration
// stop every alert in silence.
func (s *Service) setQuiet(quiet bool, reason string) {
	s.mu.Lock()
	was := s.quiet
	s.quiet = quiet
	if quiet {
		s.lastError = ""
	}
	s.mu.Unlock()
	switch {
	case quiet && !was:
		log.Printf("notify: %s — alert mail is queued but not being delivered", reason)
	case !quiet && was:
		log.Print("notify: mail transport is usable again; draining the outbox")
	}
}

// newError reports whether this failure differs from the last one logged, so a
// relay that is down for hours produces one line rather than one per attempt of
// every queued message.
func (s *Service) newError(reason string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastError == reason {
		return false
	}
	s.lastError = reason
	return true
}

// clearError forgets the last logged failure, so an identical error in a later
// outage is reported instead of being mistaken for the same one still running.
func (s *Service) clearError() {
	s.mu.Lock()
	s.lastError = ""
	s.mu.Unlock()
}

// quietReason turns an unusable transport into something an operator can act on.
// "not configured" and "configured wrong" call for different fixes.
func quietReason(cfg Config, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("cannot read the mail settings: %v", err)
	case cfg.Host == "" && cfg.From == "":
		return "no mail transport is configured"
	case cfg.Host == "":
		return fmt.Sprintf("a sender (%s) is set but no host is", cfg.From)
	default:
		return fmt.Sprintf("host %s is set but no sender address is", cfg.Host)
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
	body := b.String()
	return Event{Type: eventType, AgentID: agentID, Hostname: hostname, AlertID: alertID,
		Subject: subject, Body: body, DedupeKey: eventDedupeKey(eventType, alertID, subject, detail)}
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
		Type:      store.NotifyJobFailed,
		AgentID:   agentID,
		Hostname:  hostname,
		Subject:   fmt.Sprintf("[OpenRMM] %s — %s", labelFor(store.NotifyJobFailed), hostname),
		Body:      b.String(),
		DedupeKey: "job_failed:" + jobID,
	})
}

// SendPasswordReset delivers a reset link directly, deliberately bypassing the
// outbox that every alert goes through.
//
// Three reasons it must not be queued. The outbox persists `body` and is never
// pruned, so a reset link would sit in the database forever — defeating the
// point of storing only the token's hash. The worker drains on a 30s cadence
// with minutes of backoff, long enough that a user requests a second link before
// the first arrives. And Queue fans out by device scope and subscription, so a
// message with no agent is dropped outright and a readonly account — which lacks
// alerts.view — could never reset its password.
//
// Callers run this off the request path: it blocks for the SMTP timeout, and the
// requesting endpoint must not reveal, by how long it takes, whether the address
// belongs to an account.
func SendPasswordReset(cfg Config, to, username, resetURL string, ttl time.Duration) error {
	var b strings.Builder
	fmt.Fprintf(&b, "A password reset was requested for %s.\n\n", username)
	fmt.Fprintf(&b, "Open this link to choose a new password:\n%s\n\n", resetURL)
	fmt.Fprintf(&b, "The link expires in %s and can be used once.\n\n", ttl.Round(time.Minute))
	b.WriteString("If you did not request this, you can ignore this message — ")
	b.WriteString("your password stays unchanged and the link remains unused.\n")
	return Send(cfg, Message{To: to, Subject: "[OpenRMM] Reset your password", Body: b.String()})
}

// AlertResolvedEvent renders the recovery mail, which on-call values more than
// the original failure notice.
func AlertResolvedEvent(eventType, agentID, hostname, alertID, detail, consoleURL string) Event {
	subject := fmt.Sprintf("[OpenRMM] Recovered — %s", hostname)
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\nDevice: %s\nRecovered: %s\n",
		detail, hostname, time.Now().UTC().Format(time.RFC3339))
	if consoleURL != "" {
		fmt.Fprintf(&b, "\nOpen the console: %s\n", strings.TrimRight(consoleURL, "/"))
	}
	body := b.String()
	return Event{Type: eventType, AgentID: agentID, Hostname: hostname, AlertID: alertID,
		Subject: subject, Body: body, DedupeKey: eventDedupeKey(eventType, alertID, subject, detail)}
}

func labelFor(eventType string) string {
	switch eventType {
	case store.NotifyCheckFailed:
		return "Check failing"
	case store.NotifyAgentOffline:
		return "Agent offline"
	case store.NotifyAgentRecovered:
		return "Agent recovered"
	case store.NotifyJobFailed:
		return "Job failed"
	case store.NotifyCheckRecovered:
		return "Recovered"
	}
	return "Alert"
}

func eventDedupeKey(eventType, alertID, subject, body string) string {
	sum := sha256.Sum256([]byte(subject + "\x00" + body))
	return fmt.Sprintf("%s:%s:%x", eventType, alertID, sum[:8])
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

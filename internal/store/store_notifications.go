package store

import (
	"context"
	"time"

	"github.com/p1ngu/rmm/internal/models"
)

// Event types an operator can subscribe to individually.
const (
	NotifyCheckFailed    = "check_failed"
	NotifyCheckRecovered = "check_recovered"
	NotifyAgentOffline   = "agent_offline"
	NotifyJobFailed      = "job_failed"
)

// NotificationEventTypes is the full set, in the order the preferences screen
// shows them. Anything outside it is rejected rather than stored, so a typo does
// not become a subscription row nobody will ever match.
var NotificationEventTypes = []string{
	NotifyCheckFailed, NotifyCheckRecovered, NotifyAgentOffline, NotifyJobFailed,
}

// ValidNotificationEvent reports whether an event type is one we deliver.
func ValidNotificationEvent(eventType string) bool {
	_, ok := defaultSubscribed[eventType]
	return ok
}

// defaultSubscribed decides what a user with no explicit preference receives.
// Failed jobs are the noisiest signal (a bad script fans out across the fleet),
// so they are opt-in while the health signals are opt-out.
var defaultSubscribed = map[string]bool{
	NotifyCheckFailed:    true,
	NotifyCheckRecovered: true,
	NotifyAgentOffline:   true,
	NotifyJobFailed:      false,
}

// DefaultSubscribed reports the out-of-the-box preference for an event type.
func DefaultSubscribed(eventType string) bool { return defaultSubscribed[eventType] }

// NotificationRecipient is one user who should receive an event.
type NotificationRecipient struct {
	UserID string
	Email  string
}

// RecipientsForAgentEvent returns the users who may be told about agentID.
//
// The scope predicate is deliberately the same one that authorizes reading a
// device (AgentAllowedForUser): no scope rows means full access, otherwise the
// agent must match one of the user's agent/group/site/client scopes. Deriving
// recipients from that predicate instead of a configured address list makes it
// impossible to email one client's device names to another client's contact —
// the worst failure mode this feature could have.
//
// A user needs an address, an enabled subscription (or a default-on event), and
// the alerts.view permission their role carries. That last condition is why a
// readonly account with an address stays silent: mailing someone an alert they
// cannot open in the console is a leak, not a notification. role_permissions is
// authoritative here because system roles cannot be edited, so it never drifts
// from models.DefaultRolePermissions.
func (s *Store) RecipientsForAgentEvent(ctx context.Context, agentID, eventType string, defaultEnabled bool) ([]NotificationRecipient, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email
		FROM users u
		LEFT JOIN notification_subscriptions ns
		       ON ns.user_id = u.id AND ns.event_type = $2
		WHERE u.email IS NOT NULL AND u.email <> ''
		  AND u.disabled_at IS NULL
		  AND COALESCE(ns.enabled, $3)
		  AND EXISTS (
		        SELECT 1 FROM role_permissions rp
		        WHERE rp.role_key = u.role AND rp.permission = 'alerts.view'
		      )
		  AND (
		        NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id = u.id)
		     OR EXISTS (
		          SELECT 1 FROM user_scopes us WHERE us.user_id = u.id AND (
		            (us.scope_type='agent'  AND us.scope_id = $1) OR
		            (us.scope_type='group'  AND us.scope_id = (SELECT group_id FROM agents WHERE id = $1)) OR
		            (us.scope_type='site'   AND us.scope_id = (SELECT site_id  FROM agents WHERE id = $1)) OR
		            (us.scope_type='client' AND us.scope_id = (SELECT s.client_id FROM agents a
		                                                        JOIN sites s ON s.id = a.site_id
		                                                       WHERE a.id = $1))
		          )
		        )
		  )`, agentID, eventType, defaultEnabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationRecipient
	for rows.Next() {
		var r NotificationRecipient
		if err := rows.Scan(&r.UserID, &r.Email); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// EnqueueNotification adds one message to the outbox.
func (s *Store) EnqueueNotification(ctx context.Context, n *models.Notification) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO notifications (id, event_type, alert_id, agent_id, user_id, recipient, subject, body)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		newID(), n.EventType, n.AlertID, n.AgentID, n.UserID, n.Recipient, n.Subject, n.Body)
	return err
}

// ClaimPendingNotifications locks a batch of due messages for this worker.
//
// FOR UPDATE SKIP LOCKED is what lets a second server replica run the same
// worker without sending anything twice.
func (s *Store) ClaimPendingNotifications(ctx context.Context, limit int) ([]models.Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM notifications
			WHERE status = 'pending' AND next_attempt_at <= now()
			ORDER BY next_attempt_at
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notifications n SET attempts = n.attempts + 1
		FROM due WHERE n.id = due.id
		RETURNING n.id, n.event_type, n.alert_id, n.agent_id, n.user_id, n.recipient,
		          n.subject, n.body, n.attempts`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Notification
	for rows.Next() {
		var n models.Notification
		if err := rows.Scan(&n.ID, &n.EventType, &n.AlertID, &n.AgentID, &n.UserID,
			&n.Recipient, &n.Subject, &n.Body, &n.Attempts); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkNotificationSent closes a message out successfully.
func (s *Store) MarkNotificationSent(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET status='sent', sent_at=now(), last_error='' WHERE id=$1`, id)
	return err
}

// MarkNotificationFailed reschedules a message, or gives up after maxAttempts.
// Giving up is deliberate: an address that bounces forever must not keep a
// worker busy, and the row stays as the record of what was not delivered.
func (s *Store) MarkNotificationFailed(ctx context.Context, id string, attempts, maxAttempts int, reason string, backoff time.Duration) error {
	if attempts >= maxAttempts {
		_, err := s.pool.Exec(ctx,
			`UPDATE notifications SET status='failed', last_error=$2 WHERE id=$1`, id, reason)
		return err
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET last_error=$2, next_attempt_at = now() + $3::interval WHERE id=$1`,
		id, reason, backoff.String())
	return err
}

// OutboxStats summarizes the notification queue for the metrics endpoint.
type OutboxStats struct {
	Pending             int
	Failed              int
	OldestPendingAgeSec float64
}

// OutboxStats reports whether alert mail is actually leaving.
//
// This is the signal that makes a broken relay detectable from outside: alert
// delivery failing is the failure that hides every other failure, and until
// these counters existed it could only be noticed by not receiving mail — which
// is exactly what nobody notices. Age of the oldest pending row is the useful
// one to alert on: a healthy outbox drains within a worker tick, so a row that
// has waited many minutes means delivery has stopped, whatever the cause.
func (s *Store) OutboxStats(ctx context.Context) (OutboxStats, error) {
	var out OutboxStats
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'pending'),
			count(*) FILTER (WHERE status = 'failed'),
			COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at) FILTER (WHERE status = 'pending'))), 0)
		FROM notifications`).Scan(&out.Pending, &out.Failed, &out.OldestPendingAgeSec)
	return out, err
}

// ListNotificationSubscriptions returns a user's explicit preferences.
func (s *Store) ListNotificationSubscriptions(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT event_type, enabled FROM notification_subscriptions WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var eventType string
		var enabled bool
		if err := rows.Scan(&eventType, &enabled); err != nil {
			return nil, err
		}
		out[eventType] = enabled
	}
	return out, rows.Err()
}

// SetNotificationSubscription records an explicit opt-in or opt-out.
func (s *Store) SetNotificationSubscription(ctx context.Context, userID, eventType string, enabled bool) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO notification_subscriptions (user_id, event_type, enabled)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (user_id, event_type)
		 DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now()`,
		userID, eventType, enabled)
	return err
}

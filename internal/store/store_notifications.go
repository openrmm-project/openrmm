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
	NotifyAgentRecovered = "agent_recovered"
	NotifyJobFailed      = "job_failed"
)

// NotificationEventTypes is the full set, in the order the preferences screen
// shows them. Anything outside it is rejected rather than stored, so a typo does
// not become a subscription row nobody will ever match.
var NotificationEventTypes = []string{
	NotifyCheckFailed, NotifyCheckRecovered, NotifyAgentOffline, NotifyAgentRecovered, NotifyJobFailed,
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
	NotifyAgentRecovered: true,
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

// RecipientsForAlertEvent applies the immutable scope snapshot captured when
// the incident opened. Looking at the agent's current site/group here could
// disclose an older tenant's incident after the device was moved, and would
// make delivery impossible after the live device row was deleted.
func (s *Store) RecipientsForAlertEvent(ctx context.Context, alertID, eventType string, defaultEnabled bool) ([]NotificationRecipient, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT u.id, u.email
		FROM alerts al
		JOIN users u ON true
		LEFT JOIN notification_subscriptions ns
		       ON ns.user_id=u.id AND ns.event_type=$2
		WHERE al.id=$1
		  AND u.email IS NOT NULL AND u.email<>''
		  AND u.disabled_at IS NULL
		  AND COALESCE(ns.enabled, $3)
		  AND EXISTS (
		        SELECT 1 FROM role_permissions rp
		        WHERE rp.role_key=u.role AND rp.permission='alerts.view'
		      )
		  AND (
		        NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=u.id)
		     OR EXISTS (
		          SELECT 1 FROM user_scopes us WHERE us.user_id=u.id AND (
		            (us.scope_type='agent'  AND us.scope_id=al.agent_snapshot_id) OR
		            (us.scope_type='group'  AND us.scope_id=al.group_id) OR
		            (us.scope_type='site'   AND us.scope_id=al.site_id) OR
		            (us.scope_type='client' AND us.scope_id=al.client_id)
		          )
		        )
		  )`, alertID, eventType, defaultEnabled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotificationRecipient
	for rows.Next() {
		var recipient NotificationRecipient
		if err := rows.Scan(&recipient.UserID, &recipient.Email); err != nil {
			return nil, err
		}
		out = append(out, recipient)
	}
	return out, rows.Err()
}

// EnqueueNotification adds one message to the outbox.
func (s *Store) EnqueueNotification(ctx context.Context, n *models.Notification) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO notifications
		 (id, event_type, alert_id, agent_id, user_id, recipient, subject, body, dedupe_key)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		 ON CONFLICT (dedupe_key) WHERE dedupe_key <> '' DO NOTHING`,
		newID(), n.EventType, n.AlertID, n.AgentID, n.UserID, n.Recipient,
		n.Subject, n.Body, n.DedupeKey)
	return err
}

// ClaimPendingNotifications locks a batch of due messages for this worker.
//
// FOR UPDATE SKIP LOCKED is what lets a second server replica run the same
// worker without sending anything twice.
func (s *Store) ClaimPendingNotifications(ctx context.Context, limit int) ([]models.Notification, error) {
	return s.ClaimPendingNotificationsLeased(ctx, limit, 5*time.Minute)
}

// ClaimPendingNotificationsLeased moves due rows to processing under a lease.
// SKIP LOCKED protects the claim transaction; the persisted lease protects the
// much longer SMTP send after that transaction commits.
func (s *Store) ClaimPendingNotificationsLeased(ctx context.Context, limit int, lease time.Duration) ([]models.Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if lease < time.Minute {
		lease = time.Minute
	}
	rows, err := s.pool.Query(ctx, `
		WITH due AS (
			SELECT id FROM notifications
			WHERE (status='pending' AND next_attempt_at <= now())
			   OR (status='processing' AND lease_until <= now())
			ORDER BY COALESCE(lease_until, next_attempt_at)
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE notifications n SET
			status='processing', attempts=n.attempts+1,
			lease_token=gen_random_uuid(), lease_until=now()+$2::interval,
			updated_at=now()
		FROM due WHERE n.id = due.id
		RETURNING n.id, n.event_type, n.alert_id, n.agent_id, n.user_id, n.recipient,
		          n.subject, n.body, n.attempts, n.dedupe_key, n.lease_token`, limit, lease.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Notification
	for rows.Next() {
		var n models.Notification
		if err := rows.Scan(&n.ID, &n.EventType, &n.AlertID, &n.AgentID, &n.UserID,
			&n.Recipient, &n.Subject, &n.Body, &n.Attempts, &n.DedupeKey, &n.LeaseToken); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) MarkNotificationSentLeased(ctx context.Context, id, leaseToken string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE notifications SET status='sent', sent_at=now(),
		last_error='', lease_token=NULL, lease_until=NULL, updated_at=now()
		WHERE id=$1 AND status='processing' AND lease_token=$2`, id, leaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// MarkNotificationFailedLeased reschedules a message, or gives up after
// maxAttempts. The lease token fences a worker whose claim expired while SMTP
// was blocked from overwriting the result of the worker that reclaimed it.
func (s *Store) MarkNotificationFailedLeased(ctx context.Context, id, leaseToken string, attempts, maxAttempts int, reason string, backoff time.Duration) error {
	status := "pending"
	if attempts >= maxAttempts {
		status = "failed"
	}
	tag, err := s.pool.Exec(ctx, `UPDATE notifications SET status=$3, last_error=$4,
		next_attempt_at=CASE WHEN $3='pending' THEN now()+$5::interval ELSE next_attempt_at END,
		lease_token=NULL, lease_until=NULL, updated_at=now()
		WHERE id=$1 AND status='processing' AND lease_token=$2`,
		id, leaseToken, status, reason, backoff.String())
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// OutboxStats summarizes the notification queue for the metrics endpoint.
type OutboxStats struct {
	Pending             int
	Processing          int
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
			count(*) FILTER (WHERE status = 'processing'),
			count(*) FILTER (WHERE status = 'failed'),
			COALESCE(EXTRACT(EPOCH FROM (now() - min(created_at) FILTER (WHERE status IN ('pending','processing')))), 0)
		FROM notifications`).Scan(&out.Pending, &out.Processing, &out.Failed, &out.OldestPendingAgeSec)
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

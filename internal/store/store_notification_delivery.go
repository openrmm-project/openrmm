package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/p1ngu/rmm/internal/models"
)

func nullableDeliveryContext(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return raw
}

func notificationString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func notificationUUID(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func alertDeliveryContext(a *models.Alert, eventType string) models.NotificationDeliveryContext {
	return models.NotificationDeliveryContext{Version: 1, EventType: eventType,
		AgentID: notificationString(a.AgentSnapshotID), ClientID: notificationString(a.ClientID),
		SiteID: notificationString(a.SiteID), GroupID: notificationString(a.GroupID),
		AlertID: a.ID, NotificationSeq: a.NotificationSeq, SourceType: a.SourceType,
		RuleID: notificationString(a.RuleID), Route: a.NotificationRouteSnapshot}
}

// sameNotificationModel compares the fields that identify the rendered
// transition, including content changed by observations that do NOT bump Version.
// Heartbeat timestamps and occurrence counts are not part of the email model.
func sameNotificationModel(a, b *models.Alert) bool {
	type content struct {
		Version, Seq                                                                      int64
		Pending                                                                           bool
		Kind, Status, Hostname, Severity, Message, Output, ResolutionKind, ResolutionNote string
		Observed, Threshold                                                               *float64
		AgentMonitoringScopeSince                                                         *time.Time
		Context                                                                           models.NotificationDeliveryContext
	}
	model := func(a *models.Alert) content {
		return content{a.Version, a.NotificationSeq, a.NotificationPending, a.NotificationKind,
			a.Status, a.AgentHostname, a.Severity, a.Message, a.Output, a.ResolutionKind, a.ResolutionNote,
			a.ObservedValue, a.ThresholdValue, a.AgentMonitoringScopeSince, alertDeliveryContext(a, "")}
	}
	return reflect.DeepEqual(model(a), model(b))
}

// QueueAlertNotifications atomically captures one rendered transition, selects
// eligible recipients, inserts its complete fanout and consumes its pending bit.
// disabled consumes only this exact transition without banking stale messages.
func (s *Store) QueueAlertNotifications(ctx context.Context, expected *models.Alert, eventType, subject, body string, operator, disabled bool) error {
	if expected == nil {
		return fmt.Errorf("missing alert notification snapshot")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	current, err := scanAlert(tx.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts al WHERE al.id=$1 FOR UPDATE`, expected.ID))
	if err != nil {
		return err
	}
	if !current.NotificationPending {
		return tx.Commit(ctx)
	}
	if !sameNotificationModel(expected, current) {
		return ErrConflict
	}
	if !disabled && current.AgentSnapshotID != nil {
		if !ValidNotificationEvent(eventType) {
			return fmt.Errorf("invalid notification event")
		}
		context := alertDeliveryContext(current, eventType)
		if err := fanoutNotification(ctx, tx, context, current.AgentID, &current.ID, subject, body,
			fmt.Sprintf("alert:%s:%d", current.ID, current.NotificationSeq), operator); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE alerts SET notification_pending=false, notification_kind='', last_notified_at=now() WHERE id=$1`, current.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// QueueAgentNotifications freezes organization membership for nonincident events.
// Event subscriptions apply independently of incident rule filters.
func (s *Store) QueueAgentNotifications(ctx context.Context, agentID, eventType, subject, body, dedupeKey string, operator bool) error {
	if !ValidNotificationEvent(eventType) {
		return fmt.Errorf("invalid notification event")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var siteID, clientID, groupID *string
	if err := tx.QueryRow(ctx, `SELECT a.site_id,s.client_id,a.group_id FROM agents a LEFT JOIN sites s ON s.id=a.site_id
		WHERE a.id=$1 FOR SHARE OF a`, agentID).Scan(&siteID, &clientID, &groupID); err != nil {
		return err
	}
	context := models.NotificationDeliveryContext{Version: 1, EventType: eventType, AgentID: agentID,
		ClientID: notificationString(clientID), SiteID: notificationString(siteID), GroupID: notificationString(groupID),
		Route: models.NotificationRouteSnapshot{Mode: "automatic"}}
	// Device identity warnings retain their own recipient and subscription policy.
	if eventType != NotifyDeviceKeyIssue {
		route, _, err := effectiveNotificationRoute(ctx, tx, context.ClientID, context.SiteID)
		if err != nil {
			return err
		}
		context.Route = route
	}
	if err := fanoutNotification(ctx, tx, context, &agentID, nil, subject, body, dedupeKey, operator); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func fanoutNotification(ctx context.Context, tx pgx.Tx, delivery models.NotificationDeliveryContext, agentID, alertID *string, subject, body, dedupeKey string, operator bool) error {
	if err := validateNotificationRouteSnapshot(delivery.Route); err != nil {
		return err
	}
	recipients, err := recipientsForDelivery(ctx, tx, delivery, true, true, "", "", operator)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(delivery)
	if err != nil {
		return err
	}
	for _, r := range recipients {
		userID := r.UserID
		n := &models.Notification{EventType: delivery.EventType, AgentID: agentID, AlertID: alertID,
			UserID: &userID, Recipient: r.Email, Subject: subject, Body: body,
			DedupeKey: dedupeKey + ":" + r.UserID, DeliveryContext: raw}
		if err := enqueueNotification(ctx, tx, n); err != nil {
			return err
		}
	}
	return nil
}

// One predicate serves fanout and delivery. IDs come from the closed snapshot,
// never from current agent membership or the alert's current rule.
func recipientsForDelivery(ctx context.Context, q dbQuerier, d models.NotificationDeliveryContext, scopeKnown, ruleKnown bool, userID, address string, operator bool) ([]NotificationRecipient, error) {
	rows, err := q.Query(ctx, `SELECT u.id,u.email,u.email_verified_at IS NOT NULL
		FROM users u LEFT JOIN notification_subscriptions ns ON ns.user_id=u.id AND ns.event_type=$1
		WHERE u.disabled_at IS NULL AND u.email IS NOT NULL AND u.email<>''
		AND ($9::uuid IS NULL OR u.id=$9) AND ($10='' OR lower(u.email)=lower($10))
		AND (NOT $11 OR u.email_verified_at IS NOT NULL)
		AND COALESCE(ns.enabled,$2)
		AND EXISTS (SELECT 1 FROM role_permissions rp WHERE rp.role_key=u.role AND rp.permission='alerts.view')
		AND (NOT EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=u.id)
		 OR ($7 AND EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=u.id AND (
		  (us.scope_type='agent' AND us.scope_id=$3) OR (us.scope_type='client' AND us.scope_id=$4)
		  OR (us.scope_type='site' AND us.scope_id=$5) OR (us.scope_type='group' AND us.scope_id=$6)))))
		AND (NOT $12 OR NOT EXISTS (SELECT 1 FROM notification_rule_filters f WHERE f.user_id=u.id AND f.mode='selected')
		 OR ($8 AND (($13::uuid IS NULL AND $14<>'check' AND EXISTS (SELECT 1 FROM notification_rule_filters f WHERE f.user_id=u.id AND f.include_defaults))
		 OR EXISTS (SELECT 1 FROM notification_rule_selections sel WHERE sel.user_id=u.id AND sel.check_id=$13))))
		AND ($15='automatic' OR ($15='selected' AND u.id=ANY($16::uuid[])))
		ORDER BY u.id`, d.EventType, DefaultSubscribed(d.EventType), notificationUUID(d.AgentID), notificationUUID(d.ClientID),
		notificationUUID(d.SiteID), notificationUUID(d.GroupID), scopeKnown, ruleKnown, notificationUUID(userID), address,
		operator, d.AlertID != "", notificationUUID(d.RuleID), d.SourceType, d.Route.Mode, d.Route.UserIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recipients []NotificationRecipient
	for rows.Next() {
		var r NotificationRecipient
		if err := rows.Scan(&r.UserID, &r.Email, &r.Verified); err != nil {
			return nil, err
		}
		recipients = append(recipients, r)
	}
	return recipients, rows.Err()
}

func decodeDeliveryContext(raw json.RawMessage) (models.NotificationDeliveryContext, error) {
	var d models.NotificationDeliveryContext
	if len(raw) > 16384 {
		return d, fmt.Errorf("delivery context too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return d, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return d, fmt.Errorf("invalid trailing delivery context")
	}
	if d.Version != 1 || !ValidNotificationEvent(d.EventType) || d.AgentID == "" {
		return d, fmt.Errorf("unsupported delivery context")
	}
	if err := validateNotificationRouteSnapshot(d.Route); err != nil {
		return d, err
	}
	for _, id := range []string{d.AgentID, d.ClientID, d.SiteID, d.GroupID, d.AlertID, d.RuleID} {
		if id != "" {
			if _, err := uuid.Parse(id); err != nil {
				return d, fmt.Errorf("invalid delivery identifier")
			}
		}
	}
	if d.AlertID == "" && (d.NotificationSeq != 0 || d.SourceType != "" || d.RuleID != "") {
		return d, fmt.Errorf("invalid nonincident context")
	}
	if d.AlertID != "" && (d.NotificationSeq < 1 || d.SourceType == "") {
		return d, fmt.Errorf("invalid incident context")
	}
	return d, nil
}

// NotificationDeliveryAllowed runs immediately before each SMTP attempt. A
// database failure is retryable; a deterministic denial returns a safe reason.
func (s *Store) NotificationDeliveryAllowed(ctx context.Context, n *models.Notification, operator bool) (bool, string, error) {
	if n.UserID == nil || strings.TrimSpace(n.Recipient) == "" || !ValidNotificationEvent(n.EventType) {
		return false, "recipient or event is no longer eligible", nil
	}
	var d models.NotificationDeliveryContext
	scopeKnown, ruleKnown := true, true
	if len(n.DeliveryContext) > 0 {
		var err error
		d, err = decodeDeliveryContext(n.DeliveryContext)
		if err != nil || d.EventType != n.EventType || (n.AlertID != nil && d.AlertID != *n.AlertID) || (n.AgentID != nil && d.AgentID != *n.AgentID) {
			return false, "notification provenance is invalid", nil
		}
	} else {
		// Legacy messages have no trustworthy rule identity. Immutable alert
		// scope is still useful; with no alert, only unrestricted users qualify.
		d = models.NotificationDeliveryContext{EventType: n.EventType, Route: models.NotificationRouteSnapshot{Mode: "automatic"}}
		scopeKnown, ruleKnown = false, false
		if n.AlertID != nil {
			d.AlertID = *n.AlertID
			var agentID, clientID, siteID, groupID *string
			err := s.pool.QueryRow(ctx, `SELECT agent_snapshot_id,client_id,site_id,group_id FROM alerts WHERE id=$1`, *n.AlertID).Scan(&agentID, &clientID, &siteID, &groupID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return false, "", err
			}
			if err == nil {
				d.AgentID = notificationString(agentID)
				d.ClientID = notificationString(clientID)
				d.SiteID = notificationString(siteID)
				d.GroupID = notificationString(groupID)
				scopeKnown = true
			}
		} else if n.EventType != NotifyJobFailed && n.EventType != NotifyAIReleaseRevoked && n.EventType != NotifyDeviceKeyIssue {
			// Unknown incident origin must not bypass a selected-rules preference.
			d.AlertID = "legacy-unknown"
		}
	}
	recipients, err := recipientsForDelivery(ctx, s.pool, d, scopeKnown, ruleKnown, *n.UserID, n.Recipient, operator)
	if err != nil {
		return false, "", err
	}
	if len(recipients) == 0 {
		return false, "recipient is no longer authorized or subscribed for this message", nil
	}
	return true, "", nil
}

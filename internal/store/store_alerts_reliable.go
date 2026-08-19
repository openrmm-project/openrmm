package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

const alertCols = `al.id, al.agent_id, al.source_type, al.source_id, al.condition_key,
	al.agent_snapshot_id, al.agent_hostname, al.site_id, al.client_id, al.group_id,
	al.severity, al.message, al.status, al.created_at, al.first_seen_at,
	al.last_seen_at, al.updated_at, al.resolved_at, al.occurrence_count,
	al.observed_value, al.threshold_value, al.output, al.version,
	al.acked_at, al.acked_by, al.resolved_by, al.resolution_kind,
	al.resolution_note, al.reopened_from, al.last_notified_at,
	al.notification_pending, al.notification_kind, al.notification_seq,
	al.silenced_until, al.silenced_by, al.silence_reason,
	al.last_observation_id`

func scanAlert(row pgx.Row) (*models.Alert, error) {
	var a models.Alert
	err := row.Scan(&a.ID, &a.AgentID, &a.SourceType, &a.SourceID, &a.ConditionKey,
		&a.AgentSnapshotID, &a.AgentHostname, &a.SiteID, &a.ClientID, &a.GroupID,
		&a.Severity, &a.Message, &a.Status, &a.CreatedAt, &a.FirstSeenAt,
		&a.LastSeenAt, &a.UpdatedAt, &a.ResolvedAt, &a.OccurrenceCount,
		&a.ObservedValue, &a.ThresholdValue, &a.Output, &a.Version,
		&a.AckedAt, &a.AckedBy, &a.ResolvedBy, &a.ResolutionKind,
		&a.ResolutionNote, &a.ReopenedFrom, &a.LastNotifiedAt,
		&a.NotificationPending, &a.NotificationKind, &a.NotificationSeq,
		&a.SilencedUntil, &a.SilencedBy, &a.SilenceReason,
		&a.LastObservationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func scanAlertRows(rows pgx.Rows) ([]models.Alert, error) {
	defer rows.Close()
	var out []models.Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

type AlertObservation struct {
	AgentID       *string
	SourceType    string
	SourceID      *string
	ConditionKey  string
	Severity      string
	Message       string
	ObservedValue *float64
	Threshold     *float64
	Output        string
	Cooldown      time.Duration
	ObservedAt    time.Time
	// ObservationID identifies this observation so a delivery of it that is
	// retried can be recognised as the same one. Callers that cannot be retried
	// leave it empty, and every call then counts — without an identity there is
	// nothing to deduplicate against, and guessing from timestamp and content
	// cannot tell a retry from two distinct observations that happen to match.
	ObservationID string
}

type AlertTransition struct {
	Alert        models.Alert
	Opened       bool
	Reopened     bool
	Escalated    bool
	Unsuppressed bool
	Notify       bool
}

func severityRank(v string) int {
	switch v {
	case models.SeverityError:
		return 3
	case models.SeverityWarning:
		return 2
	case models.SeverityInfo:
		return 1
	default:
		return 0
	}
}

func alertIdentity(sourceType string, sourceID, agentID *string, conditionKey string) string {
	stringValue := func(v *string) string {
		if v == nil {
			return "-"
		}
		return *v
	}
	return strings.Join([]string{sourceType, stringValue(sourceID), stringValue(agentID), conditionKey}, "|")
}

type activeSuppression struct {
	Until  time.Time
	Reason string
	Actor  string
}

const suppressionMatchesAlertSQL = `(
	(s.scope_type='agent' AND s.scope_id=al.agent_snapshot_id) OR
	(s.scope_type='site' AND s.scope_id=al.site_id) OR
	(s.scope_type='client' AND s.scope_id=al.client_id) OR
	(s.scope_type='group' AND s.scope_id=al.group_id) OR
	(s.scope_type='check' AND al.source_type='check' AND s.scope_id=al.source_id) OR
	(s.scope_type='policy' AND al.source_type='check' AND EXISTS (
		SELECT 1 FROM checks c WHERE c.id=al.source_id AND c.policy_id=s.scope_id
	))
)`

func findActiveSuppression(ctx context.Context, tx pgx.Tx, obs AlertObservation, siteID, clientID, groupID *string, now time.Time) (*activeSuppression, error) {
	var sup activeSuppression
	err := tx.QueryRow(ctx, `
		SELECT ends_at, reason, created_by
		FROM alert_suppressions s
		WHERE s.canceled_at IS NULL AND s.starts_at <= $7 AND s.ends_at > $7
		  AND (
			(s.scope_type='agent' AND s.scope_id=$1) OR
			(s.scope_type='site' AND s.scope_id=$2) OR
			(s.scope_type='client' AND s.scope_id=$3) OR
			(s.scope_type='group' AND s.scope_id=$4) OR
			(s.scope_type='check' AND $5='check' AND s.scope_id=$6) OR
			(s.scope_type='policy' AND $5='check' AND s.scope_id=(
				SELECT policy_id FROM checks WHERE id=$6
			))
		  )
		ORDER BY ends_at DESC LIMIT 1`, obs.AgentID, siteID, clientID, groupID,
		obs.SourceType, obs.SourceID, now).Scan(&sup.Until, &sup.Reason, &sup.Actor)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sup, nil
}

func addAlertEvent(ctx context.Context, tx pgx.Tx, alertID, eventType, actor, severity, message string, observed *float64, metadata any, at time.Time) error {
	if actor == "" {
		actor = "system"
	}
	var sev *string
	if severity != "" {
		sev = &severity
	}
	raw := []byte("{}")
	if metadata != nil {
		if b, err := json.Marshal(metadata); err == nil {
			raw = b
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO alert_events
		(id, alert_id, event_type, actor, severity, message, observed_value, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, newID(), alertID, eventType,
		actor, sev, message, observed, raw, at)
	return err
}

// ObserveAlert opens or updates one condition under an advisory lock. Repeated
// observations update context and counters; a severity increase is a transition
// of its own, while a resolved predecessor becomes a linked new incident.
func (s *Store) ObserveAlert(ctx context.Context, obs AlertObservation) (*AlertTransition, error) {
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now()
	}
	if obs.ConditionKey == "" {
		obs.ConditionKey = "default"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	identity := alertIdentity(obs.SourceType, obs.SourceID, obs.AgentID, obs.ConditionKey)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, identity); err != nil {
		return nil, err
	}

	var snapshotID, siteID, clientID, groupID *string
	var hostname string
	if obs.AgentID != nil {
		snapshotID = obs.AgentID
		err = tx.QueryRow(ctx, `SELECT a.hostname, a.site_id, si.client_id, a.group_id
			FROM agents a LEFT JOIN sites si ON si.id=a.site_id WHERE a.id=$1`, *obs.AgentID).
			Scan(&hostname, &siteID, &clientID, &groupID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
	}
	suppression, err := findActiveSuppression(ctx, tx, obs, siteID, clientID, groupID, obs.ObservedAt)
	if err != nil {
		return nil, err
	}

	current, err := scanAlert(tx.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts al
		WHERE al.status<>'resolved' AND al.source_type=$1
		  AND al.source_id IS NOT DISTINCT FROM $2
		  AND al.agent_id IS NOT DISTINCT FROM $3
		  AND al.condition_key=$4
		FOR UPDATE`, obs.SourceType, obs.SourceID, obs.AgentID, obs.ConditionKey))
	if err == nil {
		// A retry is recognised by identity, never inferred. If applying a check
		// state succeeded but marking it reconciled did not, the retry arrives
		// with the same ObservationID and must finish notification delivery
		// without inventing an occurrence.
		//
		// Matching on timestamp and content instead used to collapse two
		// genuinely distinct observations whenever they agreed to the
		// microsecond, which under concurrency they do: twelve simultaneous
		// observations of one condition recorded eleven occurrences.
		sameObservation := obs.ObservationID != "" && obs.ObservationID == current.LastObservationID
		if sameObservation {
			if err := tx.Commit(ctx); err != nil {
				return nil, err
			}
			return &AlertTransition{Alert: *current,
				Notify: current.NotificationPending && current.Status != models.AlertSuppressed}, nil
		}
		escalated := severityRank(obs.Severity) > severityRank(current.Severity)
		status := current.Status
		ackedAt, ackedBy := current.AckedAt, current.AckedBy
		silencedUntil, silencedBy, silenceReason := current.SilencedUntil, current.SilencedBy, current.SilenceReason
		unsuppressed := false
		if suppression != nil {
			status = models.AlertSuppressed
			silencedUntil = &suppression.Until
			silencedBy = suppression.Actor
			silenceReason = suppression.Reason
		} else if status == models.AlertSuppressed && (silencedUntil == nil || !silencedUntil.After(obs.ObservedAt)) {
			status = models.AlertNew
			silencedUntil, silencedBy, silenceReason = nil, "", ""
			unsuppressed = true
		}
		if escalated && status == models.AlertAcked {
			// Acknowledgement applies to the severity the operator saw. Escalation
			// returns the incident to the new queue.
			status, ackedAt, ackedBy = models.AlertNew, nil, ""
		}
		pending, notificationKind, notificationSeq := current.NotificationPending,
			current.NotificationKind, current.NotificationSeq
		cooldownElapsed := false
		if notificationSeq == 0 && current.ReopenedFrom != nil && status == models.AlertNew {
			var predecessorResolvedAt time.Time
			err := tx.QueryRow(ctx, `SELECT resolved_at FROM alerts WHERE id=$1`, *current.ReopenedFrom).
				Scan(&predecessorResolvedAt)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			cooldownElapsed = err == nil && (obs.Cooldown <= 0 ||
				!obs.ObservedAt.Before(predecessorResolvedAt.Add(obs.Cooldown)))
		}
		if (escalated || unsuppressed || cooldownElapsed) && status != models.AlertSuppressed {
			pending = true
			notificationKind = models.AlertEventOpened
			notificationSeq++
		}
		notify := pending && status != models.AlertSuppressed
		updated, updateErr := scanAlert(tx.QueryRow(ctx, `UPDATE alerts al SET
			severity=CASE WHEN $2 THEN $3 ELSE severity END,
			message=$4, status=$5, last_seen_at=$6, updated_at=$6,
			occurrence_count=occurrence_count+1, observed_value=$7,
			threshold_value=$8, output=$9, version=version+1,
			acked_at=$10, acked_by=$11, silenced_until=$12,
			silenced_by=$13, silence_reason=$14,
			notification_pending=$15, notification_kind=$16, notification_seq=$17,
			last_observation_id=$18
			WHERE id=$1 RETURNING `+alertCols,
			current.ID, escalated, obs.Severity, obs.Message, status, obs.ObservedAt,
			obs.ObservedValue, obs.Threshold, obs.Output, ackedAt, ackedBy,
			silencedUntil, silencedBy, silenceReason, pending, notificationKind, notificationSeq,
			obs.ObservationID))
		if updateErr != nil {
			return nil, updateErr
		}
		if err := addAlertEvent(ctx, tx, updated.ID, models.AlertEventOccurrence,
			"system", updated.Severity, obs.Message, obs.ObservedValue,
			map[string]any{"count": updated.OccurrenceCount}, obs.ObservedAt); err != nil {
			return nil, err
		}
		if escalated {
			if err := addAlertEvent(ctx, tx, updated.ID, models.AlertEventEscalated,
				"system", obs.Severity, obs.Message, obs.ObservedValue,
				map[string]any{"from": current.Severity, "to": obs.Severity}, obs.ObservedAt); err != nil {
				return nil, err
			}
		}
		if unsuppressed {
			if err := addAlertEvent(ctx, tx, updated.ID, models.AlertEventUnsuppressed,
				"system", updated.Severity, "Suppression expired while the condition remained active.",
				obs.ObservedValue, nil, obs.ObservedAt); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &AlertTransition{Alert: *updated, Escalated: escalated,
			Unsuppressed: unsuppressed, Notify: notify}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	var priorID string
	var priorResolvedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT id, resolved_at FROM alerts
		WHERE status='resolved' AND source_type=$1
		  AND source_id IS NOT DISTINCT FROM $2
		  AND agent_snapshot_id IS NOT DISTINCT FROM $3
		  AND condition_key=$4
		ORDER BY resolved_at DESC NULLS LAST, created_at DESC LIMIT 1`,
		obs.SourceType, obs.SourceID, snapshotID, obs.ConditionKey).Scan(&priorID, &priorResolvedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var reopenedFrom *string
	if err == nil {
		reopenedFrom = &priorID
	}
	status := models.AlertNew
	var silencedUntil *time.Time
	var silencedBy, silenceReason string
	if suppression != nil {
		status = models.AlertSuppressed
		silencedUntil = &suppression.Until
		silencedBy, silenceReason = suppression.Actor, suppression.Reason
	}
	notify := status != models.AlertSuppressed
	if notify && priorResolvedAt != nil && obs.Cooldown > 0 && obs.ObservedAt.Sub(*priorResolvedAt) < obs.Cooldown {
		notify = false
	}
	pending := notify
	notificationKind := ""
	var notificationSeq int64
	if notify {
		notificationKind = models.AlertEventOpened
		notificationSeq = 1
	}
	id := newID()
	opened, err := scanAlert(tx.QueryRow(ctx, `INSERT INTO alerts AS al (
		id, agent_id, source_type, source_id, condition_key,
		agent_snapshot_id, agent_hostname, site_id, client_id, group_id,
		severity, message, status, created_at, first_seen_at, last_seen_at,
		updated_at, observed_value, threshold_value, output, reopened_from,
		notification_pending, notification_kind, notification_seq,
		silenced_until, silenced_by, silence_reason, last_observation_id
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$14,$14,$14,
	          $15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
	RETURNING `+alertCols, id, obs.AgentID, obs.SourceType, obs.SourceID,
		obs.ConditionKey, snapshotID, hostname, siteID, clientID, groupID, obs.Severity,
		obs.Message, status, obs.ObservedAt, obs.ObservedValue, obs.Threshold,
		obs.Output, reopenedFrom, pending, notificationKind, notificationSeq,
		silencedUntil, silencedBy, silenceReason, obs.ObservationID))
	if err != nil {
		return nil, err
	}
	eventType := models.AlertEventOpened
	if reopenedFrom != nil {
		eventType = models.AlertEventReopened
	}
	if err := addAlertEvent(ctx, tx, opened.ID, eventType, "system", opened.Severity,
		opened.Message, opened.ObservedValue, map[string]any{"previous_alert_id": reopenedFrom}, obs.ObservedAt); err != nil {
		return nil, err
	}
	if status == models.AlertSuppressed {
		if err := addAlertEvent(ctx, tx, opened.ID, models.AlertEventSuppressed,
			silencedBy, opened.Severity, silenceReason, opened.ObservedValue,
			map[string]any{"until": silencedUntil}, obs.ObservedAt); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &AlertTransition{Alert: *opened, Opened: true, Reopened: reopenedFrom != nil, Notify: notify}, nil
}

// ResolveAlertSource closes every active incident matching a source identity.
func (s *Store) ResolveAlertSource(ctx context.Context, sourceType string, sourceID, agentID *string, actor, kind, note string, at time.Time) ([]models.Alert, error) {
	if at.IsZero() {
		at = time.Now()
	}
	if actor == "" {
		actor = "system"
	}
	if kind == "" {
		kind = "automatic"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `UPDATE alerts al SET
		status='resolved', resolved_at=$4, updated_at=$4, last_seen_at=$4,
		resolved_by=$5, resolution_kind=$6, resolution_note=$7,
		notification_pending=CASE WHEN status<>'suppressed' AND $6='automatic' THEN true ELSE false END,
		notification_kind=CASE WHEN status<>'suppressed' AND $6='automatic' THEN 'resolved' ELSE '' END,
		notification_seq=notification_seq+CASE WHEN status<>'suppressed' AND $6='automatic' THEN 1 ELSE 0 END,
		silenced_until=NULL, silenced_by='', silence_reason='', version=version+1
		WHERE status<>'resolved' AND source_type=$1
		  AND source_id IS NOT DISTINCT FROM $2
		  AND agent_id IS NOT DISTINCT FROM $3
		RETURNING `+alertCols, sourceType, sourceID, agentID, at, actor, kind, note)
	if err != nil {
		return nil, err
	}
	resolved, err := scanAlertRows(rows)
	if err != nil {
		return nil, err
	}
	for i := range resolved {
		if err := addAlertEvent(ctx, tx, resolved[i].ID, models.AlertEventResolved,
			actor, resolved[i].Severity, note, resolved[i].ObservedValue,
			map[string]any{"kind": kind}, at); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resolved, nil
}

func (s *Store) GetAlert(ctx context.Context, id string) (*models.Alert, error) {
	return scanAlert(s.pool.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts al WHERE al.id=$1`, id))
}

// PendingAlertNotifications exposes transition notifications that were not yet
// durably copied to the per-recipient outbox. The scheduler retries these with
// the same sequence-based dedupe key after a transient database failure.
func (s *Store) PendingAlertNotifications(ctx context.Context, limit int) ([]models.Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+alertCols+` FROM alerts al
		WHERE al.notification_pending
		ORDER BY al.updated_at, al.id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return scanAlertRows(rows)
}

// MarkAlertNotificationQueued clears a pending transition only when it is
// still the sequence the caller delivered. A concurrent escalation therefore
// cannot be accidentally hidden by completion of an older enqueue attempt.
func (s *Store) MarkAlertNotificationQueued(ctx context.Context, alertID string, sequence int64, at time.Time) error {
	if at.IsZero() {
		at = time.Now()
	}
	tag, err := s.pool.Exec(ctx, `UPDATE alerts SET notification_pending=false,
		notification_kind='', last_notified_at=$3
		WHERE id=$1 AND notification_pending AND notification_seq=$2`, alertID, sequence, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// A newer transition superseding this one is not an error: its pending bit
		// remains set and the maintenance loop will enqueue it separately.
		var exists bool
		if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alerts WHERE id=$1)`, alertID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

func (s *Store) ListAlertEvents(ctx context.Context, alertID string) ([]models.AlertEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, alert_id, event_type, actor, severity,
		message, observed_value, metadata, created_at
		FROM alert_events WHERE alert_id=$1 ORDER BY created_at, id`, alertID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AlertEvent
	for rows.Next() {
		var e models.AlertEvent
		if err := rows.Scan(&e.ID, &e.AlertID, &e.EventType, &e.Actor, &e.Severity,
			&e.Message, &e.ObservedValue, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type AlertListOptions struct {
	Limit      int
	BeforeTime *time.Time
	BeforeID   string
	Status     string
	Severity   string
	SourceType string
	AgentID    string
	SiteID     string
	ClientID   string
	GroupID    string
	Query      string
	From       *time.Time
	To         *time.Time
	UserID     string
	Scoped     bool
}

func addSQLArg(args *[]any, value any) string {
	*args = append(*args, value)
	return fmt.Sprintf("$%d", len(*args))
}

func alertScopeSQL(args *[]any, userID string, scoped bool) string {
	if !scoped {
		return ""
	}
	p := addSQLArg(args, userID)
	return ` AND EXISTS (
		SELECT 1 FROM user_scopes us WHERE us.user_id=` + p + ` AND (
			(us.scope_type='agent' AND us.scope_id=al.agent_snapshot_id) OR
			(us.scope_type='site' AND us.scope_id=al.site_id) OR
			(us.scope_type='client' AND us.scope_id=al.client_id) OR
			(us.scope_type='group' AND us.scope_id=al.group_id)
		)
	)`
}

func buildAlertFilter(opts AlertListOptions, args *[]any) string {
	var b strings.Builder
	b.WriteString(alertScopeSQL(args, opts.UserID, opts.Scoped))
	if opts.Status != "" {
		if opts.Status == "open" {
			b.WriteString(` AND al.status IN ('new','acked')`)
		} else {
			b.WriteString(" AND al.status=" + addSQLArg(args, opts.Status))
		}
	}
	if opts.Severity != "" {
		b.WriteString(" AND al.severity=" + addSQLArg(args, opts.Severity))
	}
	if opts.SourceType != "" {
		b.WriteString(" AND al.source_type=" + addSQLArg(args, opts.SourceType))
	}
	if opts.AgentID != "" {
		b.WriteString(" AND al.agent_snapshot_id=" + addSQLArg(args, opts.AgentID))
	}
	if opts.SiteID != "" {
		b.WriteString(" AND al.site_id=" + addSQLArg(args, opts.SiteID))
	}
	if opts.ClientID != "" {
		b.WriteString(" AND al.client_id=" + addSQLArg(args, opts.ClientID))
	}
	if opts.GroupID != "" {
		b.WriteString(" AND al.group_id=" + addSQLArg(args, opts.GroupID))
	}
	if strings.TrimSpace(opts.Query) != "" {
		p := addSQLArg(args, "%"+strings.TrimSpace(opts.Query)+"%")
		b.WriteString(" AND (al.message ILIKE " + p + " OR al.agent_hostname ILIKE " + p + " OR al.output ILIKE " + p + ")")
	}
	if opts.From != nil {
		b.WriteString(" AND al.created_at >= " + addSQLArg(args, *opts.From))
	}
	if opts.To != nil {
		b.WriteString(" AND al.created_at <= " + addSQLArg(args, *opts.To))
	}
	return b.String()
}

func (s *Store) ListAlertsPage(ctx context.Context, opts AlertListOptions) ([]models.Alert, error) {
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	args := make([]any, 0, 16)
	query := `SELECT ` + alertCols + ` FROM alerts al WHERE true`
	query += buildAlertFilter(opts, &args)
	if opts.BeforeTime != nil && opts.BeforeID != "" {
		pt, pi := addSQLArg(&args, *opts.BeforeTime), addSQLArg(&args, opts.BeforeID)
		query += " AND (al.created_at, al.id) < (" + pt + "," + pi + ")"
	}
	query += " ORDER BY al.created_at DESC, al.id DESC LIMIT " + addSQLArg(&args, opts.Limit+1)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanAlertRows(rows)
}

func (s *Store) AlertCounts(ctx context.Context, userID string, scoped bool) (models.AlertCounts, error) {
	args := make([]any, 0, 1)
	scope := alertScopeSQL(&args, userID, scoped)
	var c models.AlertCounts
	err := s.pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE al.status='new'),
		count(*) FILTER (WHERE al.status='acked'),
		count(*) FILTER (WHERE al.status='suppressed'),
		count(*) FILTER (WHERE al.status='resolved'),
		count(*) FILTER (WHERE al.status IN ('new','acked')),
		count(*)
		FROM alerts al WHERE true`+scope, args...).Scan(
		&c.New, &c.Acked, &c.Suppressed, &c.Resolved, &c.Open, &c.Total)
	return c, err
}

func (s *Store) AlertAllowedForUser(ctx context.Context, userID, alertID string) (bool, error) {
	var allowed bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM alerts al WHERE al.id=$2 AND (
			NOT EXISTS (SELECT 1 FROM user_scopes WHERE user_id=$1) OR
			EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
				(us.scope_type='agent' AND us.scope_id=al.agent_snapshot_id) OR
				(us.scope_type='site' AND us.scope_id=al.site_id) OR
				(us.scope_type='client' AND us.scope_id=al.client_id) OR
				(us.scope_type='group' AND us.scope_id=al.group_id)
			))
		)
	)`, userID, alertID).Scan(&allowed)
	return allowed, err
}

type AlertAction struct {
	Action          string
	Actor           string
	Note            string
	ExpectedVersion *int64
	SilenceUntil    *time.Time
}

// TransitionAlert validates the lifecycle and applies an optimistic version
// check. It is shared by single and bulk API actions.
func (s *Store) TransitionAlert(ctx context.Context, id string, action AlertAction) (*models.Alert, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanAlert(tx.QueryRow(ctx, `SELECT `+alertCols+` FROM alerts al WHERE al.id=$1 FOR UPDATE`, id))
	if err != nil {
		return nil, err
	}
	if action.ExpectedVersion != nil && current.Version != *action.ExpectedVersion {
		return nil, fmt.Errorf("%w: alert version changed", ErrConflict)
	}
	if action.Actor == "" {
		action.Actor = "system"
	}
	now := time.Now()
	var updated *models.Alert
	var eventType string
	switch action.Action {
	case "ack":
		if current.Status != models.AlertNew {
			return nil, fmt.Errorf("%w: only a new alert can be acknowledged", ErrConflict)
		}
		updated, err = scanAlert(tx.QueryRow(ctx, `UPDATE alerts al SET
			status='acked', acked_at=$2, acked_by=$3, updated_at=$2, version=version+1
			WHERE id=$1 RETURNING `+alertCols, id, now, action.Actor))
		eventType = models.AlertEventAcknowledged
	case "resolve":
		if current.Status == models.AlertResolved {
			return current, nil
		}
		updated, err = scanAlert(tx.QueryRow(ctx, `UPDATE alerts al SET
			status='resolved', resolved_at=$2, resolved_by=$3,
			resolution_kind='manual', resolution_note=$4, updated_at=$2,
			notification_pending=false, notification_kind='',
			silenced_until=NULL, silenced_by='', silence_reason='', version=version+1
			WHERE id=$1 RETURNING `+alertCols, id, now, action.Actor, action.Note))
		eventType = models.AlertEventResolved
	case "suppress":
		if current.Status == models.AlertResolved || action.SilenceUntil == nil || !action.SilenceUntil.After(now) {
			return nil, fmt.Errorf("%w: active alert and future silence_until required", ErrConflict)
		}
		updated, err = scanAlert(tx.QueryRow(ctx, `UPDATE alerts al SET
			status='suppressed', silenced_until=$2, silenced_by=$3,
			silence_reason=$4, notification_pending=false, notification_kind='',
			updated_at=$5, version=version+1
			WHERE id=$1 RETURNING `+alertCols,
			id, *action.SilenceUntil, action.Actor, action.Note, now))
		eventType = models.AlertEventSuppressed
	default:
		return nil, fmt.Errorf("%w: unsupported alert action", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	if eventType != "" {
		if err := addAlertEvent(ctx, tx, id, eventType, action.Actor, updated.Severity,
			action.Note, updated.ObservedValue, map[string]any{"version": updated.Version}, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) CreateAlertSuppression(ctx context.Context, sup *models.AlertSuppression) error {
	if sup.ID == "" {
		sup.ID = newID()
	}
	if sup.StartsAt.IsZero() {
		sup.StartsAt = time.Now()
	}
	err := s.pool.QueryRow(ctx, `INSERT INTO alert_suppressions
		(id, kind, scope_type, scope_id, starts_at, ends_at, reason, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING created_at`, sup.ID, sup.Kind, sup.ScopeType, sup.ScopeID,
		sup.StartsAt, sup.EndsAt, sup.Reason, sup.CreatedBy).Scan(&sup.CreatedAt)
	if err != nil {
		return err
	}
	if !sup.StartsAt.After(time.Now()) {
		return s.applySuppressionToAlerts(ctx, sup)
	}
	return nil
}

func suppressionAlertPredicate(scopeType string) string {
	switch scopeType {
	case models.ScopeAgent:
		return "al.agent_snapshot_id=$1"
	case models.ScopeSite:
		return "al.site_id=$1"
	case models.ScopeClient:
		return "al.client_id=$1"
	case models.ScopeGroup:
		return "al.group_id=$1"
	case "check":
		return "al.source_type='check' AND al.source_id=$1"
	case "policy":
		return "al.source_type='check' AND al.source_id IN (SELECT id FROM checks WHERE policy_id=$1)"
	default:
		return "false"
	}
}

func (s *Store) applySuppressionToAlerts(ctx context.Context, sup *models.AlertSuppression) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `UPDATE alerts al SET status='suppressed',
		silenced_until=$2, silenced_by=$3, silence_reason=$4,
		notification_pending=false, notification_kind='', updated_at=now(), version=version+1
		WHERE al.status IN ('new','acked') AND `+suppressionAlertPredicate(sup.ScopeType)+`
		RETURNING `+alertCols, sup.ScopeID, sup.EndsAt, sup.CreatedBy, sup.Reason)
	if err != nil {
		return err
	}
	alerts, err := scanAlertRows(rows)
	if err != nil {
		return err
	}
	for i := range alerts {
		if err := addAlertEvent(ctx, tx, alerts[i].ID, models.AlertEventSuppressed,
			sup.CreatedBy, alerts[i].Severity, sup.Reason, alerts[i].ObservedValue,
			map[string]any{"suppression_id": sup.ID, "until": sup.EndsAt}, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) ListAlertSuppressions(ctx context.Context, userID string, scoped bool, activeOnly bool) ([]models.AlertSuppression, error) {
	args := []any{userID, scoped, activeOnly}
	rows, err := s.pool.Query(ctx, `SELECT s.id, s.kind, s.scope_type, s.scope_id,
		s.starts_at, s.ends_at, s.reason, s.created_by, s.created_at,
		s.canceled_at, s.canceled_by
		FROM alert_suppressions s
		WHERE (NOT $3 OR (s.canceled_at IS NULL AND s.ends_at > now()))
		AND (NOT $2 OR EXISTS (
			SELECT 1 FROM agents a
			LEFT JOIN sites si ON si.id=a.site_id
			WHERE (
				(s.scope_type='agent' AND s.scope_id=a.id) OR
				(s.scope_type='site' AND s.scope_id=a.site_id) OR
				(s.scope_type='client' AND s.scope_id=si.client_id) OR
				(s.scope_type='group' AND s.scope_id=a.group_id) OR
				(s.scope_type='check' AND EXISTS (
					SELECT 1 FROM checks c WHERE c.id=s.scope_id AND (
						c.agent_id=a.id OR (c.policy_id IS NOT NULL AND EXISTS (
							SELECT 1 FROM policy_assignments pa WHERE pa.policy_id=c.policy_id AND (
								(pa.scope_type='agent' AND pa.scope_id=a.id) OR
								(pa.scope_type='group' AND pa.scope_id=a.group_id) OR
								(pa.scope_type='site' AND pa.scope_id=a.site_id) OR
								(pa.scope_type='client' AND pa.scope_id=si.client_id)
							)
						))
					)
				)) OR
				(s.scope_type='policy' AND EXISTS (
					SELECT 1 FROM policy_assignments pa WHERE pa.policy_id=s.scope_id AND (
						(pa.scope_type='agent' AND pa.scope_id=a.id) OR
						(pa.scope_type='group' AND pa.scope_id=a.group_id) OR
						(pa.scope_type='site' AND pa.scope_id=a.site_id) OR
						(pa.scope_type='client' AND pa.scope_id=si.client_id)
					)
				))
			) AND EXISTS (SELECT 1 FROM user_scopes us WHERE us.user_id=$1 AND (
				(us.scope_type='agent' AND us.scope_id=a.id) OR
				(us.scope_type='group' AND us.scope_id=a.group_id) OR
				(us.scope_type='site' AND us.scope_id=a.site_id) OR
				(us.scope_type='client' AND us.scope_id=si.client_id)
			))
		))
		ORDER BY s.created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.AlertSuppression
	for rows.Next() {
		var sup models.AlertSuppression
		if err := rows.Scan(&sup.ID, &sup.Kind, &sup.ScopeType, &sup.ScopeID,
			&sup.StartsAt, &sup.EndsAt, &sup.Reason, &sup.CreatedBy, &sup.CreatedAt,
			&sup.CanceledAt, &sup.CanceledBy); err != nil {
			return nil, err
		}
		out = append(out, sup)
	}
	return out, rows.Err()
}

func (s *Store) CancelAlertSuppression(ctx context.Context, id, actor string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sup models.AlertSuppression
	err = tx.QueryRow(ctx, `UPDATE alert_suppressions SET canceled_at=now(), canceled_by=$2
		WHERE id=$1 AND canceled_at IS NULL
		RETURNING id, kind, scope_type, scope_id, starts_at, ends_at, reason,
		created_by, created_at, canceled_at, canceled_by`, id, actor).Scan(
		&sup.ID, &sup.Kind, &sup.ScopeType, &sup.ScopeID, &sup.StartsAt,
		&sup.EndsAt, &sup.Reason, &sup.CreatedBy, &sup.CreatedAt,
		&sup.CanceledAt, &sup.CanceledBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Make alerts held specifically by this rule due for reconciliation. A
	// still-active overlapping rule is reapplied before the due rows reactivate.
	if _, err := tx.Exec(ctx, `UPDATE alerts al SET silenced_until=now()
		WHERE al.status='suppressed' AND al.silenced_until=$2
		  AND al.silenced_by=$3 AND al.silence_reason=$4
		  AND `+suppressionAlertPredicate(sup.ScopeType),
		sup.ScopeID, sup.EndsAt, sup.CreatedBy, sup.Reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ApplyActiveAlertSuppressions activates scheduled maintenance/silence windows
// and extends an alert to the longest overlapping active rule.
func (s *Store) ApplyActiveAlertSuppressions(ctx context.Context, now time.Time, limit int) ([]models.Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `WITH matches AS (
		SELECT DISTINCT ON (al.id) al.id, s.id suppression_id, s.ends_at,
			s.reason, s.created_by
		FROM alerts al JOIN alert_suppressions s ON `+suppressionMatchesAlertSQL+`
		WHERE al.status<>'resolved' AND s.canceled_at IS NULL
		  AND s.starts_at <= $1 AND s.ends_at > $1
		  AND (al.status<>'suppressed' OR al.silenced_until IS DISTINCT FROM s.ends_at
		       OR al.silenced_by IS DISTINCT FROM s.created_by
		       OR al.silence_reason IS DISTINCT FROM s.reason)
		ORDER BY al.id, s.ends_at DESC, s.id
		LIMIT $2
	)
	UPDATE alerts al SET status='suppressed', silenced_until=m.ends_at,
		silenced_by=m.created_by, silence_reason=m.reason,
		notification_pending=false, notification_kind='', updated_at=$1,
		version=version+1
	FROM matches m WHERE al.id=m.id
	RETURNING `+alertCols, now, limit)
	if err != nil {
		return nil, err
	}
	alerts, err := scanAlertRows(rows)
	if err != nil {
		return nil, err
	}
	for i := range alerts {
		if err := addAlertEvent(ctx, tx, alerts[i].ID, models.AlertEventSuppressed,
			alerts[i].SilencedBy, alerts[i].Severity, alerts[i].SilenceReason,
			alerts[i].ObservedValue, map[string]any{"until": alerts[i].SilencedUntil}, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return alerts, nil
}

// ReactivateExpiredSuppressedAlerts returns still-active conditions to the new
// queue after their direct or scoped silence expires.
func (s *Store) ReactivateExpiredSuppressedAlerts(ctx context.Context, now time.Time, limit int) ([]models.Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `WITH due AS (
		SELECT al.id FROM alerts al
		WHERE al.status='suppressed' AND al.silenced_until <= $1
		  AND NOT EXISTS (SELECT 1 FROM alert_suppressions s
			WHERE s.canceled_at IS NULL AND s.starts_at <= $1 AND s.ends_at > $1
			  AND `+suppressionMatchesAlertSQL+`)
		ORDER BY al.silenced_until LIMIT $2 FOR UPDATE SKIP LOCKED
	)
	UPDATE alerts al SET status='new', silenced_until=NULL, silenced_by='',
		silence_reason='', updated_at=$1, notification_pending=true,
		notification_kind='opened', notification_seq=notification_seq+1,
		version=version+1
	FROM due WHERE al.id=due.id
	RETURNING `+alertCols, now, limit)
	if err != nil {
		return nil, err
	}
	alerts, err := scanAlertRows(rows)
	if err != nil {
		return nil, err
	}
	for i := range alerts {
		if err := addAlertEvent(ctx, tx, alerts[i].ID, models.AlertEventUnsuppressed,
			"system", alerts[i].Severity, "Suppression expired while the condition remained active.",
			alerts[i].ObservedValue, nil, now); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return alerts, nil
}

// ResolveInapplicableCheckAlerts closes incidents whose check was disabled,
// removed, unassigned, or whose device was revoked.
func (s *Store) ResolveInapplicableCheckAlerts(ctx context.Context, now time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `UPDATE alerts al SET status='resolved', resolved_at=$1,
		updated_at=$1, resolved_by='system', resolution_kind='orphaned',
		resolution_note='The check is disabled, removed, unassigned, or its device is revoked.',
		notification_pending=false, notification_kind='', version=version+1
		WHERE al.status<>'resolved' AND al.source_type='check' AND NOT EXISTS (
			SELECT 1 FROM checks c
			JOIN agents a ON a.id=al.agent_id AND a.revoked_at IS NULL
			LEFT JOIN sites si ON si.id=a.site_id
			WHERE c.id=al.source_id AND c.enabled AND (
				c.agent_id=a.id OR c.policy_id IN (
					SELECT pa.policy_id FROM policy_assignments pa
					WHERE (pa.scope_type='agent' AND pa.scope_id=a.id)
					   OR (pa.scope_type='group' AND pa.scope_id=a.group_id)
					   OR (pa.scope_type='site' AND pa.scope_id=a.site_id)
					   OR (pa.scope_type='client' AND pa.scope_id=si.client_id)
				)
			)
		)
		RETURNING `+alertCols, now)
	if err != nil {
		return 0, err
	}
	alerts, err := scanAlertRows(rows)
	if err != nil {
		return 0, err
	}
	for i := range alerts {
		if err := addAlertEvent(ctx, tx, alerts[i].ID, models.AlertEventResolved,
			"system", alerts[i].Severity, alerts[i].ResolutionNote,
			alerts[i].ObservedValue, map[string]any{"kind": "orphaned"}, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(alerts)), nil
}

func (s *Store) ResolveCheckAlertsForOfflineAgent(ctx context.Context, agentID string, now time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `UPDATE alerts al SET status='resolved', resolved_at=$2,
		updated_at=$2, resolved_by='system', resolution_kind='dependency',
		resolution_note='Suppressed because the device is offline.',
		notification_pending=false, notification_kind='', version=version+1
		WHERE status<>'resolved' AND source_type='check' AND agent_id=$1
		RETURNING `+alertCols, agentID, now)
	if err != nil {
		return 0, err
	}
	alerts, err := scanAlertRows(rows)
	if err != nil {
		return 0, err
	}
	for i := range alerts {
		if err := addAlertEvent(ctx, tx, alerts[i].ID, models.AlertEventResolved,
			"system", alerts[i].Severity, alerts[i].ResolutionNote,
			alerts[i].ObservedValue, map[string]any{"kind": "dependency"}, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(alerts)), nil
}

// ResolveAlertsForRetiredAgent closes connectivity and check incidents before
// revoke/delete while the live foreign key still identifies the device. Alert
// snapshots remain readable and scoped after deletion.
func (s *Store) ResolveAlertsForRetiredAgent(ctx context.Context, agentID, actor, kind string, now time.Time) (int64, error) {
	if now.IsZero() {
		now = time.Now()
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	note := "Device was " + kind + "; the condition is no longer monitored."
	rows, err := tx.Query(ctx, `UPDATE alerts al SET status='resolved', resolved_at=$2,
		updated_at=$2, resolved_by=$3, resolution_kind=$4, resolution_note=$5,
		notification_pending=false, notification_kind='', silenced_until=NULL,
		silenced_by='', silence_reason='', version=version+1
		WHERE al.status<>'resolved' AND al.agent_snapshot_id=$1
		RETURNING `+alertCols, agentID, now, actor, kind, note)
	if err != nil {
		return 0, err
	}
	alerts, err := scanAlertRows(rows)
	if err != nil {
		return 0, err
	}
	for i := range alerts {
		if err := addAlertEvent(ctx, tx, alerts[i].ID, models.AlertEventResolved,
			actor, alerts[i].Severity, note, alerts[i].ObservedValue,
			map[string]any{"kind": kind}, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(alerts)), nil
}

func (s *Store) ResolveRetiredAgentAlerts(ctx context.Context, now time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	note := "The device was revoked or deleted; the incident is no longer actionable."
	rows, err := tx.Query(ctx, `UPDATE alerts al SET status='resolved', resolved_at=$1,
		updated_at=$1, resolved_by='system', resolution_kind='retired', resolution_note=$2,
		notification_pending=false, notification_kind='', silenced_until=NULL,
		silenced_by='', silence_reason='', version=version+1
		WHERE al.status<>'resolved' AND al.agent_snapshot_id IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM agents a
			WHERE a.id=al.agent_snapshot_id AND a.revoked_at IS NULL
		  )
		RETURNING `+alertCols, now, note)
	if err != nil {
		return 0, err
	}
	alerts, err := scanAlertRows(rows)
	if err != nil {
		return 0, err
	}
	for i := range alerts {
		if err := addAlertEvent(ctx, tx, alerts[i].ID, models.AlertEventResolved,
			"system", alerts[i].Severity, note, alerts[i].ObservedValue,
			map[string]any{"kind": "retired"}, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int64(len(alerts)), nil
}

func (s *Store) AgentHasOpenOfflineAlert(ctx context.Context, agentID string) (bool, error) {
	var open bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alerts
		WHERE source_type='agent' AND agent_id=$1 AND status<>'resolved')`, agentID).Scan(&open)
	return open, err
}

type RetentionResult struct {
	Alerts        int64
	Results       int64
	Executions    int64
	Notifications int64
	Suppressions  int64
}

func (s *Store) PurgeMonitoringData(ctx context.Context, now time.Time, alertDays, resultDays, notificationDays int) (RetentionResult, error) {
	if alertDays <= 0 {
		alertDays = 90
	}
	if resultDays <= 0 {
		resultDays = 90
	}
	if notificationDays <= 0 {
		notificationDays = 90
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RetentionResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var out RetentionResult
	queries := []struct {
		dest *int64
		sql  string
		cut  time.Time
	}{
		{&out.Alerts, `DELETE FROM alerts WHERE status='resolved' AND NOT notification_pending AND resolved_at < $1`, now.AddDate(0, 0, -alertDays)},
		{&out.Results, `DELETE FROM check_results WHERE ran_at < $1`, now.AddDate(0, 0, -resultDays)},
		{&out.Executions, `DELETE FROM check_executions WHERE status<>'pending' AND completed_at < $1`, now.AddDate(0, 0, -resultDays)},
		{&out.Notifications, `DELETE FROM notifications WHERE status IN ('sent','failed') AND created_at < $1`, now.AddDate(0, 0, -notificationDays)},
		{&out.Suppressions, `DELETE FROM alert_suppressions WHERE ends_at < $1`, now.AddDate(0, 0, -alertDays)},
	}
	for _, q := range queries {
		tag, err := tx.Exec(ctx, q.sql, q.cut)
		if err != nil {
			return out, err
		}
		*q.dest = tag.RowsAffected()
	}
	if err := tx.Commit(ctx); err != nil {
		return RetentionResult{}, err
	}
	return out, nil
}

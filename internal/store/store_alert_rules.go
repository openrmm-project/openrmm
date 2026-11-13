package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/p1ngu/rmm/internal/models"
)

// ===================== Alert rules (docs/24-alert-rules.md) =====================

// AlertRuleDefaults returns the fleet default of every server rule type.
func (s *Store) AlertRuleDefaults(ctx context.Context) (map[string]models.AlertRuleDefault, error) {
	rows, err := s.pool.Query(ctx, `SELECT type, config, updated_by FROM alert_rule_defaults`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]models.AlertRuleDefault{}
	for rows.Next() {
		var d models.AlertRuleDefault
		var raw json.RawMessage
		if err := rows.Scan(&d.Type, &raw, &d.UpdatedBy); err != nil {
			return nil, err
		}
		cfg, err := models.ParseServerRuleConfig(d.Type, raw)
		if err != nil {
			return nil, fmt.Errorf("fleet default %s: %w", d.Type, err)
		}
		d.Config = cfg
		out[d.Type] = d
	}
	return out, rows.Err()
}

// SetAlertRuleDefault replaces one type's fleet default. The caller validates
// the config (models.ParseServerRuleConfig).
func (s *Store) SetAlertRuleDefault(ctx context.Context, ruleType string, cfg models.ServerRuleConfig, actor string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE alert_rule_defaults SET config=$2, updated_by=$3, updated_at=now() WHERE type=$1`,
		ruleType, cfg.Raw(), actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// governingCandidates joins each device to every server rule of type $1 that
// can govern it: one it owns, and one of each policy assigned to its client,
// site, department or itself. Only enabled rules count, and an AI proposal only
// once a person accepted it, so an unreviewed proposal cannot silence anything.
const governingCandidates = `
	FROM agents a
	LEFT JOIN sites st ON st.id = a.site_id
	JOIN LATERAL (
	  SELECT c.id AS check_id, NULL::uuid AS policy_id, 'device' AS scope, 5 AS rank
	  FROM checks c WHERE c.agent_id = a.id AND c.type = $1
	  UNION ALL
	  SELECT c.id, pa.policy_id, pa.scope_type,
	         CASE pa.scope_type WHEN 'client' THEN 1 WHEN 'site' THEN 2 WHEN 'group' THEN 3 ELSE 4 END
	  FROM policy_assignments pa
	  JOIN checks c ON c.policy_id = pa.policy_id AND c.type = $1
	  WHERE pa.policy_id IN ` + activePoliciesSQL + ` AND (
	        (pa.scope_type = 'client' AND pa.scope_id = st.client_id)
	     OR (pa.scope_type = 'site'   AND pa.scope_id = a.site_id)
	     OR (pa.scope_type = 'group'  AND pa.scope_id = a.group_id)
	     OR (pa.scope_type = 'agent'  AND pa.scope_id = a.id))
	) src ON true
	JOIN checks c ON c.id = src.check_id
	LEFT JOIN policies p ON p.id = src.policy_id
	WHERE a.status <> 'revoked' AND c.enabled AND (c.proposed_by = '' OR c.approved_by <> '')`

// governingOrder is the precedence, applied with DISTINCT ON (a.id): the
// broadest scope decides (a policy at the client, then the site, the
// department, the device, then a rule the device owns), so a client's rule
// holds for every device under it. At the same level the more sensitive rule
// wins (alert over ignore, a shorter delay, a smaller version gap, every job
// over tasks only, a higher severity), then the lower id, so the result never
// depends on row order.
const governingOrder = `
	ORDER BY a.id, src.rank,
	  (c.config->>'mode' = 'ignore'),
	  COALESCE(NULLIF(c.config->>'after_min', '')::int, 0),
	  COALESCE(NULLIF(c.config->>'after_days', '')::int, 0),
	  CASE c.config->>'behind' WHEN 'patch' THEN 1 WHEN 'minor' THEN 2 ELSE 3 END,
	  (c.config->>'scope' = 'tasks'),
	  CASE c.config->>'severity' WHEN 'error' THEN 1 WHEN 'warning' THEN 2 ELSE 3 END,
	  c.id`

// GoverningRules returns, per device id, the server rule of ruleType in force
// for it. Devices no rule governs are absent: the fleet default applies to them.
func (s *Store) GoverningRules(ctx context.Context, ruleType string) (map[string]models.GoverningRule, error) {
	if !models.IsServerRule(ruleType) {
		return nil, fmt.Errorf("%q is not a server rule type", ruleType)
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (a.id) a.id, c.id, c.name, c.config,
		COALESCE(p.id::text, ''), COALESCE(p.name, ''), src.scope, a.monitoring_scope_since`+governingCandidates+governingOrder, ruleType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]models.GoverningRule{}
	for rows.Next() {
		var agentID string
		var raw json.RawMessage
		r := models.GoverningRule{Type: ruleType}
		if err := rows.Scan(&agentID, &r.CheckID, &r.Name, &raw, &r.PolicyID, &r.PolicyName, &r.ScopeType, &r.MonitoringScopeSince); err != nil {
			return nil, err
		}
		cfg, err := models.ParseServerRuleConfig(ruleType, raw)
		if err != nil {
			// A rule the console could not have saved: skip it, so one bad row
			// cannot take a whole fleet off its default.
			continue
		}
		r.Config = cfg
		out[agentID] = r
	}
	return out, rows.Err()
}

// CheckCoveredAgents lists the devices a check applies to: the one that owns
// it, or every device under the scopes its policy is assigned to.
func (s *Store) CheckCoveredAgents(ctx context.Context, checkID string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id FROM checks c JOIN agents a ON a.id = c.agent_id
		WHERE c.id = $1 AND a.status <> 'revoked'
		UNION
		SELECT a.id FROM checks c
		JOIN policy_assignments pa ON pa.policy_id = c.policy_id AND pa.policy_id IN `+activePoliciesSQL+`
		JOIN agents a ON (
		     (pa.scope_type = 'agent'  AND pa.scope_id = a.id)
		  OR (pa.scope_type = 'group'  AND pa.scope_id = a.group_id)
		  OR (pa.scope_type = 'site'   AND pa.scope_id = a.site_id)
		  OR (pa.scope_type = 'client' AND pa.scope_id = (SELECT st.client_id FROM sites st WHERE st.id = a.site_id)))
		WHERE c.id = $1 AND a.status <> 'revoked'`, checkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// FailedPatchCounts returns, per device, how many updates failed to install
// and are still failed.
func (s *Store) FailedPatchCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT agent_id, count(*) FROM agent_patches WHERE status = 'failed' GROUP BY agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// RebootPendingSince returns, per device with a reboot pending, since when.
func (s *Store) RebootPendingSince(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, COALESCE(reboot_pending_since, now()) FROM agents WHERE reboot_pending AND status <> 'revoked'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

// JobOutcome is the latest finished run of one scheduled task or script on one
// device.
type JobOutcome struct {
	MonitoringScopeSince time.Time
	AgentID              string
	Key                  string // task:<id> | script:<id> | adhoc
	FromTask             bool
	Failed               bool
	JobID                string
	Description          string
	At                   time.Time
}

// LatestJobOutcomes returns, per device and per scheduled task or script, the
// latest job that finished since `since`. Only the latest counts: a task that
// failed and then succeeded is healthy again.
func (s *Store) LatestJobOutcomes(ctx context.Context, since time.Time) ([]JobOutcome, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (j.agent_id, k.key) j.agent_id, k.key, j.task_id IS NOT NULL,
		       j.status = 'failed', j.id, j.description, j.updated_at, a.monitoring_scope_since
		FROM jobs j JOIN agents a ON a.id=j.agent_id
		CROSS JOIN LATERAL (SELECT CASE
		    WHEN j.task_id IS NOT NULL THEN 'task:' || j.task_id::text
		    WHEN j.script_id IS NOT NULL THEN 'script:' || j.script_id::text
		    ELSE 'adhoc' END AS key) k
		WHERE j.status IN ('completed', 'failed') AND j.updated_at >= $1
          AND j.agent_monitoring_scope_since = a.monitoring_scope_since
		ORDER BY j.agent_id, k.key, j.updated_at DESC, j.id DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JobOutcome
	for rows.Next() {
		var o JobOutcome
		if err := rows.Scan(&o.AgentID, &o.Key, &o.FromTask, &o.Failed, &o.JobID, &o.Description, &o.At, &o.MonitoringScopeSince); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OpenRuleAlerts returns the open alerts server rules raised (source "rule"),
// so the evaluation can close those whose condition is gone.
func (s *Store) OpenRuleAlerts(ctx context.Context) ([]models.Alert, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertCols+` FROM alerts al WHERE al.status <> 'resolved' AND al.source_type = $1`, models.AlertSourceRule)
	if err != nil {
		return nil, err
	}
	return scanAlertRows(rows)
}

// ===================== Which rules reach a user's inbox =====================

// NotificationRuleFilter is a user's choice of which rules' alerts are mailed.
type NotificationRuleFilter struct {
	Mode            string   `json:"mode"` // all | selected
	IncludeDefaults bool     `json:"include_defaults"`
	CheckIDs        []string `json:"check_ids"`
}

// GetNotificationRuleFilter returns a user's choice; with none, all rules.
func (s *Store) GetNotificationRuleFilter(ctx context.Context, userID string) (NotificationRuleFilter, error) {
	f := NotificationRuleFilter{Mode: "all", IncludeDefaults: true, CheckIDs: []string{}}
	err := s.pool.QueryRow(ctx, `SELECT mode, include_defaults FROM notification_rule_filters WHERE user_id=$1`, userID).
		Scan(&f.Mode, &f.IncludeDefaults)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return f, err
	}
	rows, err := s.pool.Query(ctx, `SELECT check_id FROM notification_rule_selections WHERE user_id=$1 ORDER BY check_id`, userID)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return f, err
		}
		f.CheckIDs = append(f.CheckIDs, id)
	}
	return f, rows.Err()
}

// SetNotificationRuleFilter replaces a user's choice. checkIDs must exist; the
// caller has already limited them to checks the user may see.
func (s *Store) SetNotificationRuleFilter(ctx context.Context, userID string, f NotificationRuleFilter) error {
	if f.Mode != "all" && f.Mode != "selected" {
		return fmt.Errorf("rules.mode must be all or selected")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO notification_rule_filters (user_id, mode, include_defaults, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE SET mode=EXCLUDED.mode, include_defaults=EXCLUDED.include_defaults, updated_at=now()`,
		userID, f.Mode, f.IncludeDefaults); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM notification_rule_selections WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, id := range f.CheckIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO notification_rule_selections (user_id, check_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

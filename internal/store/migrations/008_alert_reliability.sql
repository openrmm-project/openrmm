-- Reliable monitoring and alert lifecycle.
--
-- This migration deliberately keeps the original check/alert rows intact while
-- adding the state needed for durable dispatch, escalation, acknowledgement,
-- suppression, history, optimistic concurrency, and scoped pagination.

-- ===================== Check policy / state =====================

ALTER TABLE checks ADD COLUMN IF NOT EXISTS failure_count INTEGER NOT NULL DEFAULT 2;
ALTER TABLE checks ADD COLUMN IF NOT EXISTS recovery_count INTEGER NOT NULL DEFAULT 2;
ALTER TABLE checks ADD COLUMN IF NOT EXISTS hysteresis DOUBLE PRECISION NOT NULL DEFAULT 2;
ALTER TABLE checks ADD COLUMN IF NOT EXISTS cooldown_sec INTEGER NOT NULL DEFAULT 300;
ALTER TABLE checks ADD COLUMN IF NOT EXISTS timeout_sec INTEGER NOT NULL DEFAULT 180;

ALTER TABLE checks ADD CONSTRAINT checks_failure_count_valid CHECK (failure_count BETWEEN 1 AND 100);
ALTER TABLE checks ADD CONSTRAINT checks_recovery_count_valid CHECK (recovery_count BETWEEN 1 AND 100);
ALTER TABLE checks ADD CONSTRAINT checks_hysteresis_valid CHECK (hysteresis BETWEEN 0 AND 100);
ALTER TABLE checks ADD CONSTRAINT checks_cooldown_valid CHECK (cooldown_sec BETWEEN 0 AND 604800);
ALTER TABLE checks ADD CONSTRAINT checks_timeout_valid CHECK (timeout_sec BETWEEN 5 AND 86400);

CREATE TABLE IF NOT EXISTS check_executions (
    id            UUID PRIMARY KEY,
    check_id      UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    agent_id      UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'pending', -- pending | completed | timed_out | canceled
    dispatched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deadline_at   TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ,
    CONSTRAINT check_executions_status_valid
        CHECK (status IN ('pending', 'completed', 'timed_out', 'canceled')),
    CONSTRAINT check_executions_deadline_valid CHECK (deadline_at >= dispatched_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_check_execution_pending
    ON check_executions(check_id, agent_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_check_executions_deadline
    ON check_executions(deadline_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_check_executions_history
    ON check_executions(check_id, agent_id, dispatched_at DESC);

ALTER TABLE check_results ADD COLUMN IF NOT EXISTS execution_id UUID
    REFERENCES check_executions(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_check_results_execution
    ON check_results(execution_id) WHERE execution_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS check_states (
    check_id             UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    agent_id             UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status               TEXT NOT NULL DEFAULT 'unknown',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    consecutive_successes INTEGER NOT NULL DEFAULT 0,
    last_value           DOUBLE PRECISION,
    last_output          TEXT NOT NULL DEFAULT '',
    last_result_at       TIMESTAMPTZ,
    last_dispatch_at     TIMESTAMPTZ,
    reconciled_at        TIMESTAMPTZ,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (check_id, agent_id),
    CONSTRAINT check_states_status_valid
        CHECK (status IN ('unknown', 'passing', 'warning', 'failing')),
    CONSTRAINT check_states_counts_valid
        CHECK (consecutive_failures >= 0 AND consecutive_successes >= 0)
);
CREATE INDEX IF NOT EXISTS idx_check_states_agent ON check_states(agent_id, status);

-- Existing history remains useful immediately after upgrade: seed one current
-- state per check/device from its newest stored result.
INSERT INTO check_states (
    check_id, agent_id, status, consecutive_failures, consecutive_successes,
    last_value, last_output, last_result_at, updated_at
)
SELECT DISTINCT ON (check_id, agent_id)
    check_id,
    agent_id,
    status,
    CASE WHEN status IN ('warning', 'failing') THEN 1 ELSE 0 END,
    CASE WHEN status = 'passing' THEN 1 ELSE 0 END,
    value,
    output,
    ran_at,
    ran_at
FROM check_results
ORDER BY check_id, agent_id, ran_at DESC
ON CONFLICT (check_id, agent_id) DO NOTHING;

-- ===================== Alert lifecycle =====================

ALTER TABLE alerts ADD COLUMN IF NOT EXISTS condition_key TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS agent_snapshot_id UUID;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS agent_hostname TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS site_id UUID;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS client_id UUID;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS group_id UUID;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS first_seen_at TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS occurrence_count BIGINT NOT NULL DEFAULT 1;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS observed_value DOUBLE PRECISION;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS threshold_value DOUBLE PRECISION;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS output TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS version BIGINT NOT NULL DEFAULT 1;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS acked_at TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS acked_by TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS resolved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS resolution_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS resolution_note TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS reopened_from UUID REFERENCES alerts(id) ON DELETE SET NULL;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS last_notified_at TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS notification_pending BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS notification_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS notification_seq BIGINT NOT NULL DEFAULT 0;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS silenced_until TIMESTAMPTZ;
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS silenced_by TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS silence_reason TEXT NOT NULL DEFAULT '';

UPDATE alerts
SET first_seen_at = COALESCE(first_seen_at, created_at),
    last_seen_at = COALESCE(last_seen_at, created_at),
    updated_at = COALESCE(updated_at, resolved_at, created_at),
    occurrence_count = GREATEST(occurrence_count, 1);

UPDATE alerts al
SET agent_snapshot_id = COALESCE(al.agent_snapshot_id, a.id),
    agent_hostname = COALESCE(NULLIF(al.agent_hostname, ''), a.hostname),
    site_id = COALESCE(al.site_id, a.site_id),
    client_id = COALESCE(al.client_id, s.client_id),
    group_id = COALESCE(al.group_id, a.group_id)
FROM agents a
LEFT JOIN sites s ON s.id = a.site_id
WHERE al.agent_id = a.id;

ALTER TABLE alerts ALTER COLUMN first_seen_at SET NOT NULL;
ALTER TABLE alerts ALTER COLUMN last_seen_at SET NOT NULL;
ALTER TABLE alerts ALTER COLUMN updated_at SET NOT NULL;

-- Device deletion must not erase incident history. Snapshot columns above keep
-- old alerts scoped and readable after the live agent row is gone.
ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_agent_id_fkey;
ALTER TABLE alerts ADD CONSTRAINT alerts_agent_id_fkey
    FOREIGN KEY (agent_id) REFERENCES agents(id) ON DELETE SET NULL;

ALTER TABLE alerts ADD CONSTRAINT alerts_status_valid
    CHECK (status IN ('new', 'acked', 'suppressed', 'resolved'));
ALTER TABLE alerts ADD CONSTRAINT alerts_severity_valid
    CHECK (severity IN ('info', 'warning', 'error'));
ALTER TABLE alerts ADD CONSTRAINT alerts_source_type_valid
    CHECK (source_type IN ('check', 'task', 'agent', 'system', 'job', 'patch', 'update', 'backup', 'security'));
ALTER TABLE alerts ADD CONSTRAINT alerts_resolution_consistent
    CHECK ((status = 'resolved') = (resolved_at IS NOT NULL));
ALTER TABLE alerts ADD CONSTRAINT alerts_occurrence_count_valid CHECK (occurrence_count > 0);
ALTER TABLE alerts ADD CONSTRAINT alerts_version_valid CHECK (version > 0);
ALTER TABLE alerts ADD CONSTRAINT alerts_notification_valid
    CHECK (notification_seq >= 0 AND notification_kind IN ('', 'opened', 'resolved'));

-- Old nullable unique keys allowed duplicate open rows because NULL values are
-- distinct in a normal PostgreSQL unique index. Close all but the newest before
-- replacing it with a null-safe identity.
WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY source_type, source_id, agent_id, condition_key
               ORDER BY created_at DESC, id DESC
           ) AS rn
    FROM alerts
    WHERE status <> 'resolved'
)
UPDATE alerts a
SET status = 'resolved',
    resolved_at = now(),
    updated_at = now(),
    resolved_by = 'migration',
    resolution_kind = 'deduplicated',
    resolution_note = 'Closed while installing null-safe alert deduplication.'
FROM ranked r
WHERE a.id = r.id AND r.rn > 1;

DROP INDEX IF EXISTS uq_alert_open;
CREATE UNIQUE INDEX uq_alert_open
    ON alerts (
        source_type,
        COALESCE(source_id, '00000000-0000-0000-0000-000000000000'::uuid),
        COALESCE(agent_id, '00000000-0000-0000-0000-000000000000'::uuid),
        condition_key
    )
    WHERE status <> 'resolved';
CREATE INDEX IF NOT EXISTS idx_alerts_created ON alerts(created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_agent_open
    ON alerts(agent_id, created_at DESC) WHERE status <> 'resolved';
CREATE INDEX IF NOT EXISTS idx_alerts_agent_snapshot
    ON alerts(agent_snapshot_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_scope
    ON alerts(client_id, site_id, group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_source_history
    ON alerts(source_type, source_id, agent_id, condition_key, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_alerts_silenced_until
    ON alerts(silenced_until) WHERE status = 'suppressed';
CREATE INDEX IF NOT EXISTS idx_alerts_notification_pending
    ON alerts(updated_at, id) WHERE notification_pending;

CREATE TABLE IF NOT EXISTS alert_events (
    id             UUID PRIMARY KEY,
    alert_id       UUID NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    event_type     TEXT NOT NULL,
    actor          TEXT NOT NULL DEFAULT 'system',
    severity       TEXT,
    message        TEXT NOT NULL DEFAULT '',
    observed_value DOUBLE PRECISION,
    metadata       JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT alert_events_type_valid CHECK (
        event_type IN ('opened', 'occurrence', 'escalated', 'acknowledged',
                       'resolved', 'reopened', 'suppressed', 'unsuppressed')
    ),
    CONSTRAINT alert_events_severity_valid
        CHECK (severity IS NULL OR severity IN ('info', 'warning', 'error'))
);
CREATE INDEX IF NOT EXISTS idx_alert_events_alert
    ON alert_events(alert_id, created_at, id);

-- Backfill the opening event so existing incidents have a complete timeline.
INSERT INTO alert_events (id, alert_id, event_type, actor, severity, message, observed_value, created_at)
SELECT gen_random_uuid(), id, 'opened', 'system', severity, message, observed_value, created_at
FROM alerts
WHERE NOT EXISTS (SELECT 1 FROM alert_events e WHERE e.alert_id = alerts.id);

CREATE TABLE IF NOT EXISTS alert_suppressions (
    id          UUID PRIMARY KEY,
    kind        TEXT NOT NULL, -- silence | maintenance
    scope_type  TEXT NOT NULL, -- agent | check | policy | site | client | group
    scope_id    UUID NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at     TIMESTAMPTZ NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    canceled_at TIMESTAMPTZ,
    canceled_by TEXT NOT NULL DEFAULT '',
    CONSTRAINT alert_suppressions_kind_valid CHECK (kind IN ('silence', 'maintenance')),
    CONSTRAINT alert_suppressions_scope_valid
        CHECK (scope_type IN ('agent', 'check', 'policy', 'site', 'client', 'group')),
    CONSTRAINT alert_suppressions_window_valid CHECK (ends_at > starts_at)
);
CREATE INDEX IF NOT EXISTS idx_alert_suppressions_active
    ON alert_suppressions(scope_type, scope_id, starts_at, ends_at)
    WHERE canceled_at IS NULL;

-- ===================== Notification leases =====================

ALTER TABLE notifications ADD COLUMN IF NOT EXISTS dedupe_key TEXT NOT NULL DEFAULT '';
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS lease_token UUID;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE notifications ADD CONSTRAINT notifications_status_valid
    CHECK (status IN ('pending', 'processing', 'sent', 'failed'));
CREATE UNIQUE INDEX IF NOT EXISTS uq_notifications_dedupe
    ON notifications(dedupe_key) WHERE dedupe_key <> '';
CREATE INDEX IF NOT EXISTS idx_notifications_processing_lease
    ON notifications(lease_until) WHERE status = 'processing';

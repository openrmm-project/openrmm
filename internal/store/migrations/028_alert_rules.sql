-- Alert rules (docs/24-alert-rules.md). Every alert comes from a rule in
-- Checks: agent checks as before, and server rules (checks.type connectivity,
-- job_failure, patch_failure, reboot_pending, agent_outdated) that the server
-- evaluates from what it already knows. One server rule is in force per device
-- and type; with none, the fleet default below applies.
CREATE TABLE IF NOT EXISTS alert_rule_defaults (
    type        TEXT PRIMARY KEY
                CONSTRAINT alert_rule_defaults_type_valid
                CHECK (type IN ('connectivity', 'job_failure', 'patch_failure', 'reboot_pending', 'agent_outdated')),
    config      JSONB NOT NULL,
    updated_by  TEXT NOT NULL DEFAULT '',
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Connectivity keeps what every device did before (an error after 3 minutes);
-- the new types start off, so upgrading changes nothing by itself.
INSERT INTO alert_rule_defaults (type, config) VALUES
    ('connectivity',   '{"mode": "alert", "severity": "error", "after_min": 3}'),
    ('job_failure',    '{"mode": "ignore", "severity": "warning", "scope": "tasks"}'),
    ('patch_failure',  '{"mode": "ignore", "severity": "warning"}'),
    ('reboot_pending', '{"mode": "ignore", "severity": "warning", "after_days": 3}'),
    ('agent_outdated', '{"mode": "ignore", "severity": "info", "behind": "minor"}')
ON CONFLICT (type) DO NOTHING;

-- The rule that raised an alert; NULL for a fleet default. The alert's
-- identity stays the device and the condition, so a rule taking over from
-- another keeps the same open alert.
-- Keep the historical rule id after deletion: it must never be mistaken for
-- a fleet default by notification filters while the incident is reconciled.
ALTER TABLE alerts ADD COLUMN IF NOT EXISTS rule_id UUID;
CREATE INDEX IF NOT EXISTS idx_alerts_rule ON alerts(rule_id) WHERE rule_id IS NOT NULL;
UPDATE alerts al SET rule_id=al.source_id
WHERE al.source_type='check' AND al.rule_id IS NULL
  AND EXISTS (SELECT 1 FROM checks c WHERE c.id=al.source_id);
ALTER TABLE alerts DROP CONSTRAINT IF EXISTS alerts_source_type_valid;
ALTER TABLE alerts ADD CONSTRAINT alerts_source_type_valid CHECK (source_type IN
    ('check','task','agent','system','job','patch','update','backup','security','rule'));

-- When a pending reboot started, for reboot_pending's delay.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS reboot_pending_since TIMESTAMPTZ;
UPDATE agents SET reboot_pending_since = now() WHERE reboot_pending AND reboot_pending_since IS NULL;

-- Which rules reach a user's inbox. No row: all rules, as before.
CREATE TABLE IF NOT EXISTS notification_rule_filters (
    user_id          UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    mode             TEXT NOT NULL DEFAULT 'all'
                     CONSTRAINT notification_rule_filters_mode_valid CHECK (mode IN ('all', 'selected')),
    include_defaults BOOLEAN NOT NULL DEFAULT true,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS notification_rule_selections (
    user_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    check_id UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, check_id)
);
CREATE INDEX IF NOT EXISTS idx_notification_rule_selections_check ON notification_rule_selections(check_id);

-- Tie server-rule observations and job history to the device's current scope.
-- A move, including a move away and back, invalidates prefetched metadata.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS monitoring_scope_since TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE OR REPLACE FUNCTION advance_agent_monitoring_scope() RETURNS trigger AS $$
BEGIN
    IF OLD.site_id IS DISTINCT FROM NEW.site_id OR OLD.group_id IS DISTINCT FROM NEW.group_id THEN
        NEW.monitoring_scope_since = GREATEST(clock_timestamp(), OLD.monitoring_scope_since + interval '1 microsecond');
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS agents_monitoring_scope ON agents;
CREATE TRIGGER agents_monitoring_scope BEFORE UPDATE OF site_id, group_id ON agents
FOR EACH ROW EXECUTE FUNCTION advance_agent_monitoring_scope();

-- Capture scope at queue time, using the database clock/revision rather than
-- application timestamps. Pre-upgrade jobs have no proven scope and stay out.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS agent_monitoring_scope_since TIMESTAMPTZ;
CREATE OR REPLACE FUNCTION capture_job_monitoring_scope() RETURNS trigger AS $$
BEGIN
    SELECT monitoring_scope_since INTO NEW.agent_monitoring_scope_since
    FROM agents WHERE id=NEW.agent_id FOR SHARE;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
DROP TRIGGER IF EXISTS jobs_monitoring_scope ON jobs;
CREATE TRIGGER jobs_monitoring_scope BEFORE INSERT ON jobs
FOR EACH ROW EXECUTE FUNCTION capture_job_monitoring_scope();

-- Device-owned silences carry the same scope provenance as their incidents.
-- Legacy silences with unknown provenance remain visible to global admins but
-- must be recreated before applying to a device in its current scope.
ALTER TABLE alert_suppressions ADD COLUMN IF NOT EXISTS agent_monitoring_scope_since TIMESTAMPTZ;

-- RMM-AI database schema (PostgreSQL)
-- Current canonical schema snapshot. Versioned migrations live in internal/store/migrations.

-- ===================== Identity / RBAC =====================

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'tech', -- role key: admin | tech | readonly | custom
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS roles (
    role_key    TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    system      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role_key   TEXT NOT NULL REFERENCES roles(role_key) ON DELETE CASCADE,
    permission TEXT NOT NULL,
    PRIMARY KEY (role_key, permission)
);

INSERT INTO roles (role_key, name, description, system) VALUES
    ('readonly', 'Read-only', 'View devices, clients, sites, and start remote desktop sessions.', TRUE),
    ('tech', 'Technician', 'Operate the fleet: jobs, alerts, scripts, patches, reports, and remote tools.', TRUE),
    ('admin', 'Administrator', 'Full access to every module and administrative setting.', TRUE)
ON CONFLICT (role_key) DO UPDATE SET
    name = EXCLUDED.name,
    description = EXCLUDED.description,
    system = TRUE;

INSERT INTO role_permissions (role_key, permission)
SELECT 'readonly', permission FROM (VALUES
    ('devices.view'), ('devices.remote'), ('clients.view')
) AS p(permission)
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_key, permission)
SELECT 'tech', permission FROM (VALUES
    ('ai_chat.use'), ('mcp.use'),
    ('alerts.view'), ('alerts.manage'),
    ('jobs.view'), ('jobs.create'), ('jobs.manage'),
    ('checks.view'),
    ('tasks.view'),
    ('policies.view'),
    ('scripts.view'),
    ('patches.view'), ('patches.manage'),
    ('devices.view'), ('devices.manage'), ('devices.remote'), ('devices.files'), ('devices.terminal'),
    ('clients.view'),
    ('reports.view')
) AS p(permission)
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_key, permission)
SELECT 'admin', permission FROM (VALUES
    ('ai_chat.use'), ('mcp.use'),
    ('alerts.view'), ('alerts.manage'),
    ('jobs.view'), ('jobs.create'), ('jobs.manage'),
    ('checks.view'), ('checks.manage'),
    ('tasks.view'), ('tasks.manage'),
    ('policies.view'), ('policies.manage'),
    ('scripts.view'), ('scripts.manage'),
    ('patches.view'), ('patches.manage'), ('patches.policies.manage'),
    ('devices.view'), ('devices.manage'), ('devices.delete'), ('devices.remote'), ('devices.files'), ('devices.terminal'),
    ('clients.view'), ('clients.manage'),
    ('reports.view'), ('audit.view'),
    ('settings.manage'), ('enrollment.manage'), ('api_tokens.manage'), ('users.manage'), ('users.email.manage'), ('roles.manage')
) AS p(permission)
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_key, permission)
SELECT role_key, 'jobs.create'
FROM role_permissions
WHERE permission='jobs.manage'
ON CONFLICT DO NOTHING;

-- Per-user TOTP two-factor. Secret is AES-GCM encrypted at rest (enc:v1:…).
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret  TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT false;
-- A replacement secret remains pending until it is confirmed. The active
-- secret and enabled flag are never weakened during rotation.
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_pending_secret TEXT;

-- The user's address: where their alerts are delivered, and where a password
-- reset link is sent. Optional, so console-only accounts keep working.
--
-- Because a reset resolves an account through it, this is an authentication
-- factor and not merely a contact detail: it is unique, and only
-- users.email.manage may change it (see migration 004).
ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT;

-- Case-insensitive uniqueness, but only for users that actually have an address.
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_key
    ON users (lower(email)) WHERE email IS NOT NULL AND email <> '';

-- Disabling a user, as an alternative to deleting one: access stops, the record
-- of what the account did survives. A timestamp rather than a boolean, because
-- "when was access withdrawn" is the question an incident review asks.
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_users_disabled
    ON users (disabled_at) WHERE disabled_at IS NOT NULL;

-- One-time 2FA recovery codes. Stored hashed (SHA-256); single-use (used_at set
-- on consumption). Used to log in when the authenticator is lost.
CREATE TABLE IF NOT EXISTS user_recovery_codes (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_recovery_user ON user_recovery_codes(user_id);

CREATE TABLE IF NOT EXISTS sessions (
    id          UUID PRIMARY KEY,
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS api_tokens (
    id          UUID PRIMARY KEY,
    token_hash  TEXT NOT NULL UNIQUE,
    name        TEXT NOT NULL DEFAULT '',
    role        TEXT NOT NULL DEFAULT 'admin',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Bind an API token (e.g. an MCP connector) to a user so it inherits that
-- user's access scope. NULL = unscoped (full, current behavior).
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE;
-- Optional expiry: an expired token stops authenticating (checked in APITokenRole).
-- NULL = never expires (current behavior). Short-lived tokens shrink the blast
-- radius of a leaked MCP connector secret (esp. for public/cloud exposure).
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

-- Per-user access scope. A user with NO rows has full access (all clients);
-- any rows restrict the user to the union of those client/site/group/agent subtrees.
-- Scopes apply to every non-admin role; admins always have full access.
CREATE TABLE IF NOT EXISTS user_scopes (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL, -- client | site | group | agent
    scope_id   UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, scope_type, scope_id)
);
CREATE INDEX IF NOT EXISTS idx_user_scopes_user ON user_scopes(user_id);
-- scope_id is polymorphic, so no foreign key can cover it. Triggers on each
-- referenced table stand in for one — see migration 006. Without them a deleted
-- device leaves a row that the scope editor cannot draw and that makes every
-- subsequent save for that user fail validation. Accounts left with no rows at
-- all are disabled rather than promoted to full fleet access.

-- Editable instance configuration. Secret values are encrypted by the server
-- before storage and are never returned by the API.
CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    is_secret  BOOLEAN NOT NULL DEFAULT false,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ===================== Org hierarchy (Client -> Site -> Agent) =====================

CREATE TABLE IF NOT EXISTS clients (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sites (
    id          UUID PRIMARY KEY,
    client_id   UUID NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_sites_client ON sites(client_id);

-- A device group is a DEPARTMENT under a site (Empresa > Site > Departamento).
-- site_id binds it to the hierarchy so a group never spans companies/sites.
CREATE TABLE IF NOT EXISTS device_groups (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL,
    site_id     UUID REFERENCES sites(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE device_groups ADD COLUMN IF NOT EXISTS site_id UUID REFERENCES sites(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_device_groups_site ON device_groups(site_id);

CREATE TABLE IF NOT EXISTS agents (
    id           UUID PRIMARY KEY,
    hostname     TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    os           TEXT NOT NULL DEFAULT '',
    arch         TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'offline',
    token_hash   TEXT NOT NULL,
    site_id      UUID REFERENCES sites(id) ON DELETE SET NULL,
    group_id     UUID REFERENCES device_groups(id) ON DELETE SET NULL,
    tags         TEXT[] NOT NULL DEFAULT '{}',
    mesh_id      TEXT,
    token_rotation_canceling BOOLEAN NOT NULL DEFAULT FALSE,
    last_seen    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- For pre-existing installs:
ALTER TABLE agents ADD COLUMN IF NOT EXISTS site_id UUID REFERENCES sites(id) ON DELETE SET NULL;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS note        TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN IF NOT EXISTS pending_token_hash TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS token_rotation_id TEXT;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS pending_token_expires_at TIMESTAMPTZ;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS token_rotation_canceling BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS enroll_tokens (
    id          UUID PRIMARY KEY,
    token       TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    site_id     UUID REFERENCES sites(id) ON DELETE SET NULL,
    uses_left   INTEGER NOT NULL DEFAULT 1,
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS site_id UUID REFERENCES sites(id) ON DELETE SET NULL;

-- ===================== Scripts & Jobs =====================

CREATE TABLE IF NOT EXISTS scripts (
    id           UUID PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    category     TEXT NOT NULL DEFAULT '',
    shell        TEXT NOT NULL DEFAULT 'bash',
    body         TEXT NOT NULL DEFAULT '',
    default_args TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The catalogue of script categories. scripts.category keeps the name rather
-- than a foreign key: this table exists so a category can be named before any
-- script is filed under it, not to own the relationship. Listing unions both, so
-- a name only a script carries is still visible. See migration 007.
CREATE TABLE IF NOT EXISTS script_categories (
    id         UUID PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Case-insensitive: "Networking" and "networking" are the same category to
-- everyone except a byte comparison.
CREATE UNIQUE INDEX IF NOT EXISTS script_categories_name_key
    ON script_categories (lower(name));

CREATE TABLE IF NOT EXISTS jobs (
    id                UUID PRIMARY KEY,
    agent_id          UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    type              TEXT NOT NULL DEFAULT 'run_script',
    description       TEXT NOT NULL DEFAULT '',
    shell             TEXT NOT NULL DEFAULT 'bash',
    script            TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'awaiting_approval',
    requires_approval BOOLEAN NOT NULL DEFAULT true,
    created_by        TEXT NOT NULL DEFAULT '',
    approved_by       TEXT,
    task_id           UUID,
    batch_id          UUID,
    exit_code         INTEGER,
    stdout            TEXT NOT NULL DEFAULT '',
    stderr            TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_jobs_agent ON jobs(agent_id);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS task_id UUID;
CREATE INDEX IF NOT EXISTS idx_jobs_task ON jobs(task_id);
-- Bulk/mass-deployment grouping: jobs created together share a batch_id.
-- (ADD COLUMN before the index so it works on pre-existing jobs tables too.)
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS batch_id UUID;
CREATE INDEX IF NOT EXISTS idx_jobs_batch ON jobs(batch_id);

-- ===================== Inventory =====================

CREATE TABLE IF NOT EXISTS inventory (
    agent_id     UUID PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    data         JSONB NOT NULL DEFAULT '{}',
    collected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Speeds up the inventory reports' JSONB lookups (software/hardware aggregation).
CREATE INDEX IF NOT EXISTS idx_inventory_data ON inventory USING gin (data);

-- ===================== Policies =====================

CREATE TABLE IF NOT EXISTS policies (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- AI provenance. proposed_by is permanent ('' = a person authored it);
    -- approved_by names whoever reviewed it. See migration 010.
    proposed_by TEXT NOT NULL DEFAULT '',
    approved_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_policies_awaiting_review ON policies (created_at DESC)
    WHERE proposed_by <> '' AND approved_by = '';

-- A policy is attached to a scope: an agent, a site, or a client. Agents inherit
-- every policy whose scope covers them.
CREATE TABLE IF NOT EXISTS policy_assignments (
    id          UUID PRIMARY KEY,
    policy_id   UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    scope_type  TEXT NOT NULL, -- client | site | agent
    scope_id    UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_policy_assign_scope ON policy_assignments(scope_type, scope_id);

-- ===================== Checks (monitoring) =====================
-- A check is owned either directly by an agent (agent_id) or by a policy
-- (policy_id). Exactly one of the two is set.

CREATE TABLE IF NOT EXISTS checks (
    id              UUID PRIMARY KEY,
    agent_id        UUID REFERENCES agents(id) ON DELETE CASCADE,
    policy_id       UUID REFERENCES policies(id) ON DELETE CASCADE,
    type            TEXT NOT NULL, -- diskspace | memory | cpuload | ping | winservice | script
    name            TEXT NOT NULL DEFAULT '',
    config          JSONB NOT NULL DEFAULT '{}',
    warn_threshold  DOUBLE PRECISION,
    err_threshold   DOUBLE PRECISION,
    interval_sec    INTEGER NOT NULL DEFAULT 300,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    failure_count   INTEGER NOT NULL DEFAULT 2
                        CONSTRAINT checks_failure_count_valid CHECK (failure_count BETWEEN 1 AND 100),
    recovery_count  INTEGER NOT NULL DEFAULT 2
                        CONSTRAINT checks_recovery_count_valid CHECK (recovery_count BETWEEN 1 AND 100),
    hysteresis      DOUBLE PRECISION NOT NULL DEFAULT 2
                        CONSTRAINT checks_hysteresis_valid CHECK (hysteresis BETWEEN 0 AND 100),
    cooldown_sec    INTEGER NOT NULL DEFAULT 300
                        CONSTRAINT checks_cooldown_valid CHECK (cooldown_sec BETWEEN 0 AND 604800),
    timeout_sec     INTEGER NOT NULL DEFAULT 180
                        CONSTRAINT checks_timeout_valid CHECK (timeout_sec BETWEEN 5 AND 86400),
    proposed_by     TEXT NOT NULL DEFAULT '',
    approved_by     TEXT NOT NULL DEFAULT '',
    -- An AI proposal nobody has reviewed must not be enabled: the scheduler
    -- selects WHERE enabled, so this keeps it from ever running unseen.
    CONSTRAINT checks_unreviewed_proposal_disabled
        CHECK (proposed_by = '' OR approved_by <> '' OR NOT enabled)
);
CREATE INDEX IF NOT EXISTS idx_checks_agent ON checks(agent_id);
CREATE INDEX IF NOT EXISTS idx_checks_awaiting_review ON checks (created_at DESC)
    WHERE proposed_by <> '' AND approved_by = '';
CREATE INDEX IF NOT EXISTS idx_checks_policy ON checks(policy_id);

CREATE TABLE IF NOT EXISTS check_executions (
    id            UUID PRIMARY KEY,
    check_id      UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    agent_id      UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status        TEXT NOT NULL DEFAULT 'pending'
                      CONSTRAINT check_executions_status_valid
                      CHECK (status IN ('pending', 'completed', 'timed_out', 'canceled')),
    dispatched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    deadline_at   TIMESTAMPTZ NOT NULL,
    completed_at  TIMESTAMPTZ,
    CONSTRAINT check_executions_deadline_valid CHECK (deadline_at >= dispatched_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_check_execution_pending
    ON check_executions(check_id, agent_id) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_check_executions_deadline
    ON check_executions(deadline_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_check_executions_history
    ON check_executions(check_id, agent_id, dispatched_at DESC);

-- Per-(check, agent) latest status + history.
CREATE TABLE IF NOT EXISTS check_results (
    id          UUID PRIMARY KEY,
    check_id    UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status      TEXT NOT NULL, -- passing | warning | failing
    value       DOUBLE PRECISION,
    output      TEXT NOT NULL DEFAULT '',
    ran_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    execution_id UUID REFERENCES check_executions(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_check_results_lookup ON check_results(check_id, agent_id, ran_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_check_results_execution
    ON check_results(execution_id) WHERE execution_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS check_states (
    check_id              UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    agent_id              UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status                TEXT NOT NULL DEFAULT 'unknown'
                               CONSTRAINT check_states_status_valid
                               CHECK (status IN ('unknown', 'passing', 'warning', 'failing')),
    consecutive_failures  INTEGER NOT NULL DEFAULT 0,
    consecutive_successes INTEGER NOT NULL DEFAULT 0,
    last_value            DOUBLE PRECISION,
    last_output           TEXT NOT NULL DEFAULT '',
    last_result_at        TIMESTAMPTZ,
    last_dispatch_at      TIMESTAMPTZ,
    reconciled_at         TIMESTAMPTZ,
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (check_id, agent_id),
    CONSTRAINT check_states_counts_valid
        CHECK (consecutive_failures >= 0 AND consecutive_successes >= 0)
);
CREATE INDEX IF NOT EXISTS idx_check_states_agent ON check_states(agent_id, status);

-- ===================== Tasks (scheduled automation) =====================
-- Like checks, a task is owned by an agent or a policy.

CREATE TABLE IF NOT EXISTS tasks (
    id            UUID PRIMARY KEY,
    agent_id      UUID REFERENCES agents(id) ON DELETE CASCADE,
    policy_id     UUID REFERENCES policies(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    script_id     UUID REFERENCES scripts(id) ON DELETE SET NULL,
    shell         TEXT NOT NULL DEFAULT 'bash',
    script        TEXT NOT NULL DEFAULT '',     -- inline fallback if no script_id
    schedule_kind TEXT NOT NULL DEFAULT 'interval', -- interval | daily | weekly
    interval_sec  INTEGER NOT NULL DEFAULT 3600,
    run_at_minute INTEGER NOT NULL DEFAULT 0,    -- for daily: minutes since midnight (UTC)
    days_of_week  SMALLINT NOT NULL DEFAULT 0,   -- for weekly: bitmask, bit N = time.Weekday N (0=Sun)
    run_at_minutes INTEGER[] NOT NULL DEFAULT '{}', -- for weekly: times of day (minutes since midnight, UTC)
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    proposed_by   TEXT NOT NULL DEFAULT '',
    approved_by   TEXT NOT NULL DEFAULT '',
    -- A task is a script with a schedule, run pre-approved by the scheduler. An
    -- unreviewed AI proposal must never reach it.
    CONSTRAINT tasks_unreviewed_proposal_disabled
        CHECK (proposed_by = '' OR approved_by <> '' OR NOT enabled)
);
CREATE INDEX IF NOT EXISTS idx_tasks_agent ON tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_awaiting_review ON tasks (created_at DESC)
    WHERE proposed_by <> '' AND approved_by = '';
-- Weekly schedule (days-of-week bitmask + multiple times per day). Added after
-- the original interval/daily kinds; back-fills empty on existing rows.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS days_of_week SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS run_at_minutes INTEGER[] NOT NULL DEFAULT '{}';
CREATE INDEX IF NOT EXISTS idx_tasks_policy ON tasks(policy_id);

-- ===================== Alerts =====================

CREATE TABLE IF NOT EXISTS alerts (
    id              UUID PRIMARY KEY,
    agent_id        UUID REFERENCES agents(id) ON DELETE SET NULL,
    source_type     TEXT NOT NULL CONSTRAINT alerts_source_type_valid CHECK (source_type IN
                        ('check', 'task', 'agent', 'system', 'job', 'patch', 'update', 'backup', 'security')),
    source_id       UUID,
    severity        TEXT NOT NULL DEFAULT 'warning'
                            CONSTRAINT alerts_severity_valid CHECK (severity IN ('info', 'warning', 'error')),
    message         TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'new'
                            CONSTRAINT alerts_status_valid CHECK (status IN ('new', 'acked', 'suppressed', 'resolved')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ,
    condition_key   TEXT NOT NULL DEFAULT '',
    agent_snapshot_id UUID,
    agent_hostname  TEXT NOT NULL DEFAULT '',
    site_id         UUID,
    client_id       UUID,
    group_id        UUID,
    first_seen_at   TIMESTAMPTZ NOT NULL,
    last_seen_at    TIMESTAMPTZ NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL,
    occurrence_count BIGINT NOT NULL DEFAULT 1
                              CONSTRAINT alerts_occurrence_count_valid CHECK (occurrence_count > 0),
    observed_value  DOUBLE PRECISION,
    threshold_value DOUBLE PRECISION,
    output          TEXT NOT NULL DEFAULT '',
    version         BIGINT NOT NULL DEFAULT 1
                            CONSTRAINT alerts_version_valid CHECK (version > 0),
    acked_at        TIMESTAMPTZ,
    acked_by        TEXT NOT NULL DEFAULT '',
    resolved_by     TEXT NOT NULL DEFAULT '',
    resolution_kind TEXT NOT NULL DEFAULT '',
    resolution_note TEXT NOT NULL DEFAULT '',
    reopened_from   UUID REFERENCES alerts(id) ON DELETE SET NULL,
    last_notified_at TIMESTAMPTZ,
    notification_pending BOOLEAN NOT NULL DEFAULT false,
    notification_kind TEXT NOT NULL DEFAULT '',
    notification_seq BIGINT NOT NULL DEFAULT 0,
    silenced_until  TIMESTAMPTZ,
    silenced_by     TEXT NOT NULL DEFAULT '',
    silence_reason  TEXT NOT NULL DEFAULT '',
    -- Identity of the observation that last changed this alert. A caller that
    -- can be retried supplies a stable key and the repeat is recognised; one
    -- that supplies none is always a new occurrence. Inferring the repeat from
    -- timestamp and content collapsed distinct simultaneous observations, which
    -- is what migration 009 replaced.
    last_observation_id TEXT NOT NULL DEFAULT '',
    CONSTRAINT alerts_resolution_consistent
        CHECK ((status = 'resolved') = (resolved_at IS NOT NULL)),
    CONSTRAINT alerts_notification_valid
        CHECK (notification_seq >= 0 AND notification_kind IN ('', 'opened', 'resolved'))
);
CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status, created_at DESC);
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
-- At most one open alert per null-safe source identity.
CREATE UNIQUE INDEX IF NOT EXISTS uq_alert_open
    ON alerts(
        source_type,
        COALESCE(source_id, '00000000-0000-0000-0000-000000000000'::uuid),
        COALESCE(agent_id, '00000000-0000-0000-0000-000000000000'::uuid),
        condition_key
    )
    WHERE status <> 'resolved';

CREATE TABLE IF NOT EXISTS alert_events (
    id             UUID PRIMARY KEY,
    alert_id       UUID NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
    event_type     TEXT NOT NULL CONSTRAINT alert_events_type_valid CHECK (event_type IN
                       ('opened', 'occurrence', 'escalated', 'acknowledged',
                        'resolved', 'reopened', 'suppressed', 'unsuppressed')),
    actor          TEXT NOT NULL DEFAULT 'system',
    severity       TEXT CONSTRAINT alert_events_severity_valid
                        CHECK (severity IS NULL OR severity IN ('info', 'warning', 'error')),
    message        TEXT NOT NULL DEFAULT '',
    observed_value DOUBLE PRECISION,
    metadata       JSONB NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_alert_events_alert
    ON alert_events(alert_id, created_at, id);

CREATE TABLE IF NOT EXISTS alert_suppressions (
    id          UUID PRIMARY KEY,
    kind        TEXT NOT NULL CONSTRAINT alert_suppressions_kind_valid
                         CHECK (kind IN ('silence', 'maintenance')),
    scope_type  TEXT NOT NULL CONSTRAINT alert_suppressions_scope_valid
                         CHECK (scope_type IN ('agent', 'check', 'policy', 'site', 'client', 'group')),
    scope_id    UUID NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at     TIMESTAMPTZ NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    canceled_at TIMESTAMPTZ,
    canceled_by TEXT NOT NULL DEFAULT '',
    CONSTRAINT alert_suppressions_window_valid CHECK (ends_at > starts_at)
);
CREATE INDEX IF NOT EXISTS idx_alert_suppressions_active
    ON alert_suppressions(scope_type, scope_id, starts_at, ends_at)
    WHERE canceled_at IS NULL;

-- ===================== Remote sessions =====================

CREATE TABLE IF NOT EXISTS remote_sessions (
    id          UUID PRIMARY KEY,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    provider    TEXT NOT NULL DEFAULT 'native',
    status      TEXT NOT NULL DEFAULT 'pending',
    token       TEXT NOT NULL,
    connect_url TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

-- Native relay tickets are separate per peer, hashed at rest, and consumed once.
ALTER TABLE remote_sessions ADD COLUMN IF NOT EXISTS viewer_ticket_hash TEXT;
ALTER TABLE remote_sessions ADD COLUMN IF NOT EXISTS agent_ticket_hash TEXT;
ALTER TABLE remote_sessions ADD COLUMN IF NOT EXISTS viewer_used_at TIMESTAMPTZ;
ALTER TABLE remote_sessions ADD COLUMN IF NOT EXISTS agent_used_at TIMESTAMPTZ;

-- A revoked device loses its control credential immediately. Keep the timestamp
-- rather than deleting the row so the audit and inventory history remain intact.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;

-- ===================== Audit =====================

CREATE TABLE IF NOT EXISTS audit_log (
    id          UUID PRIMARY KEY,
    actor       TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL DEFAULT '',
    target      TEXT NOT NULL DEFAULT '',
    detail      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at DESC);

-- ===================== Chat (AI) =====================

CREATE TABLE IF NOT EXISTS chat_sessions (
    id          UUID PRIMARY KEY,
    title       TEXT NOT NULL DEFAULT 'New chat',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Owner (actor) of a chat session, so users only see their own AI conversations.
ALTER TABLE chat_sessions ADD COLUMN IF NOT EXISTS created_by TEXT;

CREATE TABLE IF NOT EXISTS chat_messages (
    id           UUID PRIMARY KEY,
    session_id   UUID NOT NULL REFERENCES chat_sessions(id) ON DELETE CASCADE,
    role         TEXT NOT NULL,
    content      TEXT NOT NULL DEFAULT '',
    tool_calls   JSONB,
    tool_call_id TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_chat_messages_session ON chat_messages(session_id, created_at);

-- ===================== Patch management =====================
ALTER TABLE agents ADD COLUMN IF NOT EXISTS reboot_pending  BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS last_patch_scan TIMESTAMPTZ;

-- Catalog of distinct patches seen across the fleet, deduped per (os, identifier).
CREATE TABLE IF NOT EXISTS patches (
    id              UUID PRIMARY KEY,
    os              TEXT NOT NULL,
    identifier      TEXT NOT NULL,                  -- KB id / package name / advisory id
    title           TEXT NOT NULL DEFAULT '',
    classification  TEXT NOT NULL DEFAULT 'other',  -- security|critical|important|moderate|optional|other
    kb              TEXT NOT NULL DEFAULT '',
    cves            TEXT[] NOT NULL DEFAULT '{}',
    reboot_required BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (os, identifier)
);

-- Per-device missing/available patch state.
CREATE TABLE IF NOT EXISTS agent_patches (
    agent_id          UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    patch_id          UUID NOT NULL REFERENCES patches(id) ON DELETE CASCADE,
    status            TEXT NOT NULL DEFAULT 'missing', -- missing|approved|installing|installed|failed|excluded
    available_version TEXT NOT NULL DEFAULT '',
    detected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    installed_at      TIMESTAMPTZ,
    error             TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (agent_id, patch_id)
);
CREATE INDEX IF NOT EXISTS idx_agent_patches_agent  ON agent_patches(agent_id);
CREATE INDEX IF NOT EXISTS idx_agent_patches_status ON agent_patches(status);

-- Approval decisions, scoped (global when scope_type/scope_id are empty).
CREATE TABLE IF NOT EXISTS patch_approvals (
    id          UUID PRIMARY KEY,
    patch_id    UUID NOT NULL REFERENCES patches(id) ON DELETE CASCADE,
    scope_type  TEXT NOT NULL DEFAULT '',  -- ''=global | client|site|agent
    scope_id    TEXT NOT NULL DEFAULT '',
    decision    TEXT NOT NULL DEFAULT 'approve', -- approve|decline
    source      TEXT NOT NULL DEFAULT 'manual',  -- manual|policy
    actor       TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (patch_id, scope_type, scope_id)
);

-- Patch policies (maintenance window, auto-approval, exclusions, reboot).
CREATE TABLE IF NOT EXISTS patch_policies (
    id                    UUID PRIMARY KEY,
    name                  TEXT NOT NULL,
    enabled               BOOLEAN NOT NULL DEFAULT TRUE,
    auto_approve          TEXT[] NOT NULL DEFAULT '{}', -- classifications to auto-approve
    auto_approve_age_days INT NOT NULL DEFAULT 7,
    exclusions            TEXT[] NOT NULL DEFAULT '{}', -- identifiers or classifications to never install
    window_days           INT[] NOT NULL DEFAULT '{}',  -- 0=Sun..6=Sat; empty = any day
    window_start_min      INT NOT NULL DEFAULT 0,       -- minutes from midnight (server time)
    window_end_min        INT NOT NULL DEFAULT 1439,
    reboot                TEXT NOT NULL DEFAULT 'if_required', -- never|if_required|always
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS patch_policy_assignments (
    id          UUID PRIMARY KEY,
    policy_id   UUID NOT NULL REFERENCES patch_policies(id) ON DELETE CASCADE,
    scope_type  TEXT NOT NULL,  -- client|site|agent
    scope_id    UUID NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_patch_policy_assign ON patch_policy_assignments(policy_id);

-- ===================== MCP OAuth 2.1 (Authorization Server) =====================
-- The RMM acts as its own OAuth AS + Resource Server so cloud MCP clients (Claude
-- web, ChatGPT) can connect via the standard connector flow. Codes and tokens are
-- stored HASHED (SHA-256), never in the clear — same pattern as api_tokens/sessions.

-- Registered OAuth clients (via Dynamic Client Registration, RFC 7591).
CREATE TABLE IF NOT EXISTS oauth_clients (
    client_id     TEXT PRIMARY KEY,
    name          TEXT NOT NULL DEFAULT '',
    redirect_uris TEXT[] NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Short-lived authorization codes (PKCE S256). Deleted on exchange.
CREATE TABLE IF NOT EXISTS oauth_codes (
    code_hash      TEXT PRIMARY KEY,      -- SHA-256 of the authorization code
    client_id      TEXT NOT NULL,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,         -- PKCE code_challenge (S256)
    scope          TEXT NOT NULL DEFAULT '',
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Access + refresh tokens issued to a connector, bound to a user (inherits that
-- user's role + access scope). An expired/absent access token stops authenticating.
CREATE TABLE IF NOT EXISTS oauth_tokens (
    token_hash         TEXT PRIMARY KEY,        -- SHA-256 of the access token
    refresh_hash       TEXT UNIQUE,             -- SHA-256 of the refresh token (nullable)
    client_id          TEXT NOT NULL,
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope              TEXT NOT NULL DEFAULT '',
    expires_at         TIMESTAMPTZ NOT NULL,    -- access-token expiry (short)
    refresh_expires_at TIMESTAMPTZ,             -- refresh-token expiry (longer; NULL = no refresh)
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_oauth_tokens_user ON oauth_tokens(user_id);
-- Bounded refresh-token lifetime (added after the initial oauth_tokens table).
ALTER TABLE oauth_tokens ADD COLUMN IF NOT EXISTS refresh_expires_at TIMESTAMPTZ;

-- ===================== Email notifications =====================
-- Recipients are users, never free-text address lists: an address list would let
-- a misconfiguration leak one client's hostnames to another client's contact,
-- while a user carries a scope the delivery query can enforce (users.email above).

-- One row per user per event type. Absence means "use the default", so a new
-- event type added later does not silently start mailing everyone.
CREATE TABLE IF NOT EXISTS notification_subscriptions (
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type TEXT        NOT NULL, -- check_failed | check_recovered | agent_offline | job_failed
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, event_type)
);

-- Outbox. Delivery never happens inline with the scheduler tick: an SMTP server
-- that hangs would stall check evaluation for the whole fleet. Rows survive a
-- restart, which matters because restarts and outages correlate.
CREATE TABLE IF NOT EXISTS notifications (
    id              UUID PRIMARY KEY,
    event_type      TEXT        NOT NULL,
    alert_id        UUID        REFERENCES alerts(id) ON DELETE SET NULL,
    agent_id        UUID        REFERENCES agents(id) ON DELETE SET NULL,
    user_id         UUID        REFERENCES users(id) ON DELETE CASCADE,
    recipient       TEXT        NOT NULL,
    subject         TEXT        NOT NULL,
    body            TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'pending'
                                CONSTRAINT notifications_status_valid
                                CHECK (status IN ('pending', 'processing', 'sent', 'failed')),
    attempts        INT         NOT NULL DEFAULT 0,
    last_error      TEXT        NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    dedupe_key      TEXT        NOT NULL DEFAULT '',
    lease_token     UUID,
    lease_until     TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Drives the worker's claim query; partial so it stays small as sent rows pile up.
CREATE INDEX IF NOT EXISTS idx_notifications_pending
    ON notifications (next_attempt_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_notifications_created ON notifications (created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS uq_notifications_dedupe
    ON notifications(dedupe_key) WHERE dedupe_key <> '';
CREATE INDEX IF NOT EXISTS idx_notifications_processing_lease
    ON notifications(lease_until) WHERE status = 'processing';

-- Password reset. Only the hash is stored: a database leak must not hand out
-- working reset links, exactly like the agent enrollment tokens.
CREATE TABLE IF NOT EXISTS password_resets (
    id         UUID        PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT        NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_password_resets_user
    ON password_resets (user_id) WHERE used_at IS NULL;

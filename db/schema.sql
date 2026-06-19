-- RMM-AI database schema (PostgreSQL)
-- Idempotent: safe to run on every server start.

-- ===================== Identity / RBAC =====================

CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'tech', -- admin | tech | readonly
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Per-user TOTP two-factor. Secret is AES-GCM encrypted at rest (enc:v1:…).
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret  TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT false;

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

-- Per-user access scope. A user with NO rows has full access (all clients);
-- any rows restrict the user to the union of those client/site/group/agent subtrees.
-- Scopes apply to tech/readonly; admins always have full access.
CREATE TABLE IF NOT EXISTS user_scopes (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_type TEXT NOT NULL, -- client | site | group | agent
    scope_id   UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, scope_type, scope_id)
);
CREATE INDEX IF NOT EXISTS idx_user_scopes_user ON user_scopes(user_id);

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
    last_seen    TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- For pre-existing installs:
ALTER TABLE agents ADD COLUMN IF NOT EXISTS site_id UUID REFERENCES sites(id) ON DELETE SET NULL;
ALTER TABLE agents ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';
-- The RustDesk provider was dropped; remove its column from existing installs.
ALTER TABLE agents DROP COLUMN IF EXISTS rustdesk_id;

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
CREATE INDEX IF NOT EXISTS idx_jobs_task ON jobs(task_id);
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS task_id UUID;
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

-- ===================== Policies =====================

CREATE TABLE IF NOT EXISTS policies (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

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
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_checks_agent ON checks(agent_id);
CREATE INDEX IF NOT EXISTS idx_checks_policy ON checks(policy_id);

-- Per-(check, agent) latest status + history.
CREATE TABLE IF NOT EXISTS check_results (
    id          UUID PRIMARY KEY,
    check_id    UUID NOT NULL REFERENCES checks(id) ON DELETE CASCADE,
    agent_id    UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    status      TEXT NOT NULL, -- passing | warning | failing
    value       DOUBLE PRECISION,
    output      TEXT NOT NULL DEFAULT '',
    ran_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_check_results_lookup ON check_results(check_id, agent_id, ran_at DESC);

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
    schedule_kind TEXT NOT NULL DEFAULT 'interval', -- interval | daily
    interval_sec  INTEGER NOT NULL DEFAULT 3600,
    run_at_minute INTEGER NOT NULL DEFAULT 0,    -- for daily: minutes since midnight (UTC)
    enabled       BOOLEAN NOT NULL DEFAULT true,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_tasks_agent ON tasks(agent_id);
CREATE INDEX IF NOT EXISTS idx_tasks_policy ON tasks(policy_id);

-- ===================== Alerts =====================

CREATE TABLE IF NOT EXISTS alerts (
    id          UUID PRIMARY KEY,
    agent_id    UUID REFERENCES agents(id) ON DELETE CASCADE,
    source_type TEXT NOT NULL,  -- check | task | agent
    source_id   UUID,
    severity    TEXT NOT NULL DEFAULT 'warning', -- info | warning | error
    message     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'new', -- new | acked | resolved
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_alerts_status ON alerts(status, created_at DESC);
-- At most one open alert per (source_type, source_id, agent).
CREATE UNIQUE INDEX IF NOT EXISTS uq_alert_open
    ON alerts(source_type, source_id, agent_id)
    WHERE status <> 'resolved';

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

-- Email notifications (checks, offline agents, failed jobs) and password reset.

-- Recipients are users, never free-text address lists: an address list would let
-- a misconfiguration leak one client's hostnames to another client's contact,
-- while a user carries a scope the delivery query can enforce.
ALTER TABLE users ADD COLUMN IF NOT EXISTS email TEXT;

-- Case-insensitive uniqueness, but only for users that actually have an address;
-- email stays optional so console-only accounts keep working.
CREATE UNIQUE INDEX IF NOT EXISTS users_email_lower_key
    ON users (lower(email)) WHERE email IS NOT NULL AND email <> '';

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
    status          TEXT        NOT NULL DEFAULT 'pending', -- pending | sent | failed
    attempts        INT         NOT NULL DEFAULT 0,
    last_error      TEXT        NOT NULL DEFAULT '',
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ
);

-- Drives the worker's claim query; partial so it stays small as sent rows pile up.
CREATE INDEX IF NOT EXISTS idx_notifications_pending
    ON notifications (next_attempt_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_notifications_created ON notifications (created_at DESC);

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

-- The user of a device's active interactive session, as its agent last
-- reported it with a heartbeat (Devices ▸ Logged-in user). Empty when no one is
-- logged in, or when the agent is older than 0.11 and does not report it.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS logged_in_user TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS logged_in_user_since TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_agents_logged_in_user ON agents (lower(logged_in_user)) WHERE logged_in_user <> '';

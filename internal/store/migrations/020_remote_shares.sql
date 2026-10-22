-- One-time guest access to a remote session (docs/08-remote-access.md).
--
-- A technician invites someone who has no account: a link carries a 256-bit
-- token, a six-digit code travels by another channel, and the first correct
-- code burns the link. Only hashes are stored. The guest then waits until the
-- technician who sent the invitation accepts them from their own session.
--
-- "Expired" is not a stored status: a share past expires_at is expired whatever
-- the row says, and every query that admits one checks the time itself.
CREATE TABLE IF NOT EXISTS remote_shares (
    id                UUID PRIMARY KEY,
    agent_id          UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    created_by        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    mode              TEXT NOT NULL CHECK (mode IN ('view', 'control')),
    token_hash        TEXT NOT NULL UNIQUE,
    code_hash         TEXT NOT NULL,
    guest_secret_hash TEXT UNIQUE,
    status            TEXT NOT NULL DEFAULT 'open'
                      CHECK (status IN ('open', 'pending', 'accepted', 'rejected', 'revoked', 'ended')),
    failed_attempts   INTEGER NOT NULL DEFAULT 0,
    guest_name        TEXT NOT NULL DEFAULT '',
    guest_ip          TEXT NOT NULL DEFAULT '',
    guest_user_agent  TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at        TIMESTAMPTZ NOT NULL,
    requested_at      TIMESTAMPTZ,
    decided_at        TIMESTAMPTZ,
    ended_at          TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_remote_shares_agent_creator ON remote_shares(agent_id, created_by);

-- Only the built-in administrator role holds it by default; custom roles opt in.
INSERT INTO role_permissions (role_key, permission)
VALUES ('admin', 'devices.remote.share')
ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS oidc_console_handoffs (
    ticket_hash TEXT PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    browser_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oidc_console_handoff_expiry ON oidc_console_handoffs(expires_at);

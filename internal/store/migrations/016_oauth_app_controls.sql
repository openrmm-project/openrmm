-- An administrator's say over which MCP applications may connect.
--
-- Applications register themselves (Dynamic Client Registration), which is how
-- Claude and ChatGPT connect without setup — and also how anyone can register
-- an application named "Claude" that sends users back to their own site.
-- These columns let an administrator see, approve and revoke them:
--
--   status        'approved' may authorize; 'pending' waits for an
--                 administrator when the instance requires approval. Existing
--                 applications stay approved.
--   approved_by   who approved it ('' for one approved by policy at
--                 registration).
--   last_used_at  when it last obtained a token, so a forgotten one is visible.
--
-- Tokens carry client_id without a foreign key; revoking an application
-- deletes its tokens, codes and SSO grants explicitly, and authentication
-- joins the application, so a token of a removed or unapproved application
-- stops working at once.
ALTER TABLE oauth_clients ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'approved'
    CHECK (status IN ('approved', 'pending'));
ALTER TABLE oauth_clients ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_clients ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;
ALTER TABLE oauth_clients ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_oauth_tokens_client ON oauth_tokens (client_id);

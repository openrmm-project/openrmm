-- Authorizing an MCP client through single sign-on.
--
-- The OAuth consent form only took a password, and an account linked to single
-- sign-on has no password path while SSO is on. So the form can now send the
-- user through the identity provider instead, and two things carry that trip:
--
--   oidc_login_states.oauth_request  the MCP authorization request a sign-in
--                                    was started for; NULL for a console
--                                    sign-in, which is every row before this.
--   oauth_sso_grants                 what the callback hands back to the consent
--                                    form: proof that this user just signed in
--                                    through the provider, for this one request.
--
-- A grant is bound to the client, the redirect URI and the PKCE challenge of
-- the request it was minted for, lives five minutes, is stored as a hash, and
-- is deleted on first use, whatever that use is — so it cannot be carried to
-- another client, replayed, or read out of a dump.
ALTER TABLE oidc_login_states ADD COLUMN IF NOT EXISTS oauth_request JSONB;

CREATE TABLE IF NOT EXISTS oauth_sso_grants (
    ticket_hash    TEXT PRIMARY KEY,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id      TEXT NOT NULL,
    redirect_uri   TEXT NOT NULL,
    code_challenge TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_oauth_sso_grants_expiry ON oauth_sso_grants (expires_at);

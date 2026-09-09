-- Sign in through an external identity provider.
--
-- The console has only ever known local users: a row in `users` with a bcrypt
-- hash. That is fine for one operator and wrong for everyone else — an MSP
-- already has an identity provider, and a second directory of technicians is a
-- second place to forget to remove someone who left.
--
-- This adds OpenID Connect as a way to *authenticate*, and deliberately not as
-- a way to authorise. Two separate questions:
--
--   who is this?      answered by the provider, and now by these columns
--   what may they do? answered here, by `role` and the access scope, exactly as
--                     before
--
-- Keeping the second answer local is what makes the feature safe to hand to a
-- hosted control plane later: an identity provider that could also assign roles
-- would be an identity provider that could make itself an admin of every
-- tenant it signs into. It cannot. An account arrives with the role an operator
-- gave it, and a provider that claims otherwise is ignored.

-- Which provider vouched for this user, and their stable identifier there.
--
-- The pair is what identifies an account, never the email address on its own:
-- addresses are reassigned, and `sub` is the one claim OIDC guarantees is
-- stable and unique within an issuer. Matching on email alone means whoever
-- inherits alice@ inherits Alice's console.
ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_issuer  TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN IF NOT EXISTS oidc_subject TEXT NOT NULL DEFAULT '';

-- One console account per identity, and no second account able to claim it.
-- Partial, because local users legitimately share the empty pair.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_oidc_identity
    ON users (oidc_issuer, oidc_subject)
    WHERE oidc_subject <> '';

-- An in-flight sign-in.
--
-- state and nonce both defend the redirect, against different things: state
-- proves the callback belongs to a sign-in this server started (CSRF), nonce
-- proves the ID token was minted for that same sign-in and is not a replay of
-- an older one. The PKCE verifier proves the code is being redeemed by whoever
-- asked for it.
--
-- All three are stored rather than held in memory because a login that breaks
-- whenever the server restarts is a login nobody trusts. They are short-lived
-- and single-use: the row is deleted when the callback consumes it.
CREATE TABLE IF NOT EXISTS oidc_login_states (
    -- SHA-256, like sessions and API tokens: a dump of this table must not hand
    -- anyone a usable in-flight login.
    state_hash    TEXT PRIMARY KEY,
    nonce         TEXT NOT NULL,
    code_verifier TEXT NOT NULL,
    -- Where to send the browser afterwards. Validated against the console's own
    -- origin before use — an open redirect here would turn the login page into
    -- a credential-phishing hop.
    redirect_to   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_oidc_states_expiry ON oidc_login_states (expires_at);

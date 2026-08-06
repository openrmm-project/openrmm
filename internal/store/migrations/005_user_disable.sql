-- Disabling a user, as an alternative to deleting one.
--
-- Deletion cascades: sessions, scopes, API tokens, notification subscriptions
-- and the audit actor reference all go with it. That is the right tool for "this
-- account was created by mistake" and the wrong one for "this person left" —
-- where the account must stop working while the record of what it did survives.
--
-- A timestamp rather than a boolean: knowing *when* access was withdrawn is the
-- question asked during an incident review, and NULL is an unambiguous "active".
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ;

-- Partial index: the lookups that matter ask for the disabled minority.
CREATE INDEX IF NOT EXISTS idx_users_disabled
    ON users (disabled_at) WHERE disabled_at IS NOT NULL;

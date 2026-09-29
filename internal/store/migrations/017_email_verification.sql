-- Confirmed alert recipients.
--
-- An instance may send alert mail through the operator's shared alert service
-- (RMM_ALERTS_SMTP_*) rather than a relay of its own. Recipients are the users'
-- addresses, and a tenant administrator types those in: without a check, they
-- could point alerts — whose text carries hostnames and check names the tenant
-- chose — at anyone, signed with the operator's domain. So, through the shared
-- service, mail goes only to an address its owner confirmed by clicking a link.
--
--   users.email_verified_at   when the current address was confirmed; cleared
--                             whenever the address changes.
--   email_verifications       outstanding confirmation links: stored as a hash,
--                             bound to the address they were sent to, single
--                             use, expiring.
ALTER TABLE users ADD COLUMN IF NOT EXISTS email_verified_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS email_verifications (
    token_hash TEXT        PRIMARY KEY,
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email      TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_email_verifications_user ON email_verifications (user_id, created_at);
-- The throttles count per address, whichever account asked.
CREATE INDEX IF NOT EXISTS idx_email_verifications_email ON email_verifications (email, created_at);

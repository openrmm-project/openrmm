-- An account that must sign in through single sign-on although it has never
-- done so yet: a managed service's owner account, marked by the provisioning
-- that creates it. A linked account (oidc_subject) already had no password
-- path; before its first single sign-on, this one had both a password sign-in
-- and "Forgot password", which stepped around the provider's second factor.
ALTER TABLE users ADD COLUMN IF NOT EXISTS sso_required BOOLEAN NOT NULL DEFAULT false;

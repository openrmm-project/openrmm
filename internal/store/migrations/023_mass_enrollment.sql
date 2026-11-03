-- Install tokens for mass deployment: one token may now enrol up to 10,000
-- computers for up to 90 days, so it is kept like every other credential, as
-- the hex SHA-256 of the token, and it can be listed and revoked.
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS uses_total INTEGER NOT NULL DEFAULT 1;
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS revoked_by TEXT;

-- The tokens already issued keep working: each is replaced by its hash, which
-- is what an enrolment is now matched against.
UPDATE enroll_tokens SET token = encode(sha256(convert_to(token, 'UTF8')), 'hex');

-- The new bounds apply to them too. Until now the API accepted a token that
-- never expired and had no limit on its uses.
UPDATE enroll_tokens SET uses_left = LEAST(uses_left, 10000);
UPDATE enroll_tokens SET uses_total = GREATEST(uses_left, 1);
UPDATE enroll_tokens SET expires_at = now() + interval '90 days'
 WHERE expires_at IS NULL OR expires_at > now() + interval '90 days';

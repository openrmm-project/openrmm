-- Install tokens for mass deployment: one token may now enrol up to 10,000
-- computers for up to 90 days, so it is kept like every other credential, as
-- the hex SHA-256 of the token, and it can be listed and revoked.
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS uses_total INTEGER NOT NULL DEFAULT 1;
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS created_by TEXT NOT NULL DEFAULT '';
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS revoked_at TIMESTAMPTZ;
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS revoked_by TEXT;

-- The token that admitted each device: after a mass-deployment token leaks and
-- is revoked, the devices it enrolled can be found and removed.
ALTER TABLE agents ADD COLUMN IF NOT EXISTS enroll_token_id UUID REFERENCES enroll_tokens(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_agents_enroll_token ON agents(enroll_token_id) WHERE enroll_token_id IS NOT NULL;

-- The tokens already issued keep working: each is replaced by its hash, which
-- is what an enrolment is now matched against.
UPDATE enroll_tokens SET token = encode(sha256(convert_to(token, 'UTF8')), 'hex');

-- The new bounds apply to them too. Until now the API accepted a token that
-- never expired and had no limit on its uses.
UPDATE enroll_tokens SET uses_left = LEAST(uses_left, 10000);
UPDATE enroll_tokens SET uses_total = GREATEST(uses_left, 1);
UPDATE enroll_tokens SET expires_at = now() + interval '90 days'
 WHERE expires_at IS NULL OR expires_at > now() + interval '90 days';

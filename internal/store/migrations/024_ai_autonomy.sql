-- Risk-based autonomy for the AI assistant (docs/23-ai-autonomy.md).
--
-- A library script can be released for the AI to run without approval. The
-- release is bound to the hash of the shell, body and declared parameters at
-- release time, so any edit revokes it. ai_failure_streak counts consecutive
-- failed autonomous runs; enough of them in a row revoke the release.
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS params JSONB NOT NULL DEFAULT '[]';
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS ai_released_by TEXT;
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS ai_released_at TIMESTAMPTZ;
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS ai_released_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE scripts ADD COLUMN IF NOT EXISTS ai_failure_streak INTEGER NOT NULL DEFAULT 0;

-- Why a job ran without approval at the time ('' for every other job), the
-- script it was built from, and the parameters it carried.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS autonomy TEXT NOT NULL DEFAULT ''
    CHECK (autonomy IN ('', 'query', 'preapproved'));
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS script_id UUID REFERENCES scripts(id) ON DELETE SET NULL;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS params JSONB NOT NULL DEFAULT '{}';
-- Rate limits count recent autonomous jobs per creator and per device.
CREATE INDEX IF NOT EXISTS idx_jobs_autonomy ON jobs (created_at) WHERE autonomy <> '';

-- Administrators release scripts; using autonomy is a custom role's choice.
INSERT INTO role_permissions (role_key, permission)
VALUES ('admin', 'ai.autonomy.manage')
ON CONFLICT DO NOTHING;

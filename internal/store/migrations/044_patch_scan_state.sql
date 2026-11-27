-- Each attempt is reserved before delivery. Server timestamps only.
CREATE TABLE agent_patch_scan_state (
 agent_id UUID PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 request_id TEXT NOT NULL DEFAULT '',
 refresh_metadata BOOLEAN NOT NULL DEFAULT FALSE,
 status TEXT NOT NULL DEFAULT 'unknown',
 last_attempt TIMESTAMPTZ,
 deadline TIMESTAMPTZ,
 last_success TIMESTAMPTZ,
 last_metadata_refresh TIMESTAMPTZ,
 scan_mode TEXT NOT NULL DEFAULT 'legacy',
 error_code TEXT NOT NULL DEFAULT '',
 consecutive_failures INTEGER NOT NULL DEFAULT 0
);
INSERT INTO agent_patch_scan_state (agent_id, last_success)
SELECT id, last_patch_scan FROM agents WHERE last_patch_scan IS NOT NULL;

-- An inventory observation predating an install claim must not reopen it.
ALTER TABLE agent_patches ADD COLUMN install_claimed_at TIMESTAMPTZ;
ALTER TABLE agent_patches ADD COLUMN install_result_at TIMESTAMPTZ;

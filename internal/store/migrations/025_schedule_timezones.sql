-- Scheduled tasks and patch maintenance windows were read in UTC (tasks) or in
-- the server process's own zone (patch windows: UTC in the published images),
-- while the console showed their times with no zone at all. Each now carries
-- the IANA zone its times of day are in. Existing rows keep UTC, so nothing
-- moves by itself; the console shows the zone so it can be corrected.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE patch_policies ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'UTC';

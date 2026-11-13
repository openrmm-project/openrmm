-- The last user seen in a device's interactive session, kept after they log
-- off (Reports ▸ Hardware & System ▸ Last logged-in user), and when the agent
-- last reported them. logged_in_user (034) is who is logged in now.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS last_logged_in_user TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_logged_in_user_at TIMESTAMPTZ;
UPDATE agents
   SET last_logged_in_user = logged_in_user, last_logged_in_user_at = COALESCE(logged_in_user_since, last_seen)
 WHERE logged_in_user <> '' AND last_logged_in_user = '';

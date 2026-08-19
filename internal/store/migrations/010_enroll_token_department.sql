-- Let an enrollment token place an agent in a department, not only a site.
--
-- The token has always carried where the agent lands: enroll_tokens.site_id is
-- read at registration and written to the agent. Departments were never part of
-- that, so an agent enrolled into the site and someone moved it afterwards, by
-- hand, for every machine.
--
-- The name is the whole change. The installers are untouched: the command does
-- not describe the placement, the token does — install.sh already accepts and
-- discards a --site argument for that reason.
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS group_id UUID
    REFERENCES device_groups(id) ON DELETE SET NULL;

-- A department belongs to exactly one site, and an agent's department must be
-- one of its own site's — the rule SetAgentSite already enforces when a device
-- moves. A token that names both must name a consistent pair, or enrolment would
-- create the state that rule exists to prevent.
CREATE OR REPLACE FUNCTION enroll_token_group_matches_site() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.group_id IS NOT NULL THEN
        IF NEW.site_id IS NULL THEN
            RAISE EXCEPTION 'enroll token names a department without a site';
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM device_groups g
             WHERE g.id = NEW.group_id AND g.site_id = NEW.site_id
        ) THEN
            RAISE EXCEPTION 'enroll token department % does not belong to site %',
                NEW.group_id, NEW.site_id;
        END IF;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_enroll_token_group_matches_site ON enroll_tokens;
CREATE TRIGGER trg_enroll_token_group_matches_site
    BEFORE INSERT OR UPDATE ON enroll_tokens
    FOR EACH ROW EXECUTE FUNCTION enroll_token_group_matches_site();

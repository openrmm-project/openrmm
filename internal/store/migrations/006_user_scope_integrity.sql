-- Referential integrity for user_scopes.
--
-- scope_id is polymorphic — it points at clients, sites, device_groups or agents
-- depending on scope_type — so no single foreign key can cover it, and it has had
-- none. Deleting a device therefore left the scope rows naming it behind, and a
-- dangling row is not inert:
--
--   * the account it restricts can reach nothing through it;
--   * the console's scope editor counts it ("1 selected") but cannot draw it,
--     because there is no device to draw, so the tree looks empty;
--   * and PUT /api/users/{id}/scopes validates every entry it is sent. The editor
--     loads the dangling row and sends it straight back, so *every* save for that
--     user fails with "scope not found" — including the save that would have
--     removed it. The account cannot be repaired from the console at all.
--
-- Purge what is already stranded, then stop it recurring.
--
-- An empty user_scopes set means unrestricted, so removing someone's last row is
-- a promotion to full fleet access — the opposite of what deleting the thing they
-- were confined to should mean. Accounts left with nothing are disabled instead,
-- which is reversible and puts a person in front of the decision. Recorded first,
-- because the check has to run after the delete: a single statement would still
-- see the deleted rows in its own snapshot.
CREATE TEMPORARY TABLE stranded_scope_users ON COMMIT DROP AS
SELECT DISTINCT user_id FROM user_scopes us WHERE
       (us.scope_type = 'agent'  AND NOT EXISTS (SELECT 1 FROM agents        a WHERE a.id = us.scope_id))
    OR (us.scope_type = 'site'   AND NOT EXISTS (SELECT 1 FROM sites         s WHERE s.id = us.scope_id))
    OR (us.scope_type = 'group'  AND NOT EXISTS (SELECT 1 FROM device_groups g WHERE g.id = us.scope_id))
    OR (us.scope_type = 'client' AND NOT EXISTS (SELECT 1 FROM clients       c WHERE c.id = us.scope_id));

DELETE FROM user_scopes us WHERE
       (us.scope_type = 'agent'  AND NOT EXISTS (SELECT 1 FROM agents        a WHERE a.id = us.scope_id))
    OR (us.scope_type = 'site'   AND NOT EXISTS (SELECT 1 FROM sites         s WHERE s.id = us.scope_id))
    OR (us.scope_type = 'group'  AND NOT EXISTS (SELECT 1 FROM device_groups g WHERE g.id = us.scope_id))
    OR (us.scope_type = 'client' AND NOT EXISTS (SELECT 1 FROM clients       c WHERE c.id = us.scope_id));

UPDATE users u SET disabled_at = COALESCE(u.disabled_at, now())
 WHERE u.id IN (SELECT user_id FROM stranded_scope_users)
   AND NOT EXISTS (SELECT 1 FROM user_scopes s WHERE s.user_id = u.id);

-- A trigger rather than cleanup in the delete handlers: deleting a client
-- cascades to its sites and their device groups, and those cascades never reach
-- application code. A trigger fires for every row actually removed, however it
-- was removed.
--
-- The "left with nothing" rule lives here rather than on user_scopes itself, so
-- that it only ever applies to rows removed because their target went away.
-- Setting an account to full access deliberately also empties the table for that
-- user, and must not disable them.
CREATE OR REPLACE FUNCTION prune_user_scopes() RETURNS TRIGGER AS $$
DECLARE
    stranded UUID;
BEGIN
    FOR stranded IN
        DELETE FROM user_scopes WHERE scope_type = TG_ARGV[0] AND scope_id = OLD.id
        RETURNING user_id
    LOOP
        UPDATE users u SET disabled_at = COALESCE(u.disabled_at, now())
         WHERE u.id = stranded
           AND NOT EXISTS (SELECT 1 FROM user_scopes s WHERE s.user_id = stranded);
    END LOOP;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_prune_user_scopes_agent ON agents;
CREATE TRIGGER trg_prune_user_scopes_agent
    AFTER DELETE ON agents
    FOR EACH ROW EXECUTE FUNCTION prune_user_scopes('agent');

DROP TRIGGER IF EXISTS trg_prune_user_scopes_site ON sites;
CREATE TRIGGER trg_prune_user_scopes_site
    AFTER DELETE ON sites
    FOR EACH ROW EXECUTE FUNCTION prune_user_scopes('site');

DROP TRIGGER IF EXISTS trg_prune_user_scopes_group ON device_groups;
CREATE TRIGGER trg_prune_user_scopes_group
    AFTER DELETE ON device_groups
    FOR EACH ROW EXECUTE FUNCTION prune_user_scopes('group');

DROP TRIGGER IF EXISTS trg_prune_user_scopes_client ON clients;
CREATE TRIGGER trg_prune_user_scopes_client
    AFTER DELETE ON clients
    FOR EACH ROW EXECUTE FUNCTION prune_user_scopes('client');

-- Making the audit trail hard to rewrite.
--
-- Three layers, each covering what the one before it cannot. Stating that
-- plainly matters, because the obvious single measure — a trigger — is much
-- weaker than it looks: the application connects as the table's owner, and an
-- owner can `ALTER TABLE ... DISABLE TRIGGER` and then delete whatever it likes.
--
--   1. Triggers        stop accidents and application bugs.
--   2. Privileges      stop a compromised application, because the credentials
--                      that could delete are not the ones the server holds.
--   3. A hash chain    makes a rewrite by someone with full database access
--                      *detectable*, which is the most any in-database measure
--                      can offer against an attacker who owns the database.
--
-- Layer 3 without an external anchor cannot prevent a determined rewrite of the
-- whole chain. It is here as a complement to layer 2, not as a substitute: with
-- the application unable to delete, a rewrite requires the separate credentials,
-- and the chain means even that leaves evidence.

-- ---------------------------------------------------------------- the chain

ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS prev_hash  TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN IF NOT EXISTS entry_hash TEXT NOT NULL DEFAULT '';

-- Only the human record is chained, and this is the point where the chain and
-- retention have to agree.
--
-- Retention deletes machine-written rows (job.automated, job.result) once they
-- age out. If those rows were in the chain, every purge would sever it and the
-- verification would report tampering that never happened — an alarm that fires
-- on routine housekeeping is an alarm nobody keeps.
--
-- Chaining exactly the rows that are never deleted resolves it, and the split
-- is the same one retention already draws: the human record is the thing worth
-- protecting; the machine entries are telemetry sharing a table.
CREATE OR REPLACE FUNCTION audit_is_machine_written(action TEXT) RETURNS BOOLEAN AS $$
    SELECT action IN ('job.automated', 'job.result');
$$ LANGUAGE sql IMMUTABLE;

CREATE OR REPLACE FUNCTION audit_chain_link() RETURNS TRIGGER AS $$
DECLARE
    previous TEXT;
BEGIN
    IF audit_is_machine_written(NEW.action) THEN
        NEW.prev_hash  := '';
        NEW.entry_hash := '';
        RETURN NEW;
    END IF;

    -- Serialises chained inserts for the length of the transaction. Two
    -- concurrent inserts would otherwise both read the same predecessor under
    -- READ COMMITTED and fork the chain into two branches with one parent.
    --
    -- Affordable because this covers human actions only: an operator approving
    -- a job, changing a scope, editing settings. The machine entries — the ones
    -- that arrive thousands per day — skip this path entirely.
    PERFORM pg_advisory_xact_lock(hashtext('audit_log_chain'));

    SELECT entry_hash INTO previous
    FROM audit_log
    WHERE entry_hash <> ''
    ORDER BY created_at DESC, id DESC
    LIMIT 1;

    NEW.prev_hash := COALESCE(previous, '');
    -- Every field a reader would rely on goes into the digest. Leaving one out
    -- would let it be edited without breaking the chain, which is worse than no
    -- chain at all: it would carry an assurance it does not provide.
    NEW.entry_hash := encode(sha256(convert_to(
        NEW.prev_hash || '|' || NEW.id::text || '|' || NEW.actor || '|' ||
        NEW.action || '|' || NEW.target || '|' || NEW.detail::text || '|' ||
        to_char(NEW.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'),
        'UTF8')), 'hex');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_audit_chain ON audit_log;
CREATE TRIGGER trg_audit_chain BEFORE INSERT ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_chain_link();

-- ---------------------------------------------------------------- layer 1

-- Refuses UPDATE always, and DELETE unless the purge explicitly asked for it.
--
-- The escape hatch is a transaction-local setting rather than a role check,
-- because the purge runs as the same role as everything else. It is set inside
-- purge_machine_audit() and dies with the transaction, so it cannot be left on.
CREATE OR REPLACE FUNCTION audit_append_only() RETURNS TRIGGER AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'audit_log is append-only: entries cannot be modified'
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF current_setting('openrmm.audit_purge', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'audit_log is append-only: delete only through purge_machine_audit()'
            USING ERRCODE = 'restrict_violation';
    END IF;

    -- Even inside the purge, only the machine-written rows may go. A purge that
    -- could reach the human record would make the whole arrangement pointless.
    IF NOT audit_is_machine_written(OLD.action) THEN
        RAISE EXCEPTION 'audit_log: % is part of the human record and is never purged', OLD.action
            USING ERRCODE = 'restrict_violation';
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_audit_append_only ON audit_log;
CREATE TRIGGER trg_audit_append_only BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_append_only();

-- ---------------------------------------------------------------- layer 2

-- The one narrow thing the application is allowed to delete.
--
-- SECURITY DEFINER so it runs with the owner's rights: once the application
-- connects as a role without DELETE on audit_log (see below), this function is
-- its only path, and the function can only ever remove aged machine entries.
CREATE OR REPLACE FUNCTION purge_machine_audit(cutoff TIMESTAMPTZ)
RETURNS BIGINT AS $$
DECLARE
    removed BIGINT;
BEGIN
    PERFORM set_config('openrmm.audit_purge', 'on', true);  -- true = transaction-local
    DELETE FROM audit_log
    WHERE audit_is_machine_written(action) AND created_at < cutoff;
    GET DIAGNOSTICS removed = ROW_COUNT;
    RETURN removed;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER;

-- Walks the chain and returns the first entry whose hash does not follow from
-- its predecessor, or no rows when the chain is intact. Deletions show up as a
-- broken link at the row that followed the missing one.
CREATE OR REPLACE FUNCTION verify_audit_chain()
RETURNS TABLE (broken_id UUID, broken_at TIMESTAMPTZ, reason TEXT) AS $$
DECLARE
    row_    RECORD;
    expected TEXT;
    prior    TEXT := '';
BEGIN
    FOR row_ IN
        SELECT * FROM audit_log WHERE entry_hash <> '' ORDER BY created_at, id
    LOOP
        IF row_.prev_hash IS DISTINCT FROM prior THEN
            broken_id := row_.id; broken_at := row_.created_at;
            reason := 'previous hash does not match the entry before it';
            RETURN NEXT; RETURN;
        END IF;
        expected := encode(sha256(convert_to(
            row_.prev_hash || '|' || row_.id::text || '|' || row_.actor || '|' ||
            row_.action || '|' || row_.target || '|' || row_.detail::text || '|' ||
            to_char(row_.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US'),
            'UTF8')), 'hex');
        IF expected IS DISTINCT FROM row_.entry_hash THEN
            broken_id := row_.id; broken_at := row_.created_at;
            reason := 'entry contents do not match its recorded hash';
            RETURN NEXT; RETURN;
        END IF;
        prior := row_.entry_hash;
    END LOOP;
END;
$$ LANGUAGE plpgsql;

-- The role the server should connect as.
--
-- Created without LOGIN so this migration carries no password. Grant it one and
-- point RMM_DATABASE_URL at it to actually get layer 2 — until then the triggers
-- and the chain are in force, but the application still holds credentials that
-- could disable them. See docs/07-guardrails-security.md.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'openrmm_app') THEN
        CREATE ROLE openrmm_app NOLOGIN;
    END IF;
END $$;

-- Against current_schema(), not a hardcoded "public". A deployment that puts
-- the tables in another schema would otherwise apply none of these grants and
-- get no layer 2 at all — silently, since nothing would fail until the day the
-- role was actually used.
DO $$
DECLARE
    target TEXT := quote_ident(current_schema());
BEGIN
    EXECUTE format('GRANT USAGE ON SCHEMA %s TO openrmm_app', target);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO openrmm_app', target);
    EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %s TO openrmm_app', target);
    EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO openrmm_app', target);
END $$;

-- ...except on the audit log, where it may only append and read. This is the
-- line that makes a compromised application unable to erase what it did.
REVOKE UPDATE, DELETE ON audit_log FROM openrmm_app;
GRANT EXECUTE ON FUNCTION purge_machine_audit(TIMESTAMPTZ) TO openrmm_app;
GRANT EXECUTE ON FUNCTION verify_audit_chain() TO openrmm_app;

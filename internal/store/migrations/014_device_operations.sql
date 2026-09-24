-- Moving or deleting many devices at once, including when an AI asks for it.
--
-- The AI's standing guarantee is that it cannot act on its own: a script it
-- writes is a job awaiting approval, a task or check it writes is created
-- disabled. Moving a device between companies, or deleting it, is neither — it
-- is a change the server makes, with no job to hold back. So it gets a row of
-- its own that does nothing until a person approves it:
--
--   proposed_by  who asked ('ai:<user>', 'mcp:<principal>'). Permanent.
--   decided_by   who approved or rejected it ('' = still waiting).
--
-- On approval the server carries it out with the APPROVER's permissions and
-- access scope, checked again at that moment, never the proposer's. A proposal
-- is therefore a request, not a delegation: an AI working for a technician
-- confined to one company cannot get devices moved out of it, because the
-- person who approves is held to their own scope, and an approver without the
-- right to delete devices cannot release a deletion.
CREATE TABLE IF NOT EXISTS device_operations (
    id          UUID PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('move', 'delete')),
    agent_ids   UUID[] NOT NULL CHECK (cardinality(agent_ids) BETWEEN 1 AND 500),
    -- The destination of a move. A proposal whose destination is deleted
    -- before anyone reviews it has nothing left to approve, so it goes with it.
    site_id     UUID REFERENCES sites(id) ON DELETE CASCADE,
    group_id    UUID REFERENCES device_groups(id) ON DELETE CASCADE,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'awaiting_approval'
                CHECK (status IN ('awaiting_approval', 'approved', 'completed', 'failed', 'rejected')),
    proposed_by TEXT NOT NULL CHECK (proposed_by <> ''),
    decided_by  TEXT NOT NULL DEFAULT '',
    result      JSONB NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at  TIMESTAMPTZ,
    CHECK (kind <> 'move' OR site_id IS NOT NULL),
    CHECK (kind <> 'delete' OR (site_id IS NULL AND group_id IS NULL)),
    -- Anything past review names who reviewed it.
    CHECK (status = 'awaiting_approval' OR decided_by <> '')
);

CREATE INDEX IF NOT EXISTS idx_device_operations_awaiting
    ON device_operations (created_at DESC) WHERE status = 'awaiting_approval';
CREATE INDEX IF NOT EXISTS idx_device_operations_created
    ON device_operations (created_at DESC);

-- A policy gains an on/off switch (docs/10). Switched off, it applies nothing:
-- none of its tasks, checks or server alert rules reach a device, whatever
-- each one's own flag says. AI Chat and MCP propose a policy switched off,
-- complete with its scopes, tasks and checks, and a person switches it on
-- after reviewing all of it in one screen.
ALTER TABLE policies
    ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;

-- Until now a policy had no review of its own: a person reviewed what it does
-- by assigning it (no AI tool assigns) and by enabling each task and check.
-- An AI policy someone assigned was reviewed in practice, so it keeps applying.
UPDATE policies p SET approved_by = 'migration:032', approved_at = now()
 WHERE p.proposed_by <> '' AND p.approved_by = ''
   AND EXISTS (SELECT 1 FROM policy_assignments pa WHERE pa.policy_id = p.id);
-- An unassigned AI policy applied to nothing; it now waits switched off.
UPDATE policies SET enabled = FALSE WHERE proposed_by <> '' AND approved_by = '';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'policies_proposal_needs_review') THEN
        ALTER TABLE policies ADD CONSTRAINT policies_proposal_needs_review
            CHECK (proposed_by = '' OR approved_by <> '' OR NOT enabled);
    END IF;
END $$;

-- Patch policies already had the switch. They gain provenance so AI Chat and
-- MCP can propose one switched off, under the same rule: the database refuses
-- to switch on an AI proposal nobody reviewed.
ALTER TABLE patch_policies
    ADD COLUMN IF NOT EXISTS proposed_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'patch_policies_proposal_needs_review') THEN
        ALTER TABLE patch_policies ADD CONSTRAINT patch_policies_proposal_needs_review
            CHECK (proposed_by = '' OR approved_by <> '' OR NOT enabled);
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_patch_policies_awaiting_review ON patch_policies (created_at DESC)
    WHERE proposed_by <> '' AND approved_by = '';

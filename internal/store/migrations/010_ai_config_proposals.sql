-- AI-proposed configuration.
--
-- The AI's guarantee has always been that it cannot execute: propose_script
-- creates a job in 'awaiting_approval' and a person releases it. Configuration
-- would have punched a hole straight through that. A task is a script with a
-- schedule, and the scheduler runs one through jobs.RunAutomated — pre-approved,
-- dispatched immediately, forever. An AI able to create a task would have been
-- strictly more powerful than one able to propose a script.
--
-- So configuration gets the treatment jobs already have, reusing the kill switch
-- that already exists: `enabled`. The scheduler selects WHERE enabled, so a
-- proposal is inert until a person turns it on, with no scheduler change.
--
-- Two columns, because they answer two different questions that must not be
-- collapsed into one:
--
--   proposed_by  who authored this ('' = a person, in the console). PERMANENT
--                provenance: it survives acceptance, so months later the row
--                still says an AI wrote it.
--   approved_by  who reviewed and released it ('' = nobody yet).
--
-- "Needs review" is therefore proposed_by <> '' AND approved_by = '', and
-- accepting a proposal fills approved_by rather than erasing its origin.
ALTER TABLE tasks    ADD COLUMN IF NOT EXISTS proposed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks    ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE checks   ADD COLUMN IF NOT EXISTS proposed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE checks   ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS proposed_by TEXT NOT NULL DEFAULT '';
ALTER TABLE policies ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '';

-- An unreviewed proposal that is enabled means something reached the scheduler
-- without a person seeing it. Enforced in the database because the AI tools and
-- the REST handlers are separate code paths into the same rows, and a guard
-- written in only one of them is precisely the bug this column exists to stop.
ALTER TABLE tasks  DROP CONSTRAINT IF EXISTS tasks_unreviewed_proposal_disabled;
ALTER TABLE tasks  ADD  CONSTRAINT tasks_unreviewed_proposal_disabled
    CHECK (proposed_by = '' OR approved_by <> '' OR NOT enabled);
ALTER TABLE checks DROP CONSTRAINT IF EXISTS checks_unreviewed_proposal_disabled;
ALTER TABLE checks ADD  CONSTRAINT checks_unreviewed_proposal_disabled
    CHECK (proposed_by = '' OR approved_by <> '' OR NOT enabled);

-- Reviewing means finding them. Partial so the index stays small: it holds only
-- what is still waiting for a person.
CREATE INDEX IF NOT EXISTS idx_tasks_awaiting_review    ON tasks (created_at DESC)
    WHERE proposed_by <> '' AND approved_by = '';
CREATE INDEX IF NOT EXISTS idx_checks_awaiting_review   ON checks (created_at DESC)
    WHERE proposed_by <> '' AND approved_by = '';
CREATE INDEX IF NOT EXISTS idx_policies_awaiting_review ON policies (created_at DESC)
    WHERE proposed_by <> '' AND approved_by = '';

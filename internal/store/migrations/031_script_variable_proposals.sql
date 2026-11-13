-- AI Chat and MCP may propose a script variable definition (docs/25). A
-- proposal carries who proposed it and stays inert until a person approves
-- it: no value can be set, no script can bind it, and a run never resolves it.
ALTER TABLE script_variables
    ADD COLUMN IF NOT EXISTS proposed_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;

-- The same for library scripts: AI Chat and MCP propose one, a person approves
-- it before it can run, be scheduled or be released for AI autonomy.
ALTER TABLE scripts
    ADD COLUMN IF NOT EXISTS proposed_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS approved_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS approved_at TIMESTAMPTZ;

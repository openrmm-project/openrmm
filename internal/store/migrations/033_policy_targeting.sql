-- Policies and patch policies can target devices by OS and role, and leave out
-- chosen devices (docs/10, RFC #258). Every existing assignment keeps empty
-- filters (any OS, any role) and no policy excludes anything, so what applies
-- today keeps applying.

-- A device's role: detected from its inventory, or set by an administrator.
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS os_role TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS role_override TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD CONSTRAINT agents_role_valid
    CHECK (os_role IN ('', 'workstation', 'server') AND role_override IN ('', 'workstation', 'server'));

-- Detect it from the inventory already collected: Windows names its product
-- type (newer agents) or its edition; a Mac is a workstation; Linux has none.
UPDATE agents a SET os_role = CASE
    WHEN a.os = 'darwin' THEN 'workstation'
    WHEN a.os = 'windows' THEN COALESCE((
        SELECT CASE
            WHEN lower(COALESCE(i.data->>'platform_family', '')) LIKE 'server%' THEN 'server'
            WHEN lower(COALESCE(i.data->>'platform_family', '')) LIKE '%workstation%' THEN 'workstation'
            WHEN lower(COALESCE(i.data->>'platform', '')) LIKE '%windows server%'
              OR lower(COALESCE(i.data->>'platform', '')) LIKE '%server 20%' THEN 'server'
            WHEN btrim(COALESCE(i.data->>'platform', '')) <> '' THEN 'workstation'
            ELSE '' END
        FROM inventory i WHERE i.agent_id = a.id), '')
    ELSE '' END;

-- Assignment filters, and the global scope (every device, through the filters).
ALTER TABLE policy_assignments
    ADD COLUMN IF NOT EXISTS os TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT '';
ALTER TABLE policy_assignments ADD CONSTRAINT policy_assignments_target_valid
    CHECK (os IN ('', 'windows', 'macos', 'linux') AND role IN ('', 'workstation', 'server'));
ALTER TABLE patch_policy_assignments
    ADD COLUMN IF NOT EXISTS os TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT '';
ALTER TABLE patch_policy_assignments ADD CONSTRAINT patch_policy_assignments_target_valid
    CHECK (os IN ('', 'windows', 'macos', 'linux') AND role IN ('', 'workstation', 'server'));

-- Exceptions: devices a policy leaves out whichever assignment reaches them.
CREATE TABLE IF NOT EXISTS policy_exclusions (
    policy_id  UUID NOT NULL REFERENCES policies(id) ON DELETE CASCADE,
    agent_id   UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (policy_id, agent_id)
);
CREATE INDEX IF NOT EXISTS idx_policy_exclusions_agent ON policy_exclusions (agent_id);
CREATE TABLE IF NOT EXISTS patch_policy_exclusions (
    policy_id  UUID NOT NULL REFERENCES patch_policies(id) ON DELETE CASCADE,
    agent_id   UUID NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (policy_id, agent_id)
);
CREATE INDEX IF NOT EXISTS idx_patch_policy_exclusions_agent ON patch_policy_exclusions (agent_id);

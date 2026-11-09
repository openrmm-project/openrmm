-- The built-in administrator role has full access, AI autonomy included.
-- Migration 025 gave it only ai.autonomy.manage (releasing scripts); this grants
-- the four switches as well, for the built-in chat and for MCP. Technician and
-- read-only still hold none of them, so for anyone else a custom role opts in.
-- Settings ▸ AI ▸ pause still stops autonomy for every role at once.
INSERT INTO role_permissions (role_key, permission)
SELECT 'admin', permission FROM (VALUES
    ('ai.autonomy.query'), ('ai.autonomy.run'),
    ('mcp.autonomy.query'), ('mcp.autonomy.run')
) AS p(permission)
ON CONFLICT DO NOTHING;

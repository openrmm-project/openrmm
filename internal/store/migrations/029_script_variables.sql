-- Script variables (a name a library script may read at runtime).
--
-- The value is not part of the script. One script is reused across clients,
-- and each client, site, department or device can hold its own value; the
-- most specific scope wins. secret values are encrypted with the instance
-- settings key and are never written back to an API response, an audit row,
-- or a job's stored output.
--
-- scripts.variables is the list of names that script is allowed to receive.
-- A script that does not list a name does not receive it, so a technician
-- who can run or edit an unrelated script cannot read a customer's key.
-- jobs.variables snapshots that list at creation, so a later edit does not
-- change a job already approved. jobs.secret_redactions is the ciphertext
-- of the secret values actually sent, used only to redact the result.
CREATE TABLE IF NOT EXISTS script_variables (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    secret      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT script_variables_name_valid CHECK (
        name ~ '^[A-Z][A-Z0-9_]{0,40}$' AND name !~ '^(RMM_|OPENRMM_)'
    )
);

CREATE TABLE IF NOT EXISTS script_variable_values (
    variable_id UUID NOT NULL REFERENCES script_variables(id) ON DELETE CASCADE,
    scope_type  TEXT NOT NULL,
    scope_id    TEXT NOT NULL DEFAULT '',
    value       TEXT NOT NULL,
    PRIMARY KEY (variable_id, scope_type, scope_id),
    CONSTRAINT script_variable_values_scope_valid CHECK (
        scope_type IN ('global', 'client', 'site', 'group', 'agent')
        AND ((scope_type = 'global' AND scope_id = '') OR (scope_type <> 'global' AND scope_id <> ''))
    )
);

ALTER TABLE scripts ADD COLUMN IF NOT EXISTS variables JSONB NOT NULL DEFAULT '[]';

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS variables JSONB NOT NULL DEFAULT '[]';
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS secret_redactions TEXT NOT NULL DEFAULT '';

INSERT INTO role_permissions (role_key, permission)
VALUES ('admin', 'scripts.variables')
ON CONFLICT DO NOTHING;

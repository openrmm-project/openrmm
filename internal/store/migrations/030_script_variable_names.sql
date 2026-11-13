-- Preserve the user-chosen case and remove the business name length limit.
ALTER TABLE script_variables DROP CONSTRAINT script_variables_name_valid;
ALTER TABLE script_variables ADD CONSTRAINT script_variables_name_valid CHECK (
    name ~ '^[A-Za-z_][A-Za-z0-9_]*$' AND upper(name) !~ '^(RMM_|OPENRMM_)'
);

-- Windows environment names are case-insensitive. Keep one spelling per name,
-- preserving it verbatim for Unix. Hash the ASCII-folded identifier so long
-- names do not hit PostgreSQL's B-tree key-size limit. SHA-256 is built in.
-- translate makes ASCII folding independent of database locale.
ALTER TABLE script_variables DROP CONSTRAINT script_variables_name_key;
CREATE UNIQUE INDEX script_variables_name_casefold_unique ON script_variables (
    sha256(translate(name, 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz')::bytea)
);

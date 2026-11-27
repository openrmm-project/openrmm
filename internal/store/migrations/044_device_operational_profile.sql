CREATE TABLE device_operations_profiles (
    agent_id UUID PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL DEFAULT '' CHECK (char_length(purpose) <= 2000),
    applications TEXT NOT NULL DEFAULT '' CHECK (char_length(applications) <= 8000),
    dependencies TEXT NOT NULL DEFAULT '' CHECK (char_length(dependencies) <= 8000),
    criticality TEXT NOT NULL DEFAULT 'unknown' CHECK (criticality IN ('unknown','low','normal','high','critical')),
    maintenance_window TEXT NOT NULL DEFAULT '' CHECK (char_length(maintenance_window) <= 1000),
    maintenance_timezone TEXT NOT NULL DEFAULT '' CHECK (char_length(maintenance_timezone) <= 100),
    backup_notes TEXT NOT NULL DEFAULT '' CHECK (char_length(backup_notes) <= 8000),
    backup_verified_on DATE,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_by TEXT NOT NULL
);

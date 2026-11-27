CREATE TABLE docker_inventory (
 agent_id UUID PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
 status TEXT NOT NULL CHECK (status IN ('available','not_installed','unavailable','unsupported')),
 error_code TEXT NOT NULL DEFAULT '',
 containers JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(containers)='array' AND jsonb_array_length(containers)<=500),
 last_attempt_at TIMESTAMPTZ NOT NULL,
 last_success_at TIMESTAMPTZ
);

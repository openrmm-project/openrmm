CREATE TABLE notification_routes (
    id UUID PRIMARY KEY,
    client_id UUID UNIQUE REFERENCES clients(id) ON DELETE CASCADE,
    site_id UUID UNIQUE REFERENCES sites(id) ON DELETE CASCADE,
    mode TEXT NOT NULL CHECK (mode IN ('inherit','selected')),
    version BIGINT NOT NULL CHECK (version > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_by TEXT NOT NULL,
    CHECK ((client_id IS NOT NULL)::int + (site_id IS NOT NULL)::int = 1)
);
CREATE TABLE notification_route_users (
    route_id UUID NOT NULL REFERENCES notification_routes(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (route_id,user_id)
);
ALTER TABLE alerts ADD COLUMN notification_route_snapshot JSONB NOT NULL DEFAULT '{"mode":"automatic"}'
    CHECK (jsonb_typeof(notification_route_snapshot) = 'object');

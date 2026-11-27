-- Existing rows retain NULL: their immutable rule provenance is not recoverable
-- from the current (mutable) alert rule. Delivery handles these conservatively.
ALTER TABLE notifications ADD COLUMN delivery_context JSONB;
ALTER TABLE notifications ADD CONSTRAINT notifications_delivery_context_object
    CHECK (delivery_context IS NULL OR jsonb_typeof(delivery_context) = 'object');

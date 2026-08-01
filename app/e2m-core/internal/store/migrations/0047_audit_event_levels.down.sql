ALTER TABLE notification_routes
    DROP CONSTRAINT IF EXISTS notification_routes_min_event_level_check,
    DROP COLUMN IF EXISTS min_event_level;

ALTER TABLE operation_audits
    DROP CONSTRAINT IF EXISTS operation_audits_event_level_check,
    DROP COLUMN IF EXISTS details,
    DROP COLUMN IF EXISTS event_level;
-- Target normalization and secret scrubbing performed by the up migration are
-- intentionally irreversible. Rolling back removes only the write constraint.
BEGIN;

ALTER TABLE notification_routes
    DROP CONSTRAINT IF EXISTS notification_route_target_ref_check;

COMMIT;

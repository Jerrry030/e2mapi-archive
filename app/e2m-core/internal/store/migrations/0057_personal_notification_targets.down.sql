-- Quarantine personal routes before restoring the system-only constraint so a
-- rollback remains executable without exposing or deleting their history.
BEGIN;

ALTER TABLE notification_routes
    DROP CONSTRAINT IF EXISTS notification_route_target_ref_check;

UPDATE notification_routes
SET target_ref = '', enabled = false
WHERE (channel = 'feishu' AND target_ref = 'credential_ref:user/' || user_id::text || '/notification/personal-feishu')
   OR (channel = 'qq' AND target_ref = 'credential_ref:user/' || user_id::text || '/notification/personal-qq');

ALTER TABLE notification_routes
    ADD CONSTRAINT notification_route_target_ref_check CHECK (
        channel IS NOT NULL AND target_ref IS NOT NULL AND enabled IS NOT NULL AND (
            (channel = 'feishu' AND target_ref = 'system:feishu') OR
            (channel = 'qq' AND target_ref = 'system:qq') OR
            (channel = 'webhook' AND target_ref ~ (
                '^credential_ref:user/' || user_id::text || '/notification/[A-Za-z0-9._-]+$'
            )) OR
            (enabled = false AND target_ref = '')
        )
    );

COMMIT;

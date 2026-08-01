-- Canonicalize notification targets before enforcing their ownership boundary.
-- Invalid webhook values may contain legacy plaintext URLs or credentials, so
-- they are irreversibly scrubbed and the route is quarantined instead of being
-- deleted. Unknown channels are quarantined as well so their history survives.
BEGIN;

LOCK TABLE notification_routes IN ACCESS EXCLUSIVE MODE;

ALTER TABLE notification_routes
    DROP CONSTRAINT IF EXISTS notification_route_target_ref_check;

WITH normalized AS MATERIALIZED (
    SELECT id,
           btrim(COALESCE(target_ref, ''), E' \t\n\r\f\v') AS target_ref
    FROM notification_routes
)
UPDATE notification_routes AS route
SET target_ref = CASE
        WHEN route.channel = 'feishu' THEN 'system:feishu'
        WHEN route.channel = 'qq' THEN 'system:qq'
        WHEN route.channel = 'webhook'
             AND normalized.target_ref ~ (
                 '^credential_ref:user/' || route.user_id::text ||
                 '/notification/[A-Za-z0-9._-]+$'
             )
            THEN normalized.target_ref
        ELSE ''
    END,
    enabled = CASE
        WHEN route.channel IN ('feishu', 'qq') THEN COALESCE(route.enabled, false)
        WHEN route.channel = 'webhook'
             AND normalized.target_ref ~ (
                 '^credential_ref:user/' || route.user_id::text ||
                 '/notification/[A-Za-z0-9._-]+$'
             )
            THEN COALESCE(route.enabled, false)
        ELSE false
    END
FROM normalized
WHERE normalized.id = route.id;

ALTER TABLE notification_routes
    ADD CONSTRAINT notification_route_target_ref_check CHECK (
        channel IS NOT NULL AND
        target_ref IS NOT NULL AND
        enabled IS NOT NULL AND (
            (channel = 'feishu' AND target_ref = 'system:feishu') OR
            (channel = 'qq' AND target_ref = 'system:qq') OR
            (channel = 'webhook' AND target_ref ~ (
                '^credential_ref:user/' || user_id::text ||
                '/notification/[A-Za-z0-9._-]+$'
            )) OR
            (enabled = false AND target_ref = '')
        )
    );

COMMIT;

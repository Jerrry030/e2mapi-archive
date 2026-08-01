-- Feishu/QQ routes may select either the platform sender or the owning user's
-- fixed personal credential reference. Plaintext credentials remain in Vault.
BEGIN;

ALTER TABLE notification_routes
    DROP CONSTRAINT IF EXISTS notification_route_target_ref_check;

ALTER TABLE notification_routes
    ADD CONSTRAINT notification_route_target_ref_check CHECK (
        channel IS NOT NULL AND target_ref IS NOT NULL AND enabled IS NOT NULL AND (
            (channel = 'feishu' AND (
                target_ref = 'system:feishu' OR
                target_ref = 'credential_ref:user/' || user_id::text || '/notification/personal-feishu'
            )) OR
            (channel = 'qq' AND (
                target_ref = 'system:qq' OR
                target_ref = 'credential_ref:user/' || user_id::text || '/notification/personal-qq'
            )) OR
            (channel = 'webhook' AND target_ref ~ (
                '^credential_ref:user/' || user_id::text || '/notification/[A-Za-z0-9._-]+$'
            )) OR
            (enabled = false AND target_ref = '')
        )
    );

COMMIT;

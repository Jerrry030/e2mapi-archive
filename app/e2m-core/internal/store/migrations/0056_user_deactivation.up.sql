-- Client-service deactivation is a two-phase drain. Console authorization is
-- removed immediately, while Connector identity remains available only for
-- fail-closed gateway withdrawal until every published binding is revoked.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS deactivation_status TEXT NOT NULL DEFAULT 'none',
    ADD COLUMN IF NOT EXISTS deactivation_error_code TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS deactivation_requested_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS deactivation_completed_at TIMESTAMPTZ;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_deactivation_status_check,
    DROP CONSTRAINT IF EXISTS users_deactivation_shape_check;

ALTER TABLE users
    ADD CONSTRAINT users_deactivation_status_check CHECK (
        deactivation_status IN ('none','draining','failed','completed')
    ),
    ADD CONSTRAINT users_deactivation_shape_check CHECK (
        (deactivation_status = 'none'
            AND deactivation_error_code = ''
            AND deactivation_requested_at IS NULL
            AND deactivation_completed_at IS NULL)
        OR (deactivation_status = 'draining'
            AND deactivation_error_code = ''
            AND deactivation_requested_at IS NOT NULL
            AND deactivation_completed_at IS NULL)
        OR (deactivation_status = 'failed'
            AND deactivation_error_code <> ''
            AND deactivation_requested_at IS NOT NULL
            AND deactivation_completed_at IS NULL)
        OR (deactivation_status = 'completed'
            AND deactivation_error_code = ''
            AND deactivation_requested_at IS NOT NULL
            AND deactivation_completed_at IS NOT NULL)
    );

CREATE INDEX IF NOT EXISTS idx_users_deactivation_due
    ON users (deactivation_status, updated_at)
    WHERE deactivation_status IN ('draining','failed');

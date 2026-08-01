DROP INDEX IF EXISTS idx_users_deactivation_due;

ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_deactivation_shape_check,
    DROP CONSTRAINT IF EXISTS users_deactivation_status_check,
    DROP COLUMN IF EXISTS deactivation_completed_at,
    DROP COLUMN IF EXISTS deactivation_requested_at,
    DROP COLUMN IF EXISTS deactivation_error_code,
    DROP COLUMN IF EXISTS deactivation_status;

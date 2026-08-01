DROP INDEX IF EXISTS idx_published_bindings_verification;

ALTER TABLE published_bindings
    DROP CONSTRAINT IF EXISTS published_bindings_verification_shape_check,
    DROP CONSTRAINT IF EXISTS published_bindings_verification_source_check,
    DROP CONSTRAINT IF EXISTS published_bindings_verification_status_check,
    DROP COLUMN IF EXISTS verification_error_code,
    DROP COLUMN IF EXISTS verified_at,
    DROP COLUMN IF EXISTS verification_source,
    DROP COLUMN IF EXISTS verification_status;

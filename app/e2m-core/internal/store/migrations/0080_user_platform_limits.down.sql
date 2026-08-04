BEGIN;

ALTER TABLE users
    DROP COLUMN IF EXISTS platform_concurrency,
    DROP COLUMN IF EXISTS platform_rpm;

COMMIT;

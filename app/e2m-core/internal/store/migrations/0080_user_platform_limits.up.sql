BEGIN;

-- Per-user platform data-plane throttles. Zero means unlimited.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS platform_concurrency INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS platform_rpm INT NOT NULL DEFAULT 0;

COMMIT;

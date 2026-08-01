DROP TABLE IF EXISTS pool_retirement_items;
DROP TABLE IF EXISTS pool_retirement_jobs;
DROP TABLE IF EXISTS upstream_channel_migrations;

DROP INDEX IF EXISTS uq_upstream_key_previous_secret_ref;
ALTER TABLE upstream_key_deliveries
    DROP COLUMN IF EXISTS rotation_started_at,
    DROP COLUMN IF EXISTS rotation_status,
    DROP COLUMN IF EXISTS rotation_resume_status,
    DROP COLUMN IF EXISTS previous_key_version,
    DROP COLUMN IF EXISTS previous_masked_value,
    DROP COLUMN IF EXISTS previous_secret_ref;

DROP INDEX IF EXISTS idx_upstream_channels_inventory;
ALTER TABLE upstream_channels DROP COLUMN IF EXISTS inventory_state;
ALTER TABLE upstream_pools DROP COLUMN IF EXISTS safety_stock_threshold;
ALTER TABLE upstream_pools ALTER COLUMN status SET DEFAULT 'active';

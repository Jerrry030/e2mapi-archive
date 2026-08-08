BEGIN;

DROP TABLE IF EXISTS supply_channel_stats;

ALTER TABLE supply_usage_records
    DROP COLUMN IF EXISTS first_token_ms,
    DROP COLUMN IF EXISTS duration_ms;

COMMIT;

ALTER TABLE channel_health_snapshots
    DROP COLUMN IF EXISTS quality_error_rate,
    DROP COLUMN IF EXISTS quality_success_rate,
    DROP COLUMN IF EXISTS quality_sample_count;

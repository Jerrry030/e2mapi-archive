-- Separate factual downstream SLA metrics from attribution-aware upstream
-- quality metrics used by scheduling and health decisions.
ALTER TABLE channel_health_snapshots
    ADD COLUMN IF NOT EXISTS quality_sample_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS quality_success_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS quality_error_rate DOUBLE PRECISION NOT NULL DEFAULT 0;

-- Before this migration the legacy columns already excluded client errors and
-- cancellations. Preserve that historical scheduling evidence in the new
-- quality columns. Factual historical values cannot be reconstructed from the
-- aggregate rows, so the legacy columns remain unchanged for old buckets.
UPDATE channel_health_snapshots
SET quality_sample_count = sample_count,
    quality_success_rate = success_rate,
    quality_error_rate = error_rate;

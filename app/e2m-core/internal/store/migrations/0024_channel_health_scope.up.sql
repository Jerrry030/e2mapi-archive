-- Isolate quality observations/snapshots by downstream instance and model.
-- Snapshots are retained per one-minute recompute bucket so deduction and
-- recovery decisions can be audited without letting one downstream overwrite
-- another downstream's current state.
ALTER TABLE channel_health_snapshots
    ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS bucket_start TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS upstream_error_rate DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS upstream_failure_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS auth_failure_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS insufficient_balance_count INTEGER NOT NULL DEFAULT 0;

UPDATE channel_health_snapshots
SET bucket_start = date_trunc('minute', created_at)
WHERE bucket_start IS NULL;

ALTER TABLE channel_health_snapshots
    ALTER COLUMN bucket_start SET NOT NULL,
    ALTER COLUMN bucket_start SET DEFAULT date_trunc('minute', now());

ALTER TABLE channel_health_snapshots
    DROP CONSTRAINT IF EXISTS channel_health_snapshots_channel_id_window_key;

ALTER TABLE channel_health_snapshots
    ADD CONSTRAINT channel_health_snapshots_scope_bucket_key
    UNIQUE (instance_id, channel_id, model, "window", bucket_start);

CREATE INDEX IF NOT EXISTS idx_channel_obs_scope_time
    ON channel_observations (instance_id, channel_id, model, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_channel_snap_scope_current
    ON channel_health_snapshots
    (instance_id, channel_id, model, "window", bucket_start DESC);

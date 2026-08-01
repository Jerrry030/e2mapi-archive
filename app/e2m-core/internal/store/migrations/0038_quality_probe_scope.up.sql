-- Persist the exact protocol surface measured by an active quality probe.
-- Empty values retain legacy passive observations and channels, while active
-- recovery requires both fields to be configured explicitly.
ALTER TABLE upstream_channels
    ADD COLUMN IF NOT EXISTS probe_capability TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS probe_endpoint_path TEXT NOT NULL DEFAULT '';

ALTER TABLE channel_observations
    ADD COLUMN IF NOT EXISTS capability TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS endpoint_path TEXT NOT NULL DEFAULT '';

ALTER TABLE channel_health_snapshots
    ADD COLUMN IF NOT EXISTS capability TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS endpoint_path TEXT NOT NULL DEFAULT '';

ALTER TABLE channel_health_snapshots
    DROP CONSTRAINT IF EXISTS channel_health_snapshots_scope_bucket_key;

ALTER TABLE channel_health_snapshots
    ADD CONSTRAINT channel_health_snapshots_scope_bucket_key
    UNIQUE (instance_id, channel_id, model, capability, endpoint_path, "window", bucket_start);

CREATE INDEX IF NOT EXISTS idx_channel_obs_probe_scope_time
    ON channel_observations
    (instance_id, channel_id, model, capability, endpoint_path, observed_at DESC);

CREATE INDEX IF NOT EXISTS idx_channel_snap_probe_scope_current
    ON channel_health_snapshots
    (instance_id, channel_id, model, capability, endpoint_path, "window", bucket_start DESC);

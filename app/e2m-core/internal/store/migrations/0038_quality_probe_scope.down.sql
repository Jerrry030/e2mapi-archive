DROP INDEX IF EXISTS idx_channel_snap_probe_scope_current;
DROP INDEX IF EXISTS idx_channel_obs_probe_scope_time;

ALTER TABLE channel_health_snapshots
    DROP CONSTRAINT IF EXISTS channel_health_snapshots_scope_bucket_key;

DELETE FROM channel_health_snapshots older
USING channel_health_snapshots newer
WHERE older.instance_id = newer.instance_id
  AND older.channel_id = newer.channel_id
  AND older.model = newer.model
  AND older."window" = newer."window"
  AND older.bucket_start = newer.bucket_start
	AND (older.created_at, older.id) < (newer.created_at, newer.id);

ALTER TABLE channel_health_snapshots
    DROP COLUMN IF EXISTS endpoint_path,
    DROP COLUMN IF EXISTS capability;

ALTER TABLE channel_observations
    DROP COLUMN IF EXISTS endpoint_path,
    DROP COLUMN IF EXISTS capability;

ALTER TABLE upstream_channels
    DROP COLUMN IF EXISTS probe_endpoint_path,
    DROP COLUMN IF EXISTS probe_capability;

ALTER TABLE channel_health_snapshots
    ADD CONSTRAINT channel_health_snapshots_scope_bucket_key
    UNIQUE (instance_id, channel_id, model, "window", bucket_start);

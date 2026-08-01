DROP INDEX IF EXISTS idx_channel_snap_scope_current;
DROP INDEX IF EXISTS idx_channel_obs_scope_time;

ALTER TABLE channel_health_snapshots
    DROP CONSTRAINT IF EXISTS channel_health_snapshots_scope_bucket_key;

-- The legacy schema can retain only one row per channel/window. Keep the most
-- recently refreshed row if this migration is rolled back.
DELETE FROM channel_health_snapshots older
USING channel_health_snapshots newer
WHERE older.channel_id = newer.channel_id
  AND older."window" = newer."window"
  AND (
      older.bucket_start < newer.bucket_start OR
      (older.bucket_start = newer.bucket_start AND older.created_at < newer.created_at) OR
      (older.bucket_start = newer.bucket_start AND older.created_at = newer.created_at AND older.id < newer.id)
  );

ALTER TABLE channel_health_snapshots
    DROP COLUMN IF EXISTS insufficient_balance_count,
    DROP COLUMN IF EXISTS auth_failure_count,
    DROP COLUMN IF EXISTS upstream_failure_count,
    DROP COLUMN IF EXISTS upstream_error_rate,
    DROP COLUMN IF EXISTS bucket_start,
    DROP COLUMN IF EXISTS model;

ALTER TABLE channel_health_snapshots
    ADD CONSTRAINT channel_health_snapshots_channel_id_window_key
    UNIQUE (channel_id, "window");

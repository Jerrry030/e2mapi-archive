BEGIN;

DROP TRIGGER IF EXISTS trg_channel_health_snapshot_reject_update
    ON channel_health_snapshots;
DROP FUNCTION IF EXISTS reject_channel_health_snapshot_update();
DROP INDEX IF EXISTS idx_channel_health_snapshots_scope_revision_current;

-- The legacy schema can retain only one row per scope bucket. Keep the same
-- deterministic revision that current reads choose before restoring it.
DELETE FROM channel_health_snapshots AS older
USING channel_health_snapshots AS newer
WHERE older.instance_id = newer.instance_id
  AND older.channel_id = newer.channel_id
  AND older.model = newer.model
  AND older.capability = newer.capability
  AND older.endpoint_path = newer.endpoint_path
  AND older."window" = newer."window"
  AND older.bucket_start = newer.bucket_start
  AND (older.created_at, older.id) < (newer.created_at, newer.id);

ALTER TABLE channel_health_snapshots
    ADD CONSTRAINT channel_health_snapshots_scope_bucket_key
    UNIQUE (instance_id, channel_id, model, capability, endpoint_path,
            "window", bucket_start);

-- Restore the pre-0071 behavior for installations intentionally rolled back.
CREATE TRIGGER trg_channel_health_snapshot_upstream_fact_version_update
AFTER UPDATE ON channel_health_snapshots
FOR EACH ROW
WHEN (OLD IS DISTINCT FROM NEW)
EXECUTE FUNCTION bump_upstream_intelligence_version_for_quality_snapshot();

COMMIT;

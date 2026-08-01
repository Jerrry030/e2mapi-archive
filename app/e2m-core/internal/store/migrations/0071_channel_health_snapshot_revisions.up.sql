BEGIN;

-- A channel-health id is durable quality evidence referenced by historical
-- recommendations. Recomputing the same scope and bucket must therefore
-- append a revision instead of replacing the row behind an existing id.
ALTER TABLE channel_health_snapshots
    DROP CONSTRAINT IF EXISTS channel_health_snapshots_scope_bucket_key;

CREATE INDEX IF NOT EXISTS idx_channel_health_snapshots_scope_revision_current
    ON channel_health_snapshots
       (instance_id, channel_id, model, capability, endpoint_path, "window",
        bucket_start DESC, created_at DESC, id DESC);

-- Protect immutable evidence even from internal SQL writers which bypass
-- PostgresStore. Deletion remains available to explicit retention and
-- rollback procedures; only in-place mutation is forbidden.
CREATE OR REPLACE FUNCTION reject_channel_health_snapshot_update()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'channel health snapshot revisions are immutable'
        USING ERRCODE = '55000';
END;
$$;

DROP TRIGGER IF EXISTS trg_channel_health_snapshot_upstream_fact_version_update
    ON channel_health_snapshots;
DROP TRIGGER IF EXISTS trg_channel_health_snapshot_reject_update
    ON channel_health_snapshots;
CREATE TRIGGER trg_channel_health_snapshot_reject_update
BEFORE UPDATE ON channel_health_snapshots
FOR EACH ROW
EXECUTE FUNCTION reject_channel_health_snapshot_update();

COMMIT;

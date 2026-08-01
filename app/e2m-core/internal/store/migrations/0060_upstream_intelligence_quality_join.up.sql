BEGIN;

-- A price dimension is part of the mapping identity, never a wildcard. Keep
-- legacy rows for audit, but make an ambiguous active mapping ineligible
-- instead of guessing which price axis it represents. Advancing each affected
-- owner's fact version invalidates read cursors built before this repair.
WITH deactivated AS (
    UPDATE upstream_intelligence_links
       SET status = 'inactive',
           updated_at = now()
     WHERE status = 'active'
       AND price_dimension = ''
    RETURNING user_id
), affected_owners AS (
    SELECT user_id FROM deactivated
    UNION
    SELECT allocation.user_id
      FROM upstream_channel_allocations AS allocation
      JOIN channel_health_snapshots AS snapshot
        ON snapshot.channel_id = allocation.channel_id
)
INSERT INTO upstream_intelligence_fact_versions (user_id, fact_version, updated_at)
SELECT user_id, 1, statement_timestamp()
  FROM affected_owners
ON CONFLICT (user_id) DO UPDATE
SET fact_version = upstream_intelligence_fact_versions.fact_version + 1,
    updated_at = statement_timestamp();

ALTER TABLE upstream_intelligence_links
    ADD CONSTRAINT upstream_intelligence_links_active_dimension_check
    CHECK (status <> 'active' OR price_dimension <> '');

-- The existing current-snapshot index starts with instance_id. Frontier reads
-- start with the allocated channel and model, then compare current instance
-- scopes, so give that owner-joined query a matching access path.
CREATE INDEX IF NOT EXISTS idx_channel_health_snapshots_frontier_current
    ON channel_health_snapshots
       (channel_id, model, "window", bucket_start DESC, created_at DESC);

-- Quality snapshots and upstream intelligence facts share one owner-scoped
-- consistency token. Resolve ownership only through the durable allocation
-- ledger; channel_health_snapshots intentionally has no user-supplied owner id.
-- A trigger keeps the version write in the exact snapshot transaction even if
-- a future internal writer bypasses PostgresStore. Identical UPDATE rows do not
-- advance the version.
CREATE OR REPLACE FUNCTION bump_upstream_intelligence_version_for_quality_snapshot()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO upstream_intelligence_fact_versions (user_id, fact_version, updated_at)
    SELECT allocation.user_id, 1, statement_timestamp()
      FROM upstream_channel_allocations AS allocation
     WHERE allocation.channel_id = NEW.channel_id
    ON CONFLICT (user_id) DO UPDATE
    SET fact_version = upstream_intelligence_fact_versions.fact_version + 1,
        updated_at = statement_timestamp();
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_channel_health_snapshot_upstream_fact_version_insert
    ON channel_health_snapshots;
DROP TRIGGER IF EXISTS trg_channel_health_snapshot_upstream_fact_version_update
    ON channel_health_snapshots;
CREATE TRIGGER trg_channel_health_snapshot_upstream_fact_version_insert
AFTER INSERT ON channel_health_snapshots
FOR EACH ROW
EXECUTE FUNCTION bump_upstream_intelligence_version_for_quality_snapshot();
CREATE TRIGGER trg_channel_health_snapshot_upstream_fact_version_update
AFTER UPDATE ON channel_health_snapshots
FOR EACH ROW
WHEN (OLD IS DISTINCT FROM NEW)
EXECUTE FUNCTION bump_upstream_intelligence_version_for_quality_snapshot();

COMMIT;

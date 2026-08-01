BEGIN;

DROP TRIGGER IF EXISTS trg_channel_health_snapshot_upstream_fact_version_insert
    ON channel_health_snapshots;
DROP TRIGGER IF EXISTS trg_channel_health_snapshot_upstream_fact_version_update
    ON channel_health_snapshots;
DROP FUNCTION IF EXISTS bump_upstream_intelligence_version_for_quality_snapshot();
DROP INDEX IF EXISTS idx_channel_health_snapshots_frontier_current;
ALTER TABLE upstream_intelligence_links
    DROP CONSTRAINT IF EXISTS upstream_intelligence_links_active_dimension_check;

-- Deliberately do not reactivate legacy empty-dimension links: a down migration
-- cannot recover the price axis that was never recorded.
COMMIT;

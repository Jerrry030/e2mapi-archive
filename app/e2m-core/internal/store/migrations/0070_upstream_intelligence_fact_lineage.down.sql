BEGIN;

-- Restore migration 0060's direct bump implementation before removing the
-- lineage function it replaced. The existing triggers retain their identity.
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

DROP FUNCTION IF EXISTS record_upstream_intelligence_fact_mutation(BIGINT, TEXT, TEXT);
DROP TABLE IF EXISTS upstream_intelligence_fact_mutations;
DROP TABLE IF EXISTS upstream_intelligence_fact_lineage_watermarks;

COMMIT;

BEGIN;

DROP TABLE IF EXISTS upstream_intelligence_links;
DROP INDEX IF EXISTS uq_upstream_channel_allocations_owner_channel;
DROP TABLE IF EXISTS upstream_change_events;
DROP TABLE IF EXISTS upstream_snapshot_absences;
DROP TABLE IF EXISTS upstream_offer_observations;
DROP TABLE IF EXISTS upstream_wallet_observations;
DROP TABLE IF EXISTS upstream_ingest_batches;
DROP TABLE IF EXISTS upstream_collection_runs;
DROP TABLE IF EXISTS upstream_intelligence_sources;
DROP TABLE IF EXISTS upstream_intelligence_fact_versions;

COMMIT;

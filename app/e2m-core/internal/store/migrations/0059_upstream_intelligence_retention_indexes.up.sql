BEGIN;

-- Owner-scoped raw-history pruning walks the oldest receipts first and keeps
-- the newest finalized/current snapshots for each source.
CREATE INDEX IF NOT EXISTS idx_upstream_collection_runs_owner_retention
    ON upstream_collection_runs (user_id, received_at, id);
CREATE INDEX IF NOT EXISTS idx_upstream_collection_runs_current_frontier
    ON upstream_collection_runs (user_id, source_id, observed_at DESC, id DESC)
    WHERE finalized_fact_version > 0;
CREATE INDEX IF NOT EXISTS idx_upstream_collection_runs_complete_frontier
    ON upstream_collection_runs (user_id, source_id, observed_at DESC, id DESC)
    WHERE finalized_fact_version > 0 AND status = 'succeeded' AND coverage = 'complete';
CREATE INDEX IF NOT EXISTS idx_upstream_wallet_observations_current_frontier
    ON upstream_wallet_observations (user_id, source_id, observed_at DESC, run_id DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_upstream_offer_observations_current_frontier
    ON upstream_offer_observations
       (user_id, source_id, group_key, model_key, price_dimension, observed_at DESC, run_id DESC, id DESC);

-- These partial indexes make evidence-pin checks bounded without indexing
-- empty sentinel values used by events and absence state.
CREATE INDEX IF NOT EXISTS idx_upstream_snapshot_absences_last_present_run
    ON upstream_snapshot_absences (user_id, source_id, last_present_run_id)
    WHERE last_present_run_id <> '';
CREATE INDEX IF NOT EXISTS idx_upstream_change_events_before_observation
    ON upstream_change_events (user_id, source_id, before_observation_id)
    WHERE before_observation_id <> '';
CREATE INDEX IF NOT EXISTS idx_upstream_change_events_after_observation
    ON upstream_change_events (user_id, source_id, after_observation_id)
    WHERE after_observation_id <> '';

COMMIT;

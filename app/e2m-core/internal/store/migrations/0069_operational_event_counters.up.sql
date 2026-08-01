BEGIN;

-- 0069 is a coordinated, forward-only cutover, not a rolling/mixed-version
-- migration. Drain every pre-0069 Core writer before applying it. The bounded
-- locks and compatibility fence are a final fail-closed integrity control;
-- they do not make legacy security-event paths or application rollback
-- compatible with schema 0069.
SET LOCAL lock_timeout = '30s';
SET LOCAL statement_timeout = '5min';

-- Lock every business table that contributes to the counter snapshot before
-- creating/backfilling it. A legacy fact writer either commits before all
-- locks are granted (and is included in the snapshot) or fails closed after
-- the fence is installed. It cannot commit in the snapshot/incremental gap.
LOCK TABLE
    reconcile_runs,
    upstream_collection_runs,
    upstream_ingest_batches,
    upstream_wallet_observations,
    upstream_offer_observations,
    upstream_change_events,
    upstream_shadow_results,
    upstream_dry_run_results
IN ACCESS EXCLUSIVE MODE;

CREATE TABLE operational_event_counters (
    kind       TEXT PRIMARY KEY CHECK (kind IN (
                   'credential_leak_detected',
                   'cross_owner_rejected',
                   'false_removal_invariant'
               )),
    total      BIGINT NOT NULL DEFAULT 0 CHECK (total >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- Monotonic exporter facts are kept outside retention-managed business
-- history. Each finalized run contributes exactly once through the run's
-- durable finalization fence; ingest outcomes contribute once per accepted or
-- replayed request. Closed labels are enforced here as well as in Go.
CREATE TABLE operational_metric_counters (
    metric     TEXT NOT NULL CHECK (metric IN (
                   'collection_runs',
                   'collection_facts',
                   'collection_coverage',
                   'ingest_facts',
                   'change_events',
                   'reconcile_runs',
                   'experiments'
               )),
    label      TEXT NOT NULL CHECK (BTRIM(label) <> '' AND char_length(label) <= 64),
    total      BIGINT NOT NULL DEFAULT 0 CHECK (total >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (metric,label),
    CHECK (
        (metric IN ('collection_runs','collection_facts') AND label IN ('succeeded','partial','failed')) OR
        (metric='collection_coverage' AND label IN ('complete','partial','unavailable')) OR
        (metric='ingest_facts' AND label IN ('accepted','duplicate')) OR
        (metric='change_events' AND label IN (
            'balance_low','balance_recovered','group_added','group_changed','group_removed',
            'model_added','price_increased','price_decreased','model_removed','source_stale','source_recovered')) OR
        (metric='reconcile_runs' AND label IN ('dry_run','apply','rollback')) OR
        (metric='experiments' AND label IN ('shadow','dry_run'))
    )
);

CREATE TABLE operational_collection_duration_counters (
    result     TEXT PRIMARY KEY CHECK (result IN ('succeeded','partial','failed')),
    count      BIGINT NOT NULL DEFAULT 0 CHECK (count >= 0),
    sum_seconds DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (sum_seconds >= 0),
    le_0_1     BIGINT NOT NULL DEFAULT 0 CHECK (le_0_1 >= 0),
    le_0_5     BIGINT NOT NULL DEFAULT 0 CHECK (le_0_5 >= 0),
    le_1       BIGINT NOT NULL DEFAULT 0 CHECK (le_1 >= 0),
    le_2       BIGINT NOT NULL DEFAULT 0 CHECK (le_2 >= 0),
    le_5       BIGINT NOT NULL DEFAULT 0 CHECK (le_5 >= 0),
    le_10      BIGINT NOT NULL DEFAULT 0 CHECK (le_10 >= 0),
    le_30      BIGINT NOT NULL DEFAULT 0 CHECK (le_30 >= 0),
    le_60      BIGINT NOT NULL DEFAULT 0 CHECK (le_60 >= 0),
    le_300     BIGINT NOT NULL DEFAULT 0 CHECK (le_300 >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT statement_timestamp()
);

-- Backfill existing durable facts before writers switch to incremental
-- updates. The locks above and the write fence below enforce the required
-- previous-binary stop instead of relying on a deployment comment alone.
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'collection_runs',status,COUNT(*) FROM upstream_collection_runs
 WHERE finalized_fact_version>0 AND status IN ('succeeded','partial','failed') GROUP BY status;
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'collection_facts',status,COALESCE(SUM(fact_count),0) FROM upstream_collection_runs
 WHERE finalized_fact_version>0 AND status IN ('succeeded','partial','failed') GROUP BY status;
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'collection_coverage',coverage,COUNT(*) FROM upstream_collection_runs
 WHERE finalized_fact_version>0 AND coverage IN ('complete','partial','unavailable') GROUP BY coverage;
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'ingest_facts','accepted',COALESCE(SUM(wallet_count+offer_count),0) FROM upstream_ingest_batches;
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'change_events',event_type,COUNT(*) FROM upstream_change_events GROUP BY event_type;
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'reconcile_runs',kind,COUNT(*) FROM reconcile_runs WHERE kind IN ('dry_run','apply','rollback') GROUP BY kind;
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'experiments','shadow',COUNT(*) FROM upstream_shadow_results;
INSERT INTO operational_metric_counters (metric,label,total)
SELECT 'experiments','dry_run',COUNT(*) FROM upstream_dry_run_results;

INSERT INTO operational_collection_duration_counters
    (result,count,sum_seconds,le_0_1,le_0_5,le_1,le_2,le_5,le_10,le_30,le_60,le_300)
SELECT status,COUNT(*),SUM(duration),
       COUNT(*) FILTER (WHERE duration<=0.1),COUNT(*) FILTER (WHERE duration<=0.5),
       COUNT(*) FILTER (WHERE duration<=1),COUNT(*) FILTER (WHERE duration<=2),
       COUNT(*) FILTER (WHERE duration<=5),COUNT(*) FILTER (WHERE duration<=10),
       COUNT(*) FILTER (WHERE duration<=30),COUNT(*) FILTER (WHERE duration<=60),
       COUNT(*) FILTER (WHERE duration<=300)
  FROM (
      SELECT status,EXTRACT(EPOCH FROM (completed_at-started_at))::double precision duration
        FROM upstream_collection_runs
       WHERE finalized_fact_version>0 AND status IN ('succeeded','partial','failed')
         AND completed_at IS NOT NULL AND completed_at>=started_at
  ) finalized GROUP BY status;

-- Current Core pools set this marker on every physical PostgreSQL connection.
-- Older binaries do not, so after the migration commits they cannot mutate a
-- counter-participating business fact. This is deliberately fail-closed, but
-- it is not proof that an old binary is safe to keep serving: security events
-- without fact-table DML cannot be fenced by these triggers. Future migrations
-- that intentionally mutate a guarded table must explicitly use the current
-- writer protocol in their transaction.
CREATE FUNCTION require_operational_counter_writer() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('e2m.operational_counter_writer', true)
       IS DISTINCT FROM 'incremental-v1' THEN
        RAISE EXCEPTION USING
            ERRCODE = '55000',
            MESSAGE = 'operational counter writer compatibility fence',
            HINT = 'stop pre-0069 Core processes and use a current Core database connection';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
	IF TG_LEVEL = 'STATEMENT' THEN
		RETURN NULL;
	END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER require_operational_counter_writer_reconcile
BEFORE INSERT OR UPDATE OR DELETE ON reconcile_runs
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_collection_runs
BEFORE INSERT OR UPDATE OR DELETE ON upstream_collection_runs
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_ingest_batches
BEFORE INSERT OR UPDATE OR DELETE ON upstream_ingest_batches
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_wallets
BEFORE INSERT OR UPDATE OR DELETE ON upstream_wallet_observations
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_offers
BEFORE INSERT OR UPDATE OR DELETE ON upstream_offer_observations
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_changes
BEFORE INSERT OR UPDATE OR DELETE ON upstream_change_events
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_shadow
BEFORE INSERT OR UPDATE OR DELETE ON upstream_shadow_results
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_dry_run
BEFORE INSERT OR UPDATE OR DELETE ON upstream_dry_run_results
FOR EACH ROW EXECUTE FUNCTION require_operational_counter_writer();

-- Row triggers do not fire for TRUNCATE. Guard it separately so maintenance
-- sessions and legacy processes cannot bypass the writer-protocol fence.
CREATE TRIGGER require_operational_counter_writer_reconcile_truncate
BEFORE TRUNCATE ON reconcile_runs
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_collection_runs_truncate
BEFORE TRUNCATE ON upstream_collection_runs
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_ingest_batches_truncate
BEFORE TRUNCATE ON upstream_ingest_batches
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_wallets_truncate
BEFORE TRUNCATE ON upstream_wallet_observations
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_offers_truncate
BEFORE TRUNCATE ON upstream_offer_observations
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_changes_truncate
BEFORE TRUNCATE ON upstream_change_events
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_shadow_truncate
BEFORE TRUNCATE ON upstream_shadow_results
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();
CREATE TRIGGER require_operational_counter_writer_dry_run_truncate
BEFORE TRUNCATE ON upstream_dry_run_results
FOR EACH STATEMENT EXECUTE FUNCTION require_operational_counter_writer();

COMMIT;

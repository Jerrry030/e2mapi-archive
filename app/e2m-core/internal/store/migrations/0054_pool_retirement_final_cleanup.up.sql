-- Pool retirement is a two-phase durable workflow. The existing item status
-- tracks the reversible drain/rollback. These fields track the final reconcile
-- that, after the pool is retired, queues generation-fenced deferred deletes.
ALTER TABLE pool_retirement_jobs
    DROP CONSTRAINT IF EXISTS pool_retirement_jobs_status_check;

ALTER TABLE pool_retirement_jobs
    ADD CONSTRAINT pool_retirement_jobs_status_check
    CHECK (status IN ('pending','running','partial','finalizing','cleanup','completed'));

ALTER TABLE pool_retirement_jobs
    ADD COLUMN IF NOT EXISTS cleanup_completed_plans INTEGER NOT NULL DEFAULT 0
        CHECK (cleanup_completed_plans >= 0),
    ADD COLUMN IF NOT EXISTS cleanup_failed_plans INTEGER NOT NULL DEFAULT 0
        CHECK (cleanup_failed_plans >= 0);

ALTER TABLE pool_retirement_items
    ADD COLUMN IF NOT EXISTS cleanup_status TEXT NOT NULL DEFAULT 'pending'
        CHECK (cleanup_status IN ('pending','running','completed','failed')),
    ADD COLUMN IF NOT EXISTS cleanup_last_error TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cleanup_attempts INTEGER NOT NULL DEFAULT 0
        CHECK (cleanup_attempts >= 0),
    ADD COLUMN IF NOT EXISTS cleanup_lease_until TIMESTAMPTZ;

-- Existing completed jobs predate the final-cleanup contract. Preserve them as
-- historical facts and do not reopen their remote lifecycle automatically.
UPDATE pool_retirement_items item
   SET cleanup_status='completed', cleanup_last_error='', cleanup_lease_until=NULL
  FROM pool_retirement_jobs job
 WHERE item.job_id=job.id AND job.status='completed';

UPDATE pool_retirement_jobs job
   SET cleanup_completed_plans=job.total_plans, cleanup_failed_plans=0
 WHERE job.status='completed';

DROP INDEX IF EXISTS uq_pool_retirement_active;
CREATE UNIQUE INDEX uq_pool_retirement_active
    ON pool_retirement_jobs (pool_id)
    WHERE status IN ('pending','running','partial','finalizing','cleanup');

CREATE INDEX IF NOT EXISTS idx_pool_retirement_cleanup_work
    ON pool_retirement_items (job_id, cleanup_status, plan_id);

DROP INDEX IF EXISTS idx_pool_retirement_cleanup_work;

-- The pre-0054 schema cannot represent a durable cleanup phase. Make an
-- in-flight downgrade conservative and retryable under the old runner instead
-- of failing the old status check (or silently marking cleanup complete).
UPDATE pool_retirement_items item
   SET status='failed',
       last_error=CASE
         WHEN BTRIM(item.cleanup_last_error) <> '' THEN item.cleanup_last_error
         ELSE 'migration 0054 rolled back while final cleanup was incomplete'
       END,
       lease_until=NULL,
       updated_at=statement_timestamp()
  FROM pool_retirement_jobs job
 WHERE item.job_id=job.id
   AND job.status='cleanup'
   AND item.cleanup_status <> 'completed';

UPDATE pool_retirement_items item
   SET status='completed',last_error='',lease_until=NULL,updated_at=statement_timestamp()
  FROM pool_retirement_jobs job
 WHERE item.job_id=job.id
   AND job.status='cleanup'
   AND item.cleanup_status='completed';

UPDATE pool_retirement_jobs job
   SET status='partial',
       completed_plans=summary.completed,
       failed_plans=summary.failed,
       last_error='migration 0054 rolled back while final cleanup was incomplete',
       completed_at=NULL,
       updated_at=statement_timestamp()
  FROM (
    SELECT job_id,
           COUNT(*) FILTER (WHERE status='completed')::integer AS completed,
           COUNT(*) FILTER (WHERE status='failed')::integer AS failed
      FROM pool_retirement_items
     GROUP BY job_id
  ) summary
 WHERE job.id=summary.job_id AND job.status='cleanup';

DROP INDEX IF EXISTS uq_pool_retirement_active;
CREATE UNIQUE INDEX uq_pool_retirement_active
    ON pool_retirement_jobs (pool_id)
    WHERE status IN ('pending','running','partial','finalizing');

ALTER TABLE pool_retirement_jobs
    DROP CONSTRAINT IF EXISTS pool_retirement_jobs_status_check;

ALTER TABLE pool_retirement_jobs
    ADD CONSTRAINT pool_retirement_jobs_status_check
    CHECK (status IN ('pending','running','partial','finalizing','completed'));

ALTER TABLE pool_retirement_items
    DROP COLUMN IF EXISTS cleanup_lease_until,
    DROP COLUMN IF EXISTS cleanup_attempts,
    DROP COLUMN IF EXISTS cleanup_last_error,
    DROP COLUMN IF EXISTS cleanup_status;

ALTER TABLE pool_retirement_jobs
    DROP COLUMN IF EXISTS cleanup_failed_plans,
    DROP COLUMN IF EXISTS cleanup_completed_plans;

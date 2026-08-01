ALTER TABLE route_plans DROP COLUMN IF EXISTS rollout_canary_count;
ALTER TABLE route_plans DROP COLUMN IF EXISTS rollout_batch_size;
ALTER TABLE route_plans DROP COLUMN IF EXISTS rollout;
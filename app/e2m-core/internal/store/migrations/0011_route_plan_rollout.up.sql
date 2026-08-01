-- Gray-rollout controls for platform-managed route plans: how aggressively a
-- reconcile brings newly-activated channels into scheduling (immediate | canary
-- | batched), plus the wave sizes.
ALTER TABLE route_plans ADD COLUMN IF NOT EXISTS rollout TEXT NOT NULL DEFAULT 'immediate';
ALTER TABLE route_plans ADD COLUMN IF NOT EXISTS rollout_batch_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE route_plans ADD COLUMN IF NOT EXISTS rollout_canary_count INTEGER NOT NULL DEFAULT 0;
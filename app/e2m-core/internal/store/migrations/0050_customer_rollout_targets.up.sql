-- Managed pools are deny-by-default. Explicit user or instance targets decide
-- which customer receives a pool; an instance target overrides its user rule.
-- Rollout settings are copied to the automatically-created route plan.
CREATE TABLE IF NOT EXISTS pool_rollout_targets (
    id                   TEXT PRIMARY KEY,
    pool_id              TEXT NOT NULL REFERENCES upstream_pools(id) ON DELETE CASCADE,
    scope                TEXT NOT NULL CHECK (scope IN ('user','instance')),
    user_id              BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id          TEXT,
    enabled              BOOLEAN NOT NULL DEFAULT TRUE,
    rollout              TEXT NOT NULL DEFAULT 'immediate'
                         CHECK (rollout IN ('immediate','canary','batched')),
    rollout_batch_size   INTEGER NOT NULL DEFAULT 0 CHECK (rollout_batch_size >= 0),
    rollout_canary_count INTEGER NOT NULL DEFAULT 0 CHECK (rollout_canary_count >= 0),
    note                 TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT pool_rollout_scope_shape CHECK (
        (scope='user' AND instance_id IS NULL) OR
        (scope='instance' AND instance_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_pool_rollout_targets_pool
    ON pool_rollout_targets (pool_id, scope, user_id, instance_id);

CREATE UNIQUE INDEX IF NOT EXISTS uq_pool_rollout_user
    ON pool_rollout_targets (pool_id, user_id) WHERE scope='user';
CREATE UNIQUE INDEX IF NOT EXISTS uq_pool_rollout_instance
    ON pool_rollout_targets (pool_id, instance_id) WHERE scope='instance';

CREATE TABLE IF NOT EXISTS pool_rollout_operations (
    id                  TEXT PRIMARY KEY,
    pool_id             TEXT NOT NULL REFERENCES upstream_pools(id) ON DELETE CASCADE,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id         TEXT NOT NULL,
    plan_id             TEXT NOT NULL DEFAULT '',
    target_id           TEXT NOT NULL DEFAULT '',
    action              TEXT NOT NULL CHECK (action IN ('drain','publish')),
    status              TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','running','succeeded','failed','superseded')),
    desired_fingerprint TEXT NOT NULL UNIQUE,
    attempts            INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error          TEXT NOT NULL DEFAULT '',
    version             BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    lease_owner         TEXT NOT NULL DEFAULT '',
    lease_until         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pool_rollout_operations_due
    ON pool_rollout_operations (status, updated_at);
CREATE INDEX IF NOT EXISTS idx_pool_rollout_operations_pool
    ON pool_rollout_operations (pool_id, instance_id, updated_at DESC);

-- Existing live plans predate explicit customer allowlists. Preserve their
-- exact instance-level service scope during upgrade; all new combinations stay
-- denied until an operator grants access.
INSERT INTO pool_rollout_targets
    (id, pool_id, scope, user_id, instance_id, enabled, rollout,
     rollout_batch_size, rollout_canary_count, note, created_at, updated_at)
SELECT 'rollout-grandfather-' || md5(plan.pool_id || ':' || plan.instance_id),
       plan.pool_id, 'instance', plan.user_id, plan.instance_id, TRUE,
       CASE WHEN plan.rollout IN ('immediate','canary','batched') THEN plan.rollout ELSE 'immediate' END,
       GREATEST(plan.rollout_batch_size, 0),
       GREATEST(plan.rollout_canary_count, 0),
       'grandfathered from an existing route plan',
       statement_timestamp(), statement_timestamp()
  FROM route_plans plan
 WHERE plan.status IN ('draft','published')
ON CONFLICT (pool_id, instance_id) WHERE scope='instance' DO NOTHING;

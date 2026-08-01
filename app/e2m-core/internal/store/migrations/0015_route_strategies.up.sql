-- Route strategies (Phase 5): the persisted health-driven selection policy,
-- scoped to a plan, pool, or user. The auto-switch orchestrator resolves a
-- plan's effective strategy by precedence plan > pool > user > built-in
-- default, so the platform ships user-wide defaults while a single plan can
-- override. thresholds/weights are stored as JSONB. Unused scope columns
-- default to 0 so the (scope, plan_id, pool_id, user_id) unique index gives
-- exactly one strategy per scoped target and supports upsert.
CREATE TABLE IF NOT EXISTS route_strategies (
    id                            TEXT PRIMARY KEY,
    name                          TEXT NOT NULL DEFAULT '',
    type                          TEXT NOT NULL DEFAULT 'stability_first',
    scope                         TEXT NOT NULL DEFAULT 'user',
    plan_id                       TEXT NOT NULL DEFAULT '',
    pool_id                       TEXT NOT NULL DEFAULT '',
    user_id                     BIGINT NOT NULL DEFAULT 0,
    thresholds                    JSONB NOT NULL DEFAULT '{}',
    weights                       JSONB NOT NULL DEFAULT '{}',
    auto_apply                    BOOLEAN NOT NULL DEFAULT false,
    approval_required             BOOLEAN NOT NULL DEFAULT false,
    cooldown_seconds              INTEGER NOT NULL DEFAULT 0,
    recovery_observation_seconds  INTEGER NOT NULL DEFAULT 0,
    max_auto_switches_per_hour    INTEGER NOT NULL DEFAULT 0,
    created_at                    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_route_strategy_scope
    ON route_strategies (scope, plan_id, pool_id, user_id);
CREATE INDEX IF NOT EXISTS idx_route_strategy_user ON route_strategies (user_id);
CREATE INDEX IF NOT EXISTS idx_route_strategy_pool ON route_strategies (pool_id);
CREATE INDEX IF NOT EXISTS idx_route_strategy_plan ON route_strategies (plan_id);

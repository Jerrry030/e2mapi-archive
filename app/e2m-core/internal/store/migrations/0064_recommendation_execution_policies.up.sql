-- Owner-scoped, explicit opt-in policy for intelligence-driven execution.
-- Absence and the database default are both fail-closed (disabled).
BEGIN;

CREATE TABLE recommendation_execution_policies (
    id                  TEXT PRIMARY KEY,
    user_id             BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope               TEXT NOT NULL CHECK (scope IN ('plan','pool')),
    plan_id             TEXT NOT NULL DEFAULT '',
    pool_id             TEXT NOT NULL DEFAULT '',
    enabled             BOOLEAN NOT NULL DEFAULT FALSE,
    kill_switch         BOOLEAN NOT NULL DEFAULT FALSE,
    daily_execution_cap INTEGER NOT NULL,
    cooldown_seconds    INTEGER NOT NULL,
    minimum_savings     NUMERIC(38,18) NOT NULL,
    version             BIGINT NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT recommendation_execution_policy_scope_shape CHECK (
        (scope='plan' AND plan_id <> '' AND plan_id=btrim(plan_id) AND pool_id='') OR
        (scope='pool' AND pool_id <> '' AND pool_id=btrim(pool_id) AND plan_id='')
    ),
    CONSTRAINT recommendation_execution_policy_id_shape CHECK (
        id <> '' AND id=btrim(id) AND char_length(id) <= 256
    ),
    CONSTRAINT recommendation_execution_policy_target_length CHECK (
        char_length(plan_id) <= 256 AND char_length(pool_id) <= 256
    ),
    CONSTRAINT recommendation_execution_policy_guard_range CHECK (
        daily_execution_cap BETWEEN 1 AND 2147483647 AND
        cooldown_seconds BETWEEN 0 AND 31536000 AND
        minimum_savings >= 0 AND
        version > 0
    ),
    CONSTRAINT recommendation_execution_policy_owner_scope UNIQUE (user_id,scope,plan_id,pool_id)
);

CREATE INDEX idx_recommendation_execution_policies_owner
    ON recommendation_execution_policies (user_id,scope,updated_at DESC);

COMMIT;

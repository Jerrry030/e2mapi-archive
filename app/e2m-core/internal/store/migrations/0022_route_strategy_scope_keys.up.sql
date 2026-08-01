-- Canonicalize strategy ownership so each semantic scope has exactly one key.
-- Older API clients could populate unrelated owner columns, which made the
-- four-column unique index treat one plan/pool/user as several strategies.
BEGIN;

LOCK TABLE route_strategies IN ACCESS EXCLUSIVE MODE;
DROP INDEX IF EXISTS uq_route_strategy_scope;

-- Whitespace-only keys and unknown scopes cannot be tied safely to a target.
-- Discard those rows rather than guessing ownership and applying a policy to
-- the wrong plan, pool, or user.
UPDATE route_strategies
SET scope = btrim(COALESCE(scope, '')),
    name = COALESCE(name, ''),
    plan_id = btrim(COALESCE(plan_id, '')),
    pool_id = btrim(COALESCE(pool_id, '')),
    user_id = COALESCE(user_id, 0),
    type = btrim(COALESCE(type, '')),
    auto_apply = COALESCE(auto_apply, false),
    approval_required = COALESCE(approval_required, false),
    cooldown_seconds = COALESCE(cooldown_seconds, 0),
    recovery_observation_seconds = COALESCE(recovery_observation_seconds, 0),
    max_auto_switches_per_hour = COALESCE(max_auto_switches_per_hour, 0),
    created_at = COALESCE(created_at, updated_at, now()),
    updated_at = COALESCE(updated_at, created_at, now());

DELETE FROM route_strategies
WHERE scope NOT IN ('plan', 'pool', 'user')
   OR (scope = 'plan' AND plan_id = '')
   OR (scope = 'pool' AND pool_id = '')
   OR (scope = 'user' AND user_id <= 0)
   OR (scope = 'plan' AND NOT EXISTS (
       SELECT 1 FROM route_plans WHERE route_plans.id = route_strategies.plan_id
   ))
   OR (scope = 'pool' AND NOT EXISTS (
       SELECT 1 FROM upstream_pools WHERE upstream_pools.id = route_strategies.pool_id
   ))
   OR (scope = 'user' AND NOT EXISTS (
       SELECT 1 FROM users WHERE users.id = route_strategies.user_id
   ));

-- Persisted free-form types used to be normalized only when read. Store the
-- conservative default explicitly before the type constraint is installed.
UPDATE route_strategies
SET type = 'stability_first'
WHERE type NOT IN ('stability_first', 'cost_first', 'latency_first', 'balanced');

-- Invalid legacy JSON cannot be partially trusted. Empty objects deliberately
-- select the strategy engine's safe type-specific defaults.
UPDATE route_strategies
SET thresholds = '{}'
WHERE NOT CASE
    WHEN thresholds IS NULL OR jsonb_typeof(thresholds) <> 'object' THEN false
    WHEN thresholds - ARRAY[
        'min_samples', 'target_success_rate', 'floor_success_rate',
        'max_ttft_p95_ms', 'max_duration_p95_ms',
        'consecutive_failure_limit'
    ] <> '{}'::jsonb THEN false
    WHEN jsonb_path_exists(thresholds, '$.* ? (@.type() != "number")') THEN false
    WHEN thresholds ? 'min_samples' AND (thresholds->>'min_samples') !~ '^(0|[1-9][0-9]*)$' THEN false
    WHEN thresholds ? 'consecutive_failure_limit' AND (thresholds->>'consecutive_failure_limit') !~ '^(0|[1-9][0-9]*)$' THEN false
    WHEN jsonb_path_exists(thresholds, '$.min_samples ? (@ > 9223372036854775807)') THEN false
    WHEN jsonb_path_exists(thresholds, '$.consecutive_failure_limit ? (@ > 9223372036854775807)') THEN false
    WHEN jsonb_path_exists(thresholds, '$.target_success_rate ? (@ < 0 || @ > 1)') THEN false
    WHEN jsonb_path_exists(thresholds, '$.floor_success_rate ? (@ < 0 || @ > 1)') THEN false
    WHEN jsonb_path_exists(thresholds, '$.max_ttft_p95_ms ? (@ < 0 || @ > 1.7976931348623157e308)') THEN false
    WHEN jsonb_path_exists(thresholds, '$.max_duration_p95_ms ? (@ < 0 || @ > 1.7976931348623157e308)') THEN false
    ELSE COALESCE(NULLIF((thresholds->>'floor_success_rate')::numeric, 0), 0.85)
         <= COALESCE(NULLIF((thresholds->>'target_success_rate')::numeric, 0), 0.95)
END;

UPDATE route_strategies
SET weights = '{}'
WHERE NOT CASE
    WHEN weights IS NULL OR jsonb_typeof(weights) <> 'object' THEN false
    WHEN weights - ARRAY['success', 'ttft', 'duration', 'stability', 'cost'] <> '{}'::jsonb THEN false
    WHEN jsonb_path_exists(weights, '$.* ? (@.type() != "number" || @ < 0 || @ > 1)') THEN false
    ELSE
        COALESCE((weights->>'success')::numeric, 0)
      + COALESCE((weights->>'ttft')::numeric, 0)
      + COALESCE((weights->>'duration')::numeric, 0)
      + COALESCE((weights->>'stability')::numeric, 0)
      + COALESCE((weights->>'cost')::numeric, 0) = 0
      OR abs(
          COALESCE((weights->>'success')::numeric, 0)
        + COALESCE((weights->>'ttft')::numeric, 0)
        + COALESCE((weights->>'duration')::numeric, 0)
        + COALESCE((weights->>'stability')::numeric, 0)
        + COALESCE((weights->>'cost')::numeric, 0) - 1
      ) <= 0.001
END;

UPDATE route_strategies
SET cooldown_seconds = least(greatest(cooldown_seconds, 0), 604800),
    recovery_observation_seconds = least(greatest(recovery_observation_seconds, 0), 604800),
    max_auto_switches_per_hour = least(greatest(max_auto_switches_per_hour, 0), 3600);

-- Remove irrelevant owner columns before ranking, then keep the most recently
-- updated row for every canonical semantic key.
UPDATE route_strategies
SET pool_id = '', user_id = 0
WHERE scope = 'plan';

UPDATE route_strategies
SET plan_id = '', user_id = 0
WHERE scope = 'pool';

UPDATE route_strategies
SET plan_id = '', pool_id = ''
WHERE scope = 'user';

WITH ranked AS (
    SELECT id,
           row_number() OVER (
               PARTITION BY scope,
                   CASE scope
                       WHEN 'plan' THEN plan_id
                       WHEN 'pool' THEN pool_id
                       ELSE user_id::text
                   END
               ORDER BY updated_at DESC, id DESC
           ) AS position
    FROM route_strategies
)
DELETE FROM route_strategies
WHERE id IN (SELECT id FROM ranked WHERE position > 1);

CREATE UNIQUE INDEX uq_route_strategy_scope
    ON route_strategies (scope, plan_id, pool_id, user_id);

ALTER TABLE route_strategies
    ALTER COLUMN name SET NOT NULL,
    ALTER COLUMN type SET NOT NULL,
    ALTER COLUMN scope SET NOT NULL,
    ALTER COLUMN plan_id SET NOT NULL,
    ALTER COLUMN pool_id SET NOT NULL,
    ALTER COLUMN user_id SET NOT NULL,
    ALTER COLUMN thresholds SET NOT NULL,
    ALTER COLUMN weights SET NOT NULL,
    ALTER COLUMN auto_apply SET NOT NULL,
    ALTER COLUMN approval_required SET NOT NULL,
    ALTER COLUMN cooldown_seconds SET NOT NULL,
    ALTER COLUMN recovery_observation_seconds SET NOT NULL,
    ALTER COLUMN max_auto_switches_per_hour SET NOT NULL,
    ALTER COLUMN created_at SET NOT NULL,
    ALTER COLUMN updated_at SET NOT NULL,
    ADD CONSTRAINT route_strategy_scope_owner_check CHECK (
        scope IS NOT NULL AND plan_id IS NOT NULL AND pool_id IS NOT NULL AND user_id IS NOT NULL AND (
            (scope = 'plan' AND plan_id <> '' AND plan_id = btrim(plan_id) AND pool_id = '' AND user_id = 0) OR
            (scope = 'pool' AND plan_id = '' AND pool_id <> '' AND pool_id = btrim(pool_id) AND user_id = 0) OR
            (scope = 'user' AND plan_id = '' AND pool_id = '' AND user_id > 0)
        )
    ),
    ADD CONSTRAINT route_strategy_type_check CHECK (
        type IS NOT NULL AND type IN ('stability_first', 'cost_first', 'latency_first', 'balanced')
    ),
    ADD CONSTRAINT route_strategy_thresholds_check CHECK (
        CASE
            WHEN thresholds IS NULL OR jsonb_typeof(thresholds) <> 'object' THEN false
            WHEN thresholds - ARRAY[
                'min_samples', 'target_success_rate', 'floor_success_rate',
                'max_ttft_p95_ms', 'max_duration_p95_ms',
                'consecutive_failure_limit'
            ] <> '{}'::jsonb THEN false
            WHEN jsonb_path_exists(thresholds, '$.* ? (@.type() != "number")') THEN false
            WHEN thresholds ? 'min_samples' AND (thresholds->>'min_samples') !~ '^(0|[1-9][0-9]*)$' THEN false
            WHEN thresholds ? 'consecutive_failure_limit' AND (thresholds->>'consecutive_failure_limit') !~ '^(0|[1-9][0-9]*)$' THEN false
            WHEN jsonb_path_exists(thresholds, '$.min_samples ? (@ > 9223372036854775807)') THEN false
            WHEN jsonb_path_exists(thresholds, '$.consecutive_failure_limit ? (@ > 9223372036854775807)') THEN false
            WHEN jsonb_path_exists(thresholds, '$.target_success_rate ? (@ < 0 || @ > 1)') THEN false
            WHEN jsonb_path_exists(thresholds, '$.floor_success_rate ? (@ < 0 || @ > 1)') THEN false
            WHEN jsonb_path_exists(thresholds, '$.max_ttft_p95_ms ? (@ < 0 || @ > 1.7976931348623157e308)') THEN false
            WHEN jsonb_path_exists(thresholds, '$.max_duration_p95_ms ? (@ < 0 || @ > 1.7976931348623157e308)') THEN false
            ELSE COALESCE(NULLIF((thresholds->>'floor_success_rate')::numeric, 0), 0.85)
                 <= COALESCE(NULLIF((thresholds->>'target_success_rate')::numeric, 0), 0.95)
        END
    ),
    ADD CONSTRAINT route_strategy_weights_check CHECK (
        CASE
            WHEN weights IS NULL OR jsonb_typeof(weights) <> 'object' THEN false
            WHEN weights - ARRAY['success', 'ttft', 'duration', 'stability', 'cost'] <> '{}'::jsonb THEN false
            WHEN jsonb_path_exists(weights, '$.* ? (@.type() != "number" || @ < 0 || @ > 1)') THEN false
            ELSE
                COALESCE((weights->>'success')::numeric, 0)
              + COALESCE((weights->>'ttft')::numeric, 0)
              + COALESCE((weights->>'duration')::numeric, 0)
              + COALESCE((weights->>'stability')::numeric, 0)
              + COALESCE((weights->>'cost')::numeric, 0) = 0
              OR abs(
                  COALESCE((weights->>'success')::numeric, 0)
                + COALESCE((weights->>'ttft')::numeric, 0)
                + COALESCE((weights->>'duration')::numeric, 0)
                + COALESCE((weights->>'stability')::numeric, 0)
                + COALESCE((weights->>'cost')::numeric, 0) - 1
              ) <= 0.001
        END
    ),
    ADD CONSTRAINT route_strategy_guard_range_check CHECK (
        cooldown_seconds IS NOT NULL AND
        recovery_observation_seconds IS NOT NULL AND
        max_auto_switches_per_hour IS NOT NULL AND
        cooldown_seconds BETWEEN 0 AND 604800 AND
        recovery_observation_seconds BETWEEN 0 AND 604800 AND
        max_auto_switches_per_hour BETWEEN 0 AND 3600
    );

COMMIT;

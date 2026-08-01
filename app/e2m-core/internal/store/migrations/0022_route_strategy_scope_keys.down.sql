BEGIN;

ALTER TABLE route_strategies
    DROP CONSTRAINT IF EXISTS route_strategy_guard_range_check,
    DROP CONSTRAINT IF EXISTS route_strategy_weights_check,
    DROP CONSTRAINT IF EXISTS route_strategy_thresholds_check,
    DROP CONSTRAINT IF EXISTS route_strategy_type_check,
    DROP CONSTRAINT IF EXISTS route_strategy_scope_owner_check;

COMMIT;

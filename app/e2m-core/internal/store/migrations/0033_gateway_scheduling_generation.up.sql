-- Connector scheduling fences use one stable plan scope. The plan row owns a
-- monotonic generation so status validation and ownership advance in the same
-- transaction as each automatic decision, repair, recovery, or manual apply.
ALTER TABLE route_plans
    ADD COLUMN IF NOT EXISTS scheduling_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE route_plans
    DROP CONSTRAINT IF EXISTS route_plan_scheduling_generation_check;
ALTER TABLE route_plans
    ADD CONSTRAINT route_plan_scheduling_generation_check
        CHECK (scheduling_generation >= 0);

ALTER TABLE auto_switch_decisions
    ADD COLUMN IF NOT EXISTS scheduling_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE auto_switch_decisions
    DROP CONSTRAINT IF EXISTS auto_switch_scheduling_generation_check;
ALTER TABLE auto_switch_decisions
    ADD CONSTRAINT auto_switch_scheduling_generation_check
        CHECK (scheduling_generation >= 0);

ALTER TABLE published_bindings
    ADD COLUMN IF NOT EXISTS scheduling_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE published_bindings
    DROP CONSTRAINT IF EXISTS published_binding_scheduling_generation_check;
ALTER TABLE published_bindings
    ADD CONSTRAINT published_binding_scheduling_generation_check
        CHECK (scheduling_generation >= 0);

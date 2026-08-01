ALTER TABLE published_bindings
    DROP CONSTRAINT IF EXISTS published_binding_scheduling_generation_check;
ALTER TABLE published_bindings
    DROP COLUMN IF EXISTS scheduling_generation;

ALTER TABLE auto_switch_decisions
    DROP CONSTRAINT IF EXISTS auto_switch_scheduling_generation_check;
ALTER TABLE auto_switch_decisions
    DROP COLUMN IF EXISTS scheduling_generation;

-- Keep route-plan generations across a binary downgrade. Dropping or resetting
-- them could make a later upgrade emit values below Connector disk watermarks.

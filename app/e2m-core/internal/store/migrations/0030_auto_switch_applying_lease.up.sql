-- Applying is a distributed claim around gateway side effects. Persist its
-- deadline so another scheduler can repair work abandoned by a crashed Core.
ALTER TABLE auto_switch_decisions
    ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_auto_switch_applying_lease
    ON auto_switch_decisions (lease_until, plan_id)
    WHERE status = 'applying' AND lease_until IS NOT NULL;

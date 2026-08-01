DROP INDEX IF EXISTS idx_auto_switch_applying_lease;

ALTER TABLE auto_switch_decisions
    DROP COLUMN IF EXISTS lease_until;

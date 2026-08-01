DROP INDEX IF EXISTS uq_auto_switch_active_fp;
CREATE UNIQUE INDEX uq_auto_switch_active_fp
    ON auto_switch_decisions (plan_id, fingerprint)
    WHERE status IN ('proposed', 'applying', 'observing');

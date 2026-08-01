-- Durable guarded recovery rollout for quality-isolated bindings. The
-- percentage is applied across downstream bindings of the same stable source;
-- gateways continue to receive only their native boolean schedulable command.
ALTER TABLE quality_circuit_runtimes
    ADD COLUMN IF NOT EXISTS recovery_ready BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS recovery_stage INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS recovery_stage_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS recovery_observe_after TIMESTAMPTZ;

ALTER TABLE quality_circuit_runtimes
    DROP CONSTRAINT IF EXISTS quality_circuit_recovery_stage_check;

ALTER TABLE quality_circuit_runtimes
    ADD CONSTRAINT quality_circuit_recovery_stage_check
    CHECK (recovery_stage IN (0, 10, 25, 50, 100));

CREATE INDEX IF NOT EXISTS idx_quality_circuit_recovery_rollout
    ON quality_circuit_runtimes (recovery_stage, recovery_observe_after, plan_id, channel_id)
    WHERE recovery_ready = TRUE;

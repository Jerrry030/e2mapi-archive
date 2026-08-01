DROP INDEX IF EXISTS idx_quality_circuit_recovery_rollout;

ALTER TABLE quality_circuit_runtimes
    DROP CONSTRAINT IF EXISTS quality_circuit_recovery_stage_check,
    DROP COLUMN IF EXISTS recovery_observe_after,
    DROP COLUMN IF EXISTS recovery_stage_started_at,
    DROP COLUMN IF EXISTS recovery_stage,
    DROP COLUMN IF EXISTS recovery_ready;

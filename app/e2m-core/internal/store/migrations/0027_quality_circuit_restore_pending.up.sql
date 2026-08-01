-- A recovery worker persists this intent before re-enabling a binding. If Core
-- stops after the gateway side effect, the next worker completes the durable
-- closed transition without issuing another probe or re-ejecting the binding.
ALTER TABLE quality_circuit_runtimes
    ADD COLUMN IF NOT EXISTS restore_pending BOOLEAN NOT NULL DEFAULT FALSE;

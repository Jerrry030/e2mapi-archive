ALTER TABLE onboarding_workflows
    ADD COLUMN IF NOT EXISTS desired_fingerprint TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS desired_generation BIGINT NOT NULL DEFAULT 1;

ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_desired_generation_check;
ALTER TABLE onboarding_workflows
    ADD CONSTRAINT onboarding_workflows_desired_generation_check
        CHECK (desired_generation > 0);

-- Existing active rows predate periodic verification. Make them immediately
-- due once so Core establishes a fingerprint and verifies remote state.
UPDATE onboarding_workflows
   SET next_attempt_at=statement_timestamp()
 WHERE status='active' AND next_attempt_at IS NULL;

ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_active_shape_check;
ALTER TABLE onboarding_workflows
    ADD CONSTRAINT onboarding_workflows_active_shape_check CHECK (
        status <> 'active' OR (stage = 'active' AND next_attempt_at IS NOT NULL)
    );

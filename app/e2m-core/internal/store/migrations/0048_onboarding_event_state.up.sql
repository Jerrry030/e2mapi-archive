ALTER TABLE onboarding_workflows
    ADD COLUMN IF NOT EXISTS last_ready_generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_ready_at TIMESTAMPTZ;

UPDATE onboarding_workflows
   SET last_ready_generation=desired_generation,
       last_ready_at=updated_at
 WHERE status='active' AND last_ready_generation=0;

ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_last_ready_generation_check;
ALTER TABLE onboarding_workflows
    ADD CONSTRAINT onboarding_workflows_last_ready_generation_check
        CHECK (last_ready_generation >= 0 AND last_ready_generation <= desired_generation);
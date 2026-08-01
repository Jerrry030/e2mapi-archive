ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_last_ready_generation_check,
    DROP COLUMN IF EXISTS last_ready_at,
    DROP COLUMN IF EXISTS last_ready_generation;
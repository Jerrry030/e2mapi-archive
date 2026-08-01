ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_active_shape_check;
ALTER TABLE onboarding_workflows
    ADD CONSTRAINT onboarding_workflows_active_shape_check CHECK (
        status <> 'active' OR stage = 'active'
    );

ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_desired_generation_check,
    DROP COLUMN IF EXISTS desired_generation,
    DROP COLUMN IF EXISTS desired_fingerprint;

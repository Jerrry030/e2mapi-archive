UPDATE onboarding_workflows
   SET stage='failed_retryable', status='retryable',
       next_attempt_at=statement_timestamp(), version=version+1,
       updated_at=statement_timestamp()
 WHERE status='dormant';

ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_dormant_shape_check,
    DROP CONSTRAINT IF EXISTS onboarding_workflows_stage_check,
    DROP CONSTRAINT IF EXISTS onboarding_workflows_status_check;

ALTER TABLE onboarding_workflows
    ADD CONSTRAINT onboarding_workflows_stage_check CHECK (stage IN (
        'waiting_connector', 'checking_gateway', 'assigning_keys',
        'delivering_bindings', 'publishing', 'verifying', 'active',
        'failed_retryable'
    )),
    ADD CONSTRAINT onboarding_workflows_status_check CHECK (status IN (
        'pending', 'running', 'retryable', 'active'
    ));

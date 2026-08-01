ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_stage_check,
    DROP CONSTRAINT IF EXISTS onboarding_workflows_status_check;

ALTER TABLE onboarding_workflows
    ADD CONSTRAINT onboarding_workflows_stage_check CHECK (stage IN (
        'waiting_connector', 'checking_gateway', 'assigning_keys',
        'delivering_bindings', 'publishing', 'verifying', 'active',
        'failed_retryable', 'dormant'
    )),
    ADD CONSTRAINT onboarding_workflows_status_check CHECK (status IN (
        'pending', 'running', 'retryable', 'active', 'dormant'
    ));

UPDATE onboarding_workflows
   SET stage='dormant', status='dormant', next_attempt_at=NULL,
       lease_owner='', lease_until=NULL, version=version+1,
       updated_at=statement_timestamp()
 WHERE status='retryable' AND last_error_code='pool_inactive';

ALTER TABLE onboarding_workflows
    DROP CONSTRAINT IF EXISTS onboarding_workflows_dormant_shape_check;
ALTER TABLE onboarding_workflows
    ADD CONSTRAINT onboarding_workflows_dormant_shape_check CHECK (
        status <> 'dormant'
        OR (stage='dormant' AND next_attempt_at IS NULL
            AND lease_owner='' AND lease_until IS NULL)
    );

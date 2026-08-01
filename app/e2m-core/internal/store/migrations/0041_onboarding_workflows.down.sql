ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;

-- The pre-0041 schema cannot represent binding-install tasks. Remove them
-- before restoring its closed task-type set, matching 0040's rollback policy
-- for gateway.binding.proof.
DELETE FROM connector_tasks WHERE type = 'gateway.binding.install';

ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_type_check CHECK (type IN (
        'gateway.health.get',
        'gateway.accounts.list',
        'gateway.account.quality.probe',
        'gateway.binding.proof',
        'gateway.account.schedulable.set',
        'gateway.account.switch',
        'gateway.scheduling.barrier',
        'gateway.account.create',
        'gateway.account.update',
        'gateway.account.delete'
    ));

DROP TABLE IF EXISTS onboarding_workflows;

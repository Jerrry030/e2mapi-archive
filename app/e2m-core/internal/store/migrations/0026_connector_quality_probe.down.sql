ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;

DELETE FROM connector_tasks
WHERE type = 'gateway.account.quality.probe';

ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_type_check CHECK (type IN (
        'gateway.health.get',
        'gateway.accounts.list',
        'gateway.account.schedulable.set',
        'gateway.account.switch'
    ));

ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;

-- The pre-0066 schema cannot represent numeric traffic-share or upstream
-- intelligence collection tasks. Remove them before restoring 0041's closed
-- task-type set.
DELETE FROM connector_tasks
WHERE type IN (
    'gateway.account.traffic_share.set',
    'upstream.intelligence.collect'
);

ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_type_check CHECK (type IN (
        'gateway.health.get',
        'gateway.accounts.list',
        'gateway.account.quality.probe',
        'gateway.binding.proof',
        'gateway.binding.install',
        'gateway.account.schedulable.set',
        'gateway.account.switch',
        'gateway.scheduling.barrier',
        'gateway.account.create',
        'gateway.account.update',
        'gateway.account.delete'
    ));

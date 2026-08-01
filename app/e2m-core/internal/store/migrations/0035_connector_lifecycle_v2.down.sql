DROP INDEX IF EXISTS idx_connector_tasks_connector_ready;

DELETE FROM connector_tasks
 WHERE type IN (
    'gateway.scheduling.barrier',
    'gateway.account.create',
    'gateway.account.update',
    'gateway.account.delete'
 );

ALTER TABLE connector_tasks DROP COLUMN IF EXISTS available_at;
ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_type_check CHECK (type IN (
        'gateway.health.get',
        'gateway.accounts.list',
        'gateway.account.quality.probe',
		'gateway.account.schedulable.set',
		'gateway.account.switch'
    ));

ALTER TABLE connectors
    DROP CONSTRAINT IF EXISTS connectors_protocol_version_check;
UPDATE connectors SET protocol_version = 1 WHERE protocol_version = 2;
ALTER TABLE connectors
    ADD CONSTRAINT connectors_protocol_version_check CHECK (protocol_version = 1);

ALTER TABLE published_bindings
    DROP CONSTRAINT IF EXISTS published_bindings_account_ownership_check;
ALTER TABLE published_bindings DROP COLUMN IF EXISTS account_ownership;

ALTER TABLE upstream_channels
    DROP CONSTRAINT IF EXISTS upstream_channels_account_ownership_check;
ALTER TABLE upstream_channels DROP COLUMN IF EXISTS account_ownership;

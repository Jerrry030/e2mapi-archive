ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;

DELETE FROM connector_tasks WHERE type = 'gateway.binding.proof';

ALTER TABLE connector_tasks
	ADD CONSTRAINT connector_tasks_type_check CHECK (type IN (
        'gateway.health.get',
        'gateway.accounts.list',
        'gateway.account.quality.probe',
        'gateway.account.schedulable.set',
        'gateway.account.switch',
        'gateway.scheduling.barrier',
        'gateway.account.create',
        'gateway.account.update',
		'gateway.account.delete'
	));

DROP INDEX IF EXISTS idx_upstream_key_deployments_instance;
DROP TABLE IF EXISTS upstream_key_deployments;

ALTER TABLE upstream_key_deliveries
    DROP CONSTRAINT IF EXISTS upstream_key_deliveries_key_version_check,
    DROP CONSTRAINT IF EXISTS upstream_key_deliveries_proof_status_check,
    DROP COLUMN IF EXISTS proof_checked_at,
    DROP COLUMN IF EXISTS proof_connector_id,
    DROP COLUMN IF EXISTS proof_status,
    DROP COLUMN IF EXISTS key_version;

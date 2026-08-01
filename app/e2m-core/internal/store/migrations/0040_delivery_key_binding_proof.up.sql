-- A random-challenge HMAC proves that a Connector-local credential binding
-- contains the same key as the Core delivery secret without either side
-- disclosing its plaintext to the other.
ALTER TABLE upstream_key_deliveries
    ADD COLUMN IF NOT EXISTS key_version BIGINT NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS proof_status TEXT NOT NULL DEFAULT 'unverified',
    ADD COLUMN IF NOT EXISTS proof_connector_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS proof_checked_at TIMESTAMPTZ;

ALTER TABLE upstream_key_deliveries
    DROP CONSTRAINT IF EXISTS upstream_key_deliveries_key_version_check,
    DROP CONSTRAINT IF EXISTS upstream_key_deliveries_proof_status_check;

ALTER TABLE upstream_key_deliveries
	ADD CONSTRAINT upstream_key_deliveries_key_version_check CHECK (key_version > 0),
	ADD CONSTRAINT upstream_key_deliveries_proof_status_check
		CHECK (proof_status IN ('unverified', 'verified', 'mismatch'));

CREATE TABLE IF NOT EXISTS upstream_key_deployments (
    channel_id   TEXT NOT NULL REFERENCES upstream_key_deliveries(channel_id) ON DELETE CASCADE,
    instance_id  TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    key_version  BIGINT NOT NULL CHECK (key_version > 0),
    connector_id TEXT NOT NULL CHECK (connector_id <> ''),
    status        TEXT NOT NULL CHECK (status IN ('pending', 'deployed', 'failed')),
    deployed_at   TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, instance_id),
    CHECK ((status = 'deployed' AND deployed_at IS NOT NULL) OR
           (status <> 'deployed' AND deployed_at IS NULL))
);

CREATE INDEX IF NOT EXISTS idx_upstream_key_deployments_instance
    ON upstream_key_deployments (instance_id, status);

ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;
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

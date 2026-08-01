-- Connector protocol v2 executes typed remote account lifecycle operations.
-- Ownership is immutable domain state: owner-provided accounts are update-only.

ALTER TABLE upstream_channels
    ADD COLUMN IF NOT EXISTS account_ownership TEXT NOT NULL DEFAULT 'owner_provided';
-- Before protocol v2 Core could not prove that it created a remote account.
-- Treat every existing row as owner-provided so migration never grants itself
-- create/delete authority over historical gateway state.
UPDATE upstream_channels SET account_ownership = 'owner_provided';
ALTER TABLE upstream_channels
    ALTER COLUMN account_ownership SET DEFAULT 'platform_managed';
ALTER TABLE upstream_channels
    DROP CONSTRAINT IF EXISTS upstream_channels_account_ownership_check;
ALTER TABLE upstream_channels
    ADD CONSTRAINT upstream_channels_account_ownership_check
    CHECK (account_ownership IN ('platform_managed', 'owner_provided'));

ALTER TABLE published_bindings
    ADD COLUMN IF NOT EXISTS account_ownership TEXT NOT NULL DEFAULT 'platform_managed';
UPDATE published_bindings pb
   SET account_ownership = uc.account_ownership
  FROM upstream_channels uc
 WHERE uc.id = pb.channel_id;
ALTER TABLE published_bindings
    DROP CONSTRAINT IF EXISTS published_bindings_account_ownership_check;
ALTER TABLE published_bindings
    ADD CONSTRAINT published_bindings_account_ownership_check
    CHECK (account_ownership IN ('platform_managed', 'owner_provided'));

ALTER TABLE connectors
    DROP CONSTRAINT IF EXISTS connectors_protocol_version_check;
UPDATE connectors SET protocol_version = 2 WHERE protocol_version = 1;
ALTER TABLE connectors
    ADD CONSTRAINT connectors_protocol_version_check CHECK (protocol_version = 2);

ALTER TABLE connector_tasks
    ADD COLUMN IF NOT EXISTS available_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_type_check;
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

CREATE INDEX IF NOT EXISTS idx_connector_tasks_connector_ready
    ON connector_tasks (connector_id, status, available_at, created_at);

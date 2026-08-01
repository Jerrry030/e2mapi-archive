BEGIN;

-- E2M is the only product and identity boundary. Platform keys bind directly
-- to an E2M distribution group (the supply-gateway UpstreamPool), not to a
-- Connector-managed customer instance.
ALTER TABLE virtual_keys
    ADD COLUMN IF NOT EXISTS group_id TEXT REFERENCES upstream_pools(id);
ALTER TABLE virtual_keys
    DROP CONSTRAINT IF EXISTS virtual_keys_instance_id_fkey;
ALTER TABLE virtual_keys
    ADD CONSTRAINT virtual_keys_instance_id_fkey
        FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE;
ALTER TABLE virtual_keys
    ALTER COLUMN instance_id DROP NOT NULL;
ALTER TABLE virtual_keys
    DROP CONSTRAINT IF EXISTS virtual_keys_platform_scope_check;
ALTER TABLE virtual_keys
    ADD CONSTRAINT virtual_keys_platform_scope_check CHECK (
        (group_id IS NOT NULL AND instance_id IS NULL) OR
        (group_id IS NULL AND instance_id IS NOT NULL)
    );
CREATE UNIQUE INDEX IF NOT EXISTS uq_virtual_keys_group_name
    ON virtual_keys(group_id,name) WHERE group_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_virtual_keys_group_enabled
    ON virtual_keys(group_id,enabled) WHERE group_id IS NOT NULL;

ALTER TABLE supply_usage_records
    ADD COLUMN IF NOT EXISTS group_id TEXT REFERENCES upstream_pools(id);
ALTER TABLE supply_usage_records
    DROP CONSTRAINT IF EXISTS supply_usage_records_instance_id_fkey;
ALTER TABLE supply_usage_records
    ADD CONSTRAINT supply_usage_records_instance_id_fkey
        FOREIGN KEY (instance_id) REFERENCES instances(id);
ALTER TABLE supply_usage_records
    ALTER COLUMN instance_id DROP NOT NULL;
ALTER TABLE supply_usage_records
    DROP CONSTRAINT IF EXISTS supply_usage_records_platform_scope_check;
ALTER TABLE supply_usage_records
    ADD CONSTRAINT supply_usage_records_platform_scope_check CHECK (
        (group_id IS NOT NULL AND instance_id IS NULL) OR
        (group_id IS NULL AND instance_id IS NOT NULL)
    );
CREATE INDEX IF NOT EXISTS idx_supply_usage_group_created
    ON supply_usage_records(group_id,created_at DESC) WHERE group_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_supply_usage_user_created
    ON supply_usage_records(user_id,created_at DESC);

-- Price snapshots make settlement independent of a concurrent operator price
-- edit. They are captured at reservation time and are immutable afterwards.
ALTER TABLE supply_usage_records
    ADD COLUMN IF NOT EXISTS input_price_micros_per_million BIGINT NOT NULL DEFAULT 0
        CHECK (input_price_micros_per_million >= 0),
    ADD COLUMN IF NOT EXISTS output_price_micros_per_million BIGINT NOT NULL DEFAULT 0
        CHECK (output_price_micros_per_million >= 0),
    ADD COLUMN IF NOT EXISTS input_supplier_micros_per_million BIGINT NOT NULL DEFAULT 0
        CHECK (input_supplier_micros_per_million >= 0),
    ADD COLUMN IF NOT EXISTS output_supplier_micros_per_million BIGINT NOT NULL DEFAULT 0
        CHECK (output_supplier_micros_per_million >= 0);

ALTER TABLE supply_channel_endpoints
    ADD COLUMN IF NOT EXISTS allow_insecure BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE wallet_journals
    DROP CONSTRAINT IF EXISTS wallet_journals_kind_check;
ALTER TABLE wallet_journals
    ADD CONSTRAINT wallet_journals_kind_check
    CHECK (kind IN ('recharge','adjustment','reserve','settle','release','refund'));

COMMIT;

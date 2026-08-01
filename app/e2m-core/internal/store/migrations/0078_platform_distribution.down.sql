BEGIN;

-- Fail closed when platform-native rows exist. Operators must migrate those
-- rows explicitly before attempting a downgrade; this migration never deletes
-- balances, keys, or usage records implicitly.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM virtual_keys WHERE group_id IS NOT NULL) OR
       EXISTS (SELECT 1 FROM supply_usage_records WHERE group_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot downgrade 0078 while E2M platform distribution data exists';
    END IF;
END;
$$;

ALTER TABLE wallet_journals DROP CONSTRAINT IF EXISTS wallet_journals_kind_check;
ALTER TABLE wallet_journals ADD CONSTRAINT wallet_journals_kind_check
    CHECK (kind IN ('recharge','reserve','settle','release','refund'));

ALTER TABLE supply_channel_endpoints DROP COLUMN IF EXISTS allow_insecure;

DROP INDEX IF EXISTS idx_supply_usage_user_created;
DROP INDEX IF EXISTS idx_supply_usage_group_created;
ALTER TABLE supply_usage_records DROP CONSTRAINT IF EXISTS supply_usage_records_platform_scope_check;
ALTER TABLE supply_usage_records DROP CONSTRAINT IF EXISTS supply_usage_records_instance_id_fkey;
ALTER TABLE supply_usage_records ALTER COLUMN instance_id SET NOT NULL;
ALTER TABLE supply_usage_records ADD CONSTRAINT supply_usage_records_instance_id_fkey
    FOREIGN KEY (instance_id) REFERENCES instances(id);
ALTER TABLE supply_usage_records
    DROP COLUMN IF EXISTS input_price_micros_per_million,
    DROP COLUMN IF EXISTS output_price_micros_per_million,
    DROP COLUMN IF EXISTS input_supplier_micros_per_million,
    DROP COLUMN IF EXISTS output_supplier_micros_per_million,
    DROP COLUMN IF EXISTS group_id;

DROP INDEX IF EXISTS idx_virtual_keys_group_enabled;
DROP INDEX IF EXISTS uq_virtual_keys_group_name;
ALTER TABLE virtual_keys DROP CONSTRAINT IF EXISTS virtual_keys_platform_scope_check;
ALTER TABLE virtual_keys DROP CONSTRAINT IF EXISTS virtual_keys_instance_id_fkey;
ALTER TABLE virtual_keys ALTER COLUMN instance_id SET NOT NULL;
ALTER TABLE virtual_keys ADD CONSTRAINT virtual_keys_instance_id_fkey
    FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE;
ALTER TABLE virtual_keys DROP COLUMN IF EXISTS group_id;

COMMIT;

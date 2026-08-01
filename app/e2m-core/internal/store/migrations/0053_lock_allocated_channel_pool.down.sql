DROP TRIGGER IF EXISTS trg_lock_allocated_channel_pool ON upstream_channels;
DROP FUNCTION IF EXISTS e2m_lock_allocated_channel_pool();
DROP TRIGGER IF EXISTS trg_lock_allocated_inventory_retirement ON upstream_channels;
DROP FUNCTION IF EXISTS e2m_lock_allocated_inventory_retirement();
DROP TRIGGER IF EXISTS trg_lock_pool_retirement ON upstream_pools;
DROP FUNCTION IF EXISTS e2m_lock_pool_retirement();

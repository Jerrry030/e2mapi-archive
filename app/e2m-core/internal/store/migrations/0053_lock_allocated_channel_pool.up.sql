-- An allocated key may only cross pools through the explicit migration API.
-- That API sets the transaction-local e2m.channel_pool_migration flag and
-- appends upstream_channel_migrations in the same transaction.
CREATE OR REPLACE FUNCTION e2m_lock_allocated_channel_pool()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.pool_id IS DISTINCT FROM OLD.pool_id
       AND EXISTS (SELECT 1 FROM upstream_channel_allocations WHERE channel_id=OLD.id)
       AND COALESCE(current_setting('e2m.channel_pool_migration', true), '') <> 'allowed'
    THEN
        RAISE EXCEPTION 'pool_id is locked after channel allocation; use explicit migration'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_lock_allocated_channel_pool ON upstream_channels;
CREATE TRIGGER trg_lock_allocated_channel_pool
BEFORE UPDATE OF pool_id ON upstream_channels
FOR EACH ROW EXECUTE FUNCTION e2m_lock_allocated_channel_pool();

CREATE OR REPLACE FUNCTION e2m_lock_allocated_inventory_retirement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.inventory_state = 'retired'
       AND OLD.inventory_state IS DISTINCT FROM 'retired'
       AND EXISTS (SELECT 1 FROM upstream_channel_allocations WHERE channel_id=OLD.id)
    THEN
        RAISE EXCEPTION 'allocated inventory cannot be retired before explicit withdrawal completes'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_lock_allocated_inventory_retirement ON upstream_channels;
CREATE TRIGGER trg_lock_allocated_inventory_retirement
BEFORE UPDATE OF inventory_state ON upstream_channels
FOR EACH ROW EXECUTE FUNCTION e2m_lock_allocated_inventory_retirement();

CREATE OR REPLACE FUNCTION e2m_lock_pool_retirement()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status = 'retired'
       AND OLD.status IS DISTINCT FROM 'retired'
       AND COALESCE(current_setting('e2m.pool_retirement_job', true), '') <> 'allowed'
    THEN
        RAISE EXCEPTION 'pool retirement requires a durable retirement job'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_lock_pool_retirement ON upstream_pools;
CREATE TRIGGER trg_lock_pool_retirement
BEFORE UPDATE OF status ON upstream_pools
FOR EACH ROW EXECUTE FUNCTION e2m_lock_pool_retirement();

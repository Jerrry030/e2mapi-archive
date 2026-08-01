BEGIN;

-- Removing the explicit identity would make an older Core treat an automatic
-- mutation as ordinary work. Invalidate every still-executable plan-scoped
-- task before the columns and enforcement trigger disappear.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM connector_tasks
         WHERE plan_id IS NOT NULL
           AND status = 'executing'
    ) THEN
        RAISE EXCEPTION 'cannot downgrade while a route-plan connector task is executing';
    END IF;
END;
$$;

UPDATE connector_tasks
   SET status = 'failed',
       result = 'null'::jsonb,
       error = '{"code":"scheduling_fence_stale"}'::jsonb,
       lease_owner = '',
       lease_nonce = '',
       lease_until = NULL,
       updated_at = statement_timestamp()
 WHERE plan_id IS NOT NULL
   AND status IN ('pending', 'leased');

DROP TRIGGER IF EXISTS trg_guard_route_plan_executing_connector_task ON route_plans;
DROP FUNCTION IF EXISTS guard_route_plan_executing_connector_task();

DROP TRIGGER IF EXISTS trg_enforce_connector_task_route_plan_fence ON connector_tasks;
DROP FUNCTION IF EXISTS enforce_connector_task_route_plan_fence();

DROP INDEX IF EXISTS idx_connector_tasks_plan_generation_active;

ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_plan_owner_fkey;
ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_plan_generation_pair_check;

ALTER TABLE connector_tasks
    DROP COLUMN IF EXISTS scheduling_generation,
    DROP COLUMN IF EXISTS plan_id;

-- Restore the exact pre-v3 state contract only after every route-plan task is
-- unable to execute. Non-plan executing tasks are also unsafe for an older
-- binary, so fail the down rather than silently changing their outcome.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM connector_tasks WHERE status = 'executing') THEN
        RAISE EXCEPTION 'cannot downgrade while a connector task is executing';
    END IF;
END;
$$;
ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_status_check;
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_status_check CHECK (
        status IN ('pending', 'leased', 'succeeded', 'failed', 'expired')
    );
DROP INDEX IF EXISTS uq_connector_tasks_idempotency;
CREATE UNIQUE INDEX uq_connector_tasks_idempotency
    ON connector_tasks (connector_id, idempotency_key)
    WHERE idempotency_key <> '' AND status IN ('pending', 'leased');

ALTER TABLE connectors
    DROP CONSTRAINT IF EXISTS connectors_protocol_version_check;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM connectors WHERE protocol_version = 3) THEN
        RAISE EXCEPTION 'cannot downgrade while a protocol v3 connector exists';
    END IF;
END;
$$;
ALTER TABLE connectors
    ADD CONSTRAINT connectors_protocol_version_check CHECK (protocol_version = 2);

COMMIT;

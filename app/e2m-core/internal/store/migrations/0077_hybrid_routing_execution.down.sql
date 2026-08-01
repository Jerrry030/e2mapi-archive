BEGIN;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM connector_tasks WHERE execution_scope='hybrid_routing' AND status='executing') THEN
        RAISE EXCEPTION 'cannot downgrade while a hybrid routing connector task is executing';
    END IF;
END;
$$;

UPDATE connector_tasks SET status='failed',result='null'::jsonb,error='{"code":"scheduling_fence_stale"}'::jsonb,
    lease_owner='',lease_nonce='',lease_until=NULL,updated_at=statement_timestamp()
WHERE execution_scope='hybrid_routing' AND status IN ('pending','leased');

DROP TRIGGER IF EXISTS trg_guard_hybrid_allocation_generation_connector_task ON hybrid_allocations;
DROP FUNCTION IF EXISTS guard_hybrid_allocation_generation_connector_task();
DROP TRIGGER IF EXISTS trg_guard_hybrid_routing_execution_connector_task ON hybrid_routing_executions;
DROP FUNCTION IF EXISTS guard_hybrid_routing_execution_connector_task();

DROP TRIGGER IF EXISTS trg_enforce_connector_task_route_plan_fence ON connector_tasks;
DROP FUNCTION IF EXISTS enforce_connector_task_route_plan_fence();

ALTER TABLE connector_tasks DROP CONSTRAINT IF EXISTS connector_tasks_hybrid_execution_owner_fkey;
DROP INDEX IF EXISTS idx_connector_tasks_hybrid_execution_active;
ALTER TABLE connector_tasks DROP CONSTRAINT IF EXISTS connector_tasks_route_or_execution_identity_check;
ALTER TABLE connector_tasks DROP CONSTRAINT IF EXISTS connector_tasks_execution_identity_check;
ALTER TABLE connector_tasks DROP COLUMN IF EXISTS execution_generation,
    DROP COLUMN IF EXISTS execution_id,DROP COLUMN IF EXISTS execution_scope;

DROP TABLE IF EXISTS hybrid_routing_executions;
DROP TABLE IF EXISTS hybrid_gateway_bindings;
ALTER TABLE hybrid_allocations DROP COLUMN IF EXISTS routing_generation;
ALTER TABLE virtual_keys DROP CONSTRAINT IF EXISTS virtual_keys_hybrid_binding_identity_key;
ALTER TABLE virtual_keys DROP COLUMN IF EXISTS key_version;

-- Restore 0074's RoutePlan-only trigger before committing the downgrade. A
-- migration rollback must leave the previous schema executable; it cannot
-- depend on re-applying an already-recorded historical migration.
CREATE OR REPLACE FUNCTION enforce_connector_task_route_plan_fence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    fence_scope TEXT;
    fence_version BIGINT;
    fence_sequence BIGINT;
    plan_user_id BIGINT;
    plan_instance_id TEXT;
    plan_generation BIGINT;
BEGIN
    IF TG_OP='UPDATE' AND (
        NEW.user_id IS DISTINCT FROM OLD.user_id OR NEW.instance_id IS DISTINCT FROM OLD.instance_id OR
        NEW.connector_id IS DISTINCT FROM OLD.connector_id OR NEW.type IS DISTINCT FROM OLD.type OR
        NEW.schema_version IS DISTINCT FROM OLD.schema_version OR NEW.input IS DISTINCT FROM OLD.input OR
        NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR NEW.plan_id IS DISTINCT FROM OLD.plan_id OR
        NEW.scheduling_generation IS DISTINCT FROM OLD.scheduling_generation
    ) THEN
        RAISE EXCEPTION 'connector task route plan execution identity is immutable' USING ERRCODE='23514';
    END IF;
    IF TG_OP='UPDATE' AND NOT (NEW.status='executing' AND OLD.status IS DISTINCT FROM 'executing') THEN RETURN NEW; END IF;
    IF NEW.type NOT IN (
        'gateway.account.schedulable.set','gateway.account.traffic_share.set','gateway.account.switch',
        'gateway.scheduling.barrier','gateway.account.create','gateway.account.update','gateway.account.delete'
    ) THEN
        IF NEW.plan_id IS NOT NULL OR NEW.scheduling_generation IS NOT NULL THEN
            RAISE EXCEPTION 'read-only connector task cannot carry route plan identity' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.input #> '{spec,fence}' IS NOT NULL THEN
        RAISE EXCEPTION 'connector task scheduling fence must be top-level' USING ERRCODE='23514';
    END IF;
    IF NOT (NEW.input ? 'fence') OR NEW.input->'fence'='null'::jsonb THEN
        IF NEW.type IN ('gateway.account.traffic_share.set','gateway.scheduling.barrier') THEN
            RAISE EXCEPTION 'connector task type requires a scheduling fence' USING ERRCODE='23514';
        END IF;
        IF NEW.plan_id IS NOT NULL OR NEW.scheduling_generation IS NOT NULL THEN
            RAISE EXCEPTION 'connector task plan identity requires an input scheduling fence' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF jsonb_typeof(NEW.input)<>'object' OR jsonb_typeof(NEW.input->'fence')<>'object' OR
       jsonb_typeof(NEW.input#>'{fence,scope}')<>'string' OR jsonb_typeof(NEW.input#>'{fence,version}')<>'number' OR
       jsonb_typeof(NEW.input#>'{fence,sequence}')<>'number' THEN
        RAISE EXCEPTION 'connector task scheduling fence shape is invalid' USING ERRCODE='23514';
    END IF;
    fence_scope := NULLIF(BTRIM(NEW.input#>>'{fence,scope}'),'');
    BEGIN
        fence_version := NULLIF(BTRIM(NEW.input#>>'{fence,version}'),'')::BIGINT;
        fence_sequence := NULLIF(BTRIM(NEW.input#>>'{fence,sequence}'),'')::BIGINT;
    EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RAISE EXCEPTION 'connector task scheduling fence number is invalid' USING ERRCODE='23514';
    END;
    IF fence_scope IS NULL OR fence_version IS NULL OR fence_version<=0 OR fence_sequence IS NULL OR fence_sequence<=0 OR
       NEW.plan_id IS NULL OR NEW.scheduling_generation IS NULL THEN
        RAISE EXCEPTION 'connector task scheduling fence identity is incomplete' USING ERRCODE='23514';
    END IF;
    IF fence_scope NOT IN (
        'auto-switch/plan/'||NEW.plan_id,'recommendation/rollout/'||NEW.plan_id,'recommendation-rollout/'||NEW.plan_id
    ) OR fence_version<>NEW.scheduling_generation THEN
        RAISE EXCEPTION 'connector task scheduling fence does not match its route plan identity' USING ERRCODE='23514';
    END IF;
    SELECT user_id,instance_id,scheduling_generation INTO plan_user_id,plan_instance_id,plan_generation
      FROM route_plans WHERE id=NEW.plan_id FOR SHARE;
    IF NOT FOUND OR plan_user_id<>NEW.user_id OR plan_instance_id<>NEW.instance_id OR
       plan_generation<>NEW.scheduling_generation THEN
        RAISE EXCEPTION 'connector task scheduling fence is not the current route plan generation' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER trg_enforce_connector_task_route_plan_fence
BEFORE INSERT OR UPDATE OF user_id,instance_id,connector_id,type,schema_version,status,input,idempotency_key,
    plan_id,scheduling_generation
ON connector_tasks FOR EACH ROW EXECUTE FUNCTION enforce_connector_task_route_plan_fence();

COMMIT;

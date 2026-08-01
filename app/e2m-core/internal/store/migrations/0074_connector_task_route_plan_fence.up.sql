BEGIN;

-- A leased v2 mutation may already be executing remotely without protocol
-- v3's final Core permit. Neither lease expiry nor a well-formed JSON fence can
-- prove that the side effect did not happen. Stop the upgrade atomically and
-- require the operator to stop v2 Connectors, reconcile the gateway outcome,
-- and explicitly terminalize or requeue every such task first.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
          FROM connector_tasks
         WHERE status = 'leased'
           AND type IN (
               'gateway.account.schedulable.set',
               'gateway.account.traffic_share.set',
               'gateway.account.switch',
               'gateway.scheduling.barrier',
               'gateway.account.create',
               'gateway.account.update',
               'gateway.account.delete'
           )
           AND (
               type IN ('gateway.account.traffic_share.set', 'gateway.scheduling.barrier') OR
               (input ? 'fence' AND input -> 'fence' <> 'null'::jsonb) OR
               input #> '{spec,fence}' IS NOT NULL
           )
    ) THEN
        RAISE EXCEPTION 'cannot upgrade while a protocol v2 fenced connector task is leased'
            USING ERRCODE = '23514';
    END IF;
END;
$$;

-- Protocol negotiation and the task-state machine advance atomically. A v2
-- Connector must never lease v3 work because it has no execution-permit edge.
ALTER TABLE connectors
    DROP CONSTRAINT IF EXISTS connectors_protocol_version_check;
ALTER TABLE connectors
    ADD CONSTRAINT connectors_protocol_version_check CHECK (protocol_version IN (2, 3));

-- Persist the route-plan ordering identity beside the opaque task payload. The
-- pair is nullable for read-only work and explicitly manual gateway writes.
ALTER TABLE connector_tasks
    ADD COLUMN IF NOT EXISTS plan_id TEXT,
    ADD COLUMN IF NOT EXISTS scheduling_generation BIGINT;

-- Protocol v3 adds a durable execution permit between lease and completion.
-- Replace both legacy indexes so an executing task continues to reserve its
-- idempotency key and remains visible to route-plan generation fencing.
ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_status_check;
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_status_check CHECK (
        status IN ('pending', 'leased', 'executing', 'succeeded', 'failed', 'expired')
    );
DROP INDEX IF EXISTS uq_connector_tasks_idempotency;
CREATE UNIQUE INDEX uq_connector_tasks_idempotency
    ON connector_tasks (connector_id, idempotency_key)
    WHERE idempotency_key <> '' AND status IN ('pending', 'leased', 'executing');

-- A pre-0074 queue may already contain automatic mutations. Recover only an
-- exact, typed top-level fence that still names the task owner's current plan
-- generation. Everything else that claims to be fenced is failed below before
-- a Connector can lease it.
CREATE OR REPLACE FUNCTION parse_connector_task_route_plan_fence_bigint(value TEXT)
RETURNS BIGINT
LANGUAGE plpgsql
IMMUTABLE
STRICT
AS $$
DECLARE
    parsed BIGINT;
BEGIN
    parsed := NULLIF(BTRIM(value), '')::BIGINT;
    RETURN parsed;
EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
    RETURN NULL;
END;
$$;

WITH active_fenced_mutations AS (
    SELECT task.id,
           CASE
               WHEN NULLIF(BTRIM(task.input #>> '{fence,scope}'), '') LIKE 'auto-switch/plan/%'
                   THEN SUBSTRING(NULLIF(BTRIM(task.input #>> '{fence,scope}'), '') FROM CHAR_LENGTH('auto-switch/plan/') + 1)
               WHEN NULLIF(BTRIM(task.input #>> '{fence,scope}'), '') LIKE 'recommendation/rollout/%'
                   THEN SUBSTRING(NULLIF(BTRIM(task.input #>> '{fence,scope}'), '') FROM CHAR_LENGTH('recommendation/rollout/') + 1)
               WHEN NULLIF(BTRIM(task.input #>> '{fence,scope}'), '') LIKE 'recommendation-rollout/%'
                   THEN SUBSTRING(NULLIF(BTRIM(task.input #>> '{fence,scope}'), '') FROM CHAR_LENGTH('recommendation-rollout/') + 1)
           END AS parsed_plan_id,
           NULLIF(BTRIM(task.input #>> '{fence,scope}'), '') AS fence_scope,
           parse_connector_task_route_plan_fence_bigint(task.input #>> '{fence,version}') AS fence_version,
           parse_connector_task_route_plan_fence_bigint(task.input #>> '{fence,sequence}') AS fence_sequence,
           task.user_id,
           task.instance_id
      FROM connector_tasks AS task
     WHERE task.status = 'pending'
       AND task.type IN (
           'gateway.account.schedulable.set',
           'gateway.account.traffic_share.set',
           'gateway.account.switch',
           'gateway.scheduling.barrier',
           'gateway.account.create',
           'gateway.account.update',
           'gateway.account.delete'
       )
       AND jsonb_typeof(task.input) = 'object'
       AND jsonb_typeof(task.input -> 'fence') = 'object'
       AND jsonb_typeof(task.input #> '{fence,scope}') = 'string'
       AND jsonb_typeof(task.input #> '{fence,version}') = 'number'
       AND jsonb_typeof(task.input #> '{fence,sequence}') = 'number'
       AND task.input #> '{spec,fence}' IS NULL
), recoverable AS (
    SELECT candidate.id,
           plan.id AS plan_id,
           candidate.fence_version AS scheduling_generation
      FROM active_fenced_mutations AS candidate
      JOIN route_plans AS plan
        ON plan.id = candidate.parsed_plan_id
       AND plan.user_id = candidate.user_id
       AND plan.instance_id = candidate.instance_id
       AND plan.scheduling_generation = candidate.fence_version
     WHERE candidate.parsed_plan_id IS NOT NULL
       AND candidate.parsed_plan_id <> ''
       AND candidate.parsed_plan_id !~ '/'
       AND candidate.fence_version > 0
       AND candidate.fence_sequence > 0
       AND candidate.fence_scope IN (
           'auto-switch/plan/' || plan.id,
           'recommendation/rollout/' || plan.id,
           'recommendation-rollout/' || plan.id
       )
)
UPDATE connector_tasks AS task
   SET plan_id = recoverable.plan_id,
       scheduling_generation = recoverable.scheduling_generation
  FROM recoverable
 WHERE task.id = recoverable.id;

-- Optional mutation fences are absent (or JSON null) for a genuinely manual
-- task. A non-null top-level fence, any nested spec.fence, and both mandatory
-- fence task types claim automatic ordering and therefore fail closed unless
-- the exact current identity was recovered above.
UPDATE connector_tasks
   SET status = 'failed',
       result = 'null'::jsonb,
       error = '{"code":"scheduling_fence_stale"}'::jsonb,
       lease_owner = '',
       lease_nonce = '',
       lease_until = NULL,
       updated_at = statement_timestamp()
 WHERE status = 'pending'
   AND plan_id IS NULL
   AND type IN (
       'gateway.account.schedulable.set',
       'gateway.account.traffic_share.set',
       'gateway.account.switch',
       'gateway.scheduling.barrier',
       'gateway.account.create',
       'gateway.account.update',
       'gateway.account.delete'
   )
   AND (
       type IN ('gateway.account.traffic_share.set', 'gateway.scheduling.barrier') OR
       (input ? 'fence' AND input -> 'fence' <> 'null'::jsonb) OR
       input #> '{spec,fence}' IS NOT NULL
   );

DROP FUNCTION parse_connector_task_route_plan_fence_bigint(TEXT);

ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_plan_generation_pair_check;
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_plan_generation_pair_check CHECK (
        (plan_id IS NULL AND scheduling_generation IS NULL) OR
        (NULLIF(BTRIM(plan_id), '') IS NOT NULL AND scheduling_generation > 0)
    );

ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_plan_owner_fkey;
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_plan_owner_fkey
        FOREIGN KEY (plan_id, user_id)
        REFERENCES route_plans(id, user_id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_connector_tasks_plan_generation_active
    ON connector_tasks (plan_id, scheduling_generation, status)
    WHERE plan_id IS NOT NULL AND status IN ('pending', 'leased', 'executing');

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
    IF TG_OP = 'UPDATE' AND (
        NEW.user_id IS DISTINCT FROM OLD.user_id OR
        NEW.instance_id IS DISTINCT FROM OLD.instance_id OR
        NEW.connector_id IS DISTINCT FROM OLD.connector_id OR
        NEW.type IS DISTINCT FROM OLD.type OR
        NEW.schema_version IS DISTINCT FROM OLD.schema_version OR
        NEW.input IS DISTINCT FROM OLD.input OR
        NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR
        NEW.plan_id IS DISTINCT FROM OLD.plan_id OR
        NEW.scheduling_generation IS DISTINCT FROM OLD.scheduling_generation
    ) THEN
        RAISE EXCEPTION 'connector task route plan execution identity is immutable'
            USING ERRCODE = '23514';
    END IF;

    -- Ordinary lease, retry, completion, expiry, and supersede transitions do
    -- not redefine execution identity. Entering executing is the one status
    -- edge that must re-prove the current plan generation immediately before
    -- the Connector receives its durable side-effect permit.
    IF TG_OP = 'UPDATE' AND
       NOT (NEW.status = 'executing' AND OLD.status IS DISTINCT FROM 'executing') THEN
        RETURN NEW;
    END IF;

    IF NEW.type NOT IN (
        'gateway.account.schedulable.set',
        'gateway.account.traffic_share.set',
        'gateway.account.switch',
        'gateway.scheduling.barrier',
        'gateway.account.create',
        'gateway.account.update',
        'gateway.account.delete'
    ) THEN
        IF NEW.plan_id IS NOT NULL OR NEW.scheduling_generation IS NOT NULL THEN
            RAISE EXCEPTION 'read-only connector task cannot carry route plan identity'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    -- Account create/update put fence beside spec. Accepting spec.fence would
    -- create a database identity that both Core and Connector silently ignore.
    IF NEW.input #> '{spec,fence}' IS NOT NULL THEN
        RAISE EXCEPTION 'connector task scheduling fence must be top-level'
            USING ERRCODE = '23514';
    END IF;

    IF NOT (NEW.input ? 'fence') OR NEW.input -> 'fence' = 'null'::jsonb THEN
        IF NEW.type IN ('gateway.account.traffic_share.set', 'gateway.scheduling.barrier') THEN
            RAISE EXCEPTION 'connector task type requires a scheduling fence'
                USING ERRCODE = '23514';
        END IF;
        IF NEW.plan_id IS NOT NULL OR NEW.scheduling_generation IS NOT NULL THEN
            RAISE EXCEPTION 'connector task plan identity requires an input scheduling fence'
                USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;

    IF jsonb_typeof(NEW.input) <> 'object' OR
       jsonb_typeof(NEW.input -> 'fence') <> 'object' OR
       jsonb_typeof(NEW.input #> '{fence,scope}') <> 'string' OR
       jsonb_typeof(NEW.input #> '{fence,version}') <> 'number' OR
       jsonb_typeof(NEW.input #> '{fence,sequence}') <> 'number' THEN
        RAISE EXCEPTION 'connector task scheduling fence shape is invalid'
            USING ERRCODE = '23514';
    END IF;

    fence_scope := NULLIF(BTRIM(NEW.input #>> '{fence,scope}'), '');
    BEGIN
        fence_version := NULLIF(BTRIM(NEW.input #>> '{fence,version}'), '')::BIGINT;
        fence_sequence := NULLIF(BTRIM(NEW.input #>> '{fence,sequence}'), '')::BIGINT;
    EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RAISE EXCEPTION 'connector task scheduling fence number is invalid'
            USING ERRCODE = '23514';
    END;

    IF fence_scope IS NULL OR fence_version IS NULL OR fence_version <= 0 OR
       fence_sequence IS NULL OR fence_sequence <= 0 OR NEW.plan_id IS NULL OR
       NEW.scheduling_generation IS NULL THEN
        RAISE EXCEPTION 'connector task scheduling fence identity is incomplete'
            USING ERRCODE = '23514';
    END IF;

    IF fence_scope NOT IN (
           'auto-switch/plan/' || NEW.plan_id,
           'recommendation/rollout/' || NEW.plan_id,
           'recommendation-rollout/' || NEW.plan_id
       ) OR fence_version <> NEW.scheduling_generation THEN
        RAISE EXCEPTION 'connector task scheduling fence does not match its route plan identity'
            USING ERRCODE = '23514';
    END IF;

    -- FOR SHARE serializes this proof with scheduling_generation updates. The
    -- task cannot commit after a newer route-plan generation without being
    -- revalidated or superseded by the generation owner.
    SELECT user_id, instance_id, scheduling_generation
      INTO plan_user_id, plan_instance_id, plan_generation
      FROM route_plans
     WHERE id = NEW.plan_id
     FOR SHARE;
    IF NOT FOUND OR plan_user_id <> NEW.user_id OR plan_instance_id <> NEW.instance_id OR
       plan_generation <> NEW.scheduling_generation THEN
        RAISE EXCEPTION 'connector task scheduling fence is not the current route plan generation'
            USING ERRCODE = '23514';
    END IF;

    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_enforce_connector_task_route_plan_fence ON connector_tasks;
CREATE TRIGGER trg_enforce_connector_task_route_plan_fence
BEFORE INSERT OR UPDATE OF user_id, instance_id, connector_id, type, schema_version,
    status, input, idempotency_key, plan_id, scheduling_generation
ON connector_tasks
FOR EACH ROW
EXECUTE FUNCTION enforce_connector_task_route_plan_fence();

-- Route-plan writers lock the plan first, then inspect/transition its tasks.
-- This trigger follows that same order: it runs while PostgreSQL already owns
-- the OLD route-plan row lock and performs only an indexed existence check. An
-- executing permit is irrevocable until completion, so advancing or deleting
-- its ordering domain must fail closed even for direct SQL writers.
CREATE OR REPLACE FUNCTION guard_route_plan_executing_connector_task()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (TG_OP = 'DELETE' OR NEW.scheduling_generation IS DISTINCT FROM OLD.scheduling_generation) AND
       EXISTS (
           SELECT 1
             FROM connector_tasks
            WHERE plan_id = OLD.id
              AND status = 'executing'
       ) THEN
        RAISE EXCEPTION 'route plan has an executing connector task'
            USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_guard_route_plan_executing_connector_task ON route_plans;
CREATE TRIGGER trg_guard_route_plan_executing_connector_task
BEFORE UPDATE OF scheduling_generation OR DELETE
ON route_plans
FOR EACH ROW
EXECUTE FUNCTION guard_route_plan_executing_connector_task();

COMMIT;

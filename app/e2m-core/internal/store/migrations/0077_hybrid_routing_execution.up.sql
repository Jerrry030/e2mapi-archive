BEGIN;

ALTER TABLE hybrid_allocations
    ADD COLUMN IF NOT EXISTS routing_generation BIGINT NOT NULL DEFAULT 0
        CHECK (routing_generation >= 0);

ALTER TABLE virtual_keys
    ADD COLUMN IF NOT EXISTS key_version BIGINT NOT NULL DEFAULT 1 CHECK (key_version > 0);
ALTER TABLE virtual_keys
    ADD CONSTRAINT virtual_keys_hybrid_binding_identity_key
        UNIQUE (id,user_id,instance_id,resource_class,key_version);

CREATE TABLE IF NOT EXISTS hybrid_gateway_bindings (
    id                    TEXT PRIMARY KEY,
    user_id               BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id           TEXT NOT NULL,
    resource_class        TEXT NOT NULL CHECK (resource_class IN ('economy','stable')),
    connector_id          TEXT NOT NULL,
    credential_binding_id TEXT NOT NULL CHECK (NULLIF(BTRIM(credential_binding_id),'') IS NOT NULL),
    remote_account_id     TEXT NOT NULL DEFAULT '',
    virtual_key_id        TEXT NOT NULL,
    virtual_key_version   BIGINT NOT NULL CHECK (virtual_key_version > 0),
    status                TEXT NOT NULL CHECK (status IN ('pending','installing','ready','error')),
    error_code            TEXT NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 64),
    version               BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT hybrid_gateway_bindings_instance_owner_fkey
        FOREIGN KEY (instance_id,user_id) REFERENCES instances(id,user_id) ON DELETE CASCADE,
    CONSTRAINT hybrid_gateway_bindings_connector_owner_fkey
        FOREIGN KEY (connector_id,user_id,instance_id)
        REFERENCES connectors(connector_id,user_id,instance_id) ON DELETE CASCADE,
    CONSTRAINT hybrid_gateway_bindings_virtual_key_identity_fkey
        FOREIGN KEY (virtual_key_id,user_id,instance_id,resource_class,virtual_key_version)
        REFERENCES virtual_keys(id,user_id,instance_id,resource_class,key_version),
    CONSTRAINT hybrid_gateway_bindings_ready_remote_check
        CHECK (status <> 'ready' OR NULLIF(BTRIM(remote_account_id),'') IS NOT NULL),
    UNIQUE (instance_id,resource_class)
);
CREATE INDEX IF NOT EXISTS idx_hybrid_gateway_bindings_user_instance
    ON hybrid_gateway_bindings(user_id,instance_id,status);

CREATE TABLE IF NOT EXISTS hybrid_routing_executions (
    id                 TEXT PRIMARY KEY,
    user_id            BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    instance_id        TEXT NOT NULL,
    allocation_version BIGINT NOT NULL CHECK (allocation_version > 0),
    generation         BIGINT NOT NULL CHECK (generation > 0),
    model              TEXT NOT NULL DEFAULT '' CHECK (char_length(model) <= 128),
    status             TEXT NOT NULL CHECK (status IN ('pending','applying','succeeded','failed')),
    target             JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(target) = 'object'),
    effective          JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(effective) = 'object'),
    actual             JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(actual) = 'object'),
    desired_weights    JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(desired_weights) = 'array'),
    adjustment_codes   JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(adjustment_codes) = 'array'),
    error_code         TEXT NOT NULL DEFAULT '' CHECK (char_length(error_code) <= 64),
    lease_owner        TEXT NOT NULL DEFAULT '',
    lease_until        TIMESTAMPTZ,
    attempts           INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    version            BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at       TIMESTAMPTZ,
    CONSTRAINT hybrid_routing_executions_instance_owner_fkey
        FOREIGN KEY (instance_id,user_id) REFERENCES instances(id,user_id) ON DELETE CASCADE,
    CONSTRAINT hybrid_routing_executions_lease_shape_check CHECK (
        (status = 'applying' AND NULLIF(BTRIM(lease_owner),'') IS NOT NULL AND lease_until IS NOT NULL) OR
        (status <> 'applying' AND lease_owner = '' AND lease_until IS NULL)
    ),
    CONSTRAINT hybrid_routing_executions_completion_shape_check CHECK (
        (status IN ('succeeded','failed') AND completed_at IS NOT NULL) OR
        (status IN ('pending','applying') AND completed_at IS NULL)
    ),
    UNIQUE (id,user_id,instance_id,generation),
    UNIQUE (instance_id,generation)
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_hybrid_routing_executions_active_scope
    ON hybrid_routing_executions(instance_id)
    WHERE status IN ('pending','applying');
CREATE INDEX IF NOT EXISTS idx_hybrid_routing_executions_claim
    ON hybrid_routing_executions(status,lease_until,updated_at);
CREATE INDEX IF NOT EXISTS idx_hybrid_routing_executions_user_instance
    ON hybrid_routing_executions(user_id,instance_id,created_at DESC);

ALTER TABLE connector_tasks
    ADD COLUMN IF NOT EXISTS execution_scope TEXT,
    ADD COLUMN IF NOT EXISTS execution_id TEXT,
    ADD COLUMN IF NOT EXISTS execution_generation BIGINT;

ALTER TABLE connector_tasks
    DROP CONSTRAINT IF EXISTS connector_tasks_execution_identity_check;
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_execution_identity_check CHECK (
        (
            execution_scope IS NULL AND execution_id IS NULL AND execution_generation IS NULL
        ) OR (
            execution_scope = 'hybrid_routing' AND
            NULLIF(BTRIM(execution_id),'') IS NOT NULL AND execution_generation > 0 AND
            plan_id IS NULL AND scheduling_generation IS NULL
        )
    );
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_route_or_execution_identity_check CHECK (
        NOT (plan_id IS NOT NULL AND execution_scope IS NOT NULL)
    );
ALTER TABLE connector_tasks
    ADD CONSTRAINT connector_tasks_hybrid_execution_owner_fkey
        FOREIGN KEY (execution_id,user_id,instance_id,execution_generation)
        REFERENCES hybrid_routing_executions(id,user_id,instance_id,generation) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_connector_tasks_hybrid_execution_active
    ON connector_tasks(execution_id,execution_generation,status)
    WHERE execution_scope='hybrid_routing' AND status IN ('pending','leased','executing');

-- Replace 0074's RoutePlan-only enforcement with a disjoint RoutePlan/Hybrid
-- validator. The task identity is immutable and entering executing always
-- re-proves the current generation under a share lock.
CREATE OR REPLACE FUNCTION enforce_connector_task_route_plan_fence()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    fence_scope TEXT;
	hybrid_scope_prefix TEXT;
	hybrid_account_id TEXT;
    fence_version BIGINT;
    fence_sequence BIGINT;
    plan_user_id BIGINT;
    plan_instance_id TEXT;
    plan_generation BIGINT;
    execution_user_id BIGINT;
    execution_instance_id TEXT;
    execution_generation BIGINT;
    execution_status TEXT;
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
        NEW.scheduling_generation IS DISTINCT FROM OLD.scheduling_generation OR
        NEW.execution_scope IS DISTINCT FROM OLD.execution_scope OR
        NEW.execution_id IS DISTINCT FROM OLD.execution_id OR
        NEW.execution_generation IS DISTINCT FROM OLD.execution_generation
    ) THEN
        RAISE EXCEPTION 'connector task execution identity is immutable' USING ERRCODE='23514';
    END IF;

    IF TG_OP = 'UPDATE' AND NOT (NEW.status='executing' AND OLD.status IS DISTINCT FROM 'executing') THEN
        RETURN NEW;
    END IF;

    IF NEW.type NOT IN (
        'gateway.account.schedulable.set','gateway.account.traffic_share.set','gateway.account.switch',
        'gateway.scheduling.barrier','gateway.account.create','gateway.account.update','gateway.account.delete'
    ) THEN
        IF NEW.plan_id IS NOT NULL OR NEW.scheduling_generation IS NOT NULL OR
           NEW.execution_scope IS NOT NULL OR NEW.execution_id IS NOT NULL OR NEW.execution_generation IS NOT NULL THEN
            RAISE EXCEPTION 'read-only connector task cannot carry execution identity' USING ERRCODE='23514';
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
        IF NEW.plan_id IS NOT NULL OR NEW.scheduling_generation IS NOT NULL OR
           NEW.execution_scope IS NOT NULL OR NEW.execution_id IS NOT NULL OR NEW.execution_generation IS NOT NULL THEN
            RAISE EXCEPTION 'connector task execution identity requires an input scheduling fence' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;
    IF jsonb_typeof(NEW.input)<>'object' OR jsonb_typeof(NEW.input->'fence')<>'object' OR
       jsonb_typeof(NEW.input#>'{fence,scope}')<>'string' OR
       jsonb_typeof(NEW.input#>'{fence,version}')<>'number' OR
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
    IF fence_scope IS NULL OR fence_version IS NULL OR fence_version<=0 OR fence_sequence IS NULL OR fence_sequence<=0 THEN
        RAISE EXCEPTION 'connector task scheduling fence identity is incomplete' USING ERRCODE='23514';
    END IF;

    IF NEW.plan_id IS NOT NULL THEN
        IF NEW.execution_scope IS NOT NULL OR fence_scope NOT IN (
            'auto-switch/plan/'||NEW.plan_id,'recommendation/rollout/'||NEW.plan_id,'recommendation-rollout/'||NEW.plan_id
        ) OR fence_version<>NEW.scheduling_generation THEN
            RAISE EXCEPTION 'connector task scheduling fence does not match its route plan identity' USING ERRCODE='23514';
        END IF;
        SELECT user_id,instance_id,scheduling_generation
          INTO plan_user_id,plan_instance_id,plan_generation
          FROM route_plans WHERE id=NEW.plan_id FOR SHARE;
        IF NOT FOUND OR plan_user_id<>NEW.user_id OR plan_instance_id<>NEW.instance_id OR plan_generation<>NEW.scheduling_generation THEN
            RAISE EXCEPTION 'connector task scheduling fence is not the current route plan generation' USING ERRCODE='23514';
        END IF;
        RETURN NEW;
    END IF;

	hybrid_scope_prefix := 'hybrid-routing/instance/'||NEW.instance_id||'/account/';
	hybrid_account_id := SUBSTRING(fence_scope FROM CHAR_LENGTH(hybrid_scope_prefix)+1);
    IF NEW.execution_scope<>'hybrid_routing' OR NEW.execution_id IS NULL OR NEW.execution_generation IS NULL OR
       fence_version<>NEW.execution_generation OR
	   LEFT(fence_scope,CHAR_LENGTH(hybrid_scope_prefix))<>hybrid_scope_prefix OR
	   hybrid_account_id='' OR STRPOS(hybrid_account_id,'/')>0 THEN
        RAISE EXCEPTION 'connector task scheduling fence does not match its hybrid execution identity' USING ERRCODE='23514';
    END IF;
    SELECT user_id,instance_id,generation,status
      INTO execution_user_id,execution_instance_id,execution_generation,execution_status
      FROM hybrid_routing_executions WHERE id=NEW.execution_id FOR SHARE;
    IF NOT FOUND OR execution_user_id<>NEW.user_id OR execution_instance_id<>NEW.instance_id OR
       execution_generation<>NEW.execution_generation OR execution_status<>'applying' THEN
        RAISE EXCEPTION 'connector task scheduling fence is not the current hybrid execution generation' USING ERRCODE='23514';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM hybrid_allocations WHERE instance_id=NEW.instance_id AND routing_generation=NEW.execution_generation) THEN
        RAISE EXCEPTION 'connector task hybrid generation has been superseded' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS trg_enforce_connector_task_route_plan_fence ON connector_tasks;
CREATE TRIGGER trg_enforce_connector_task_route_plan_fence
BEFORE INSERT OR UPDATE OF user_id,instance_id,connector_id,type,schema_version,status,input,idempotency_key,
    plan_id,scheduling_generation,execution_scope,execution_id,execution_generation
ON connector_tasks FOR EACH ROW EXECUTE FUNCTION enforce_connector_task_route_plan_fence();

CREATE OR REPLACE FUNCTION guard_hybrid_routing_execution_connector_task()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF (TG_OP='DELETE' OR NEW.generation IS DISTINCT FROM OLD.generation OR NEW.status IS DISTINCT FROM OLD.status) AND
       EXISTS (SELECT 1 FROM connector_tasks WHERE execution_scope='hybrid_routing' AND execution_id=OLD.id AND status='executing') THEN
        RAISE EXCEPTION 'hybrid routing execution has an executing connector task' USING ERRCODE='23514';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_guard_hybrid_routing_execution_connector_task ON hybrid_routing_executions;
CREATE TRIGGER trg_guard_hybrid_routing_execution_connector_task
BEFORE UPDATE OF generation,status OR DELETE ON hybrid_routing_executions
FOR EACH ROW EXECUTE FUNCTION guard_hybrid_routing_execution_connector_task();

CREATE OR REPLACE FUNCTION guard_hybrid_allocation_generation_connector_task()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.routing_generation IS DISTINCT FROM OLD.routing_generation AND EXISTS (
        SELECT 1 FROM connector_tasks WHERE execution_scope='hybrid_routing'
          AND instance_id=OLD.instance_id AND execution_generation=OLD.routing_generation AND status='executing'
    ) THEN
        RAISE EXCEPTION 'hybrid allocation has an executing connector task' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS trg_guard_hybrid_allocation_generation_connector_task ON hybrid_allocations;
CREATE TRIGGER trg_guard_hybrid_allocation_generation_connector_task
BEFORE UPDATE OF routing_generation ON hybrid_allocations
FOR EACH ROW EXECUTE FUNCTION guard_hybrid_allocation_generation_connector_task();

COMMIT;

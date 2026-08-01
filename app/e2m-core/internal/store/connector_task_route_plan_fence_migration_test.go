package store

import (
	"strings"
	"testing"
)

func TestConnectorTaskRoutePlanFenceMigrationDefinesAndReversesDatabaseContract(t *testing.T) {
	up, err := migrationsFS.ReadFile("migrations/0074_connector_task_route_plan_fence.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	upSQL := strings.ToLower(string(up))
	for _, required := range []string{
		"begin;",
		"where status = 'leased'",
		"type in ('gateway.account.traffic_share.set', 'gateway.scheduling.barrier')",
		"input ? 'fence' and input -> 'fence' <> 'null'::jsonb",
		"input #> '{spec,fence}' is not null",
		"cannot upgrade while a protocol v2 fenced connector task is leased",
		"add constraint connectors_protocol_version_check check (protocol_version in (2, 3))",
		"add column if not exists plan_id text",
		"add column if not exists scheduling_generation bigint",
		"add constraint connector_tasks_status_check check",
		"status in ('pending', 'leased', 'executing', 'succeeded', 'failed', 'expired')",
		"drop index if exists uq_connector_tasks_idempotency",
		"where idempotency_key <> '' and status in ('pending', 'leased', 'executing')",
		"create or replace function parse_connector_task_route_plan_fence_bigint",
		"where task.status = 'pending'",
		"jsonb_typeof(task.input -> 'fence') = 'object'",
		"jsonb_typeof(task.input #> '{fence,scope}') = 'string'",
		"jsonb_typeof(task.input #> '{fence,version}') = 'number'",
		"jsonb_typeof(task.input #> '{fence,sequence}') = 'number'",
		"task.input #> '{spec,fence}' is null",
		"plan.user_id = candidate.user_id",
		"plan.instance_id = candidate.instance_id",
		"plan.scheduling_generation = candidate.fence_version",
		"set plan_id = recoverable.plan_id",
		"scheduling_generation = recoverable.scheduling_generation",
		"set status = 'failed'",
		"error = '{\"code\":\"scheduling_fence_stale\"}'::jsonb",
		"lease_owner = ''",
		"lease_nonce = ''",
		"lease_until = null",
		"type in ('gateway.account.traffic_share.set', 'gateway.scheduling.barrier')",
		"input #> '{spec,fence}' is not null",
		"drop function parse_connector_task_route_plan_fence_bigint(text)",
		"connector_tasks_plan_generation_pair_check",
		"plan_id is null and scheduling_generation is null",
		"nullif(btrim(plan_id), '') is not null and scheduling_generation > 0",
		"connector_tasks_plan_owner_fkey",
		"foreign key (plan_id, user_id)",
		"references route_plans(id, user_id) on delete cascade",
		"idx_connector_tasks_plan_generation_active",
		"where plan_id is not null and status in ('pending', 'leased', 'executing')",
		"create or replace function enforce_connector_task_route_plan_fence",
		"if tg_op = 'update' and (",
		"new.user_id is distinct from old.user_id",
		"new.instance_id is distinct from old.instance_id",
		"new.connector_id is distinct from old.connector_id",
		"new.type is distinct from old.type",
		"new.schema_version is distinct from old.schema_version",
		"new.input is distinct from old.input",
		"new.idempotency_key is distinct from old.idempotency_key",
		"new.plan_id is distinct from old.plan_id",
		"new.scheduling_generation is distinct from old.scheduling_generation",
		"connector task route plan execution identity is immutable",
		"new.status = 'executing' and old.status is distinct from 'executing'",
		"not (new.status = 'executing' and old.status is distinct from 'executing')",
		"new.type not in (",
		"'gateway.account.schedulable.set'",
		"'gateway.account.traffic_share.set'",
		"'gateway.account.switch'",
		"'gateway.scheduling.barrier'",
		"'gateway.account.create'",
		"'gateway.account.update'",
		"'gateway.account.delete'",
		"new.input #> '{spec,fence}' is not null",
		"connector task scheduling fence must be top-level",
		"new.input -> 'fence' = 'null'::jsonb",
		"connector task type requires a scheduling fence",
		"jsonb_typeof(new.input -> 'fence') <> 'object'",
		"new.input #>> '{fence,scope}'",
		"new.input #>> '{fence,version}'",
		"new.input #>> '{fence,sequence}'",
		"exception when invalid_text_representation or numeric_value_out_of_range",
		"new.plan_id is not null or new.scheduling_generation is not null",
		"fence_scope is null or fence_version is null or fence_version <= 0",
		"fence_sequence is null or fence_sequence <= 0 or new.plan_id is null",
		"'auto-switch/plan/' || new.plan_id",
		"'recommendation/rollout/' || new.plan_id",
		"'recommendation-rollout/' || new.plan_id",
		"fence_version <> new.scheduling_generation",
		"from route_plans",
		"where id = new.plan_id",
		"for share",
		"plan_user_id <> new.user_id",
		"plan_instance_id <> new.instance_id",
		"plan_generation <> new.scheduling_generation",
		"before insert or update of user_id, instance_id, connector_id, type, schema_version",
		"status, input, idempotency_key, plan_id, scheduling_generation",
		"execute function enforce_connector_task_route_plan_fence()",
		"create or replace function guard_route_plan_executing_connector_task",
		"tg_op = 'delete' or new.scheduling_generation is distinct from old.scheduling_generation",
		"where plan_id = old.id",
		"and status = 'executing'",
		"route plan has an executing connector task",
		"drop trigger if exists trg_guard_route_plan_executing_connector_task on route_plans",
		"before update of scheduling_generation or delete",
		"execute function guard_route_plan_executing_connector_task()",
		"commit;",
	} {
		if !strings.Contains(upSQL, required) {
			t.Errorf("0074 up migration lacks %q", required)
		}
	}

	down, err := migrationsFS.ReadFile("migrations/0074_connector_task_route_plan_fence.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downSQL := strings.ToLower(string(down))
	for _, required := range []string{
		"status = 'executing'",
		"cannot downgrade while a route-plan connector task is executing",
		"drop trigger if exists trg_guard_route_plan_executing_connector_task on route_plans",
		"drop function if exists guard_route_plan_executing_connector_task()",
		"drop trigger if exists trg_enforce_connector_task_route_plan_fence on connector_tasks",
		"drop function if exists enforce_connector_task_route_plan_fence()",
		"set status = 'failed'",
		"error = '{\"code\":\"scheduling_fence_stale\"}'::jsonb",
		"lease_owner = ''",
		"lease_nonce = ''",
		"lease_until = null",
		"where plan_id is not null",
		"and status in ('pending', 'leased')",
		"drop index if exists idx_connector_tasks_plan_generation_active",
		"drop constraint if exists connector_tasks_plan_owner_fkey",
		"drop constraint if exists connector_tasks_plan_generation_pair_check",
		"drop column if exists scheduling_generation",
		"drop column if exists plan_id",
		"cannot downgrade while a connector task is executing",
		"status in ('pending', 'leased', 'succeeded', 'failed', 'expired')",
		"where idempotency_key <> '' and status in ('pending', 'leased')",
		"if exists (select 1 from connectors where protocol_version = 3)",
		"cannot downgrade while a protocol v3 connector exists",
		"add constraint connectors_protocol_version_check check (protocol_version = 2)",
		"commit;",
	} {
		if !strings.Contains(downSQL, required) {
			t.Errorf("0074 down migration lacks %q", required)
		}
	}
	assertSourceOrder(t, downSQL,
		"update connector_tasks",
		"drop trigger if exists trg_enforce_connector_task_route_plan_fence",
		"0074 down migration must fail active route-plan tasks before removing enforcement")
	assertSourceOrder(t, downSQL,
		"drop trigger if exists trg_enforce_connector_task_route_plan_fence",
		"drop function if exists enforce_connector_task_route_plan_fence",
		"0074 down migration must remove the trigger before its function")
	assertSourceOrder(t, downSQL,
		"drop constraint if exists connector_tasks_plan_owner_fkey",
		"drop column if exists scheduling_generation",
		"0074 down migration must remove dependent constraints before columns")

	for _, forbidden := range []string{
		"new.input #>> '{spec,fence,scope}'",
		"new.input #>> '{spec,fence,version}'",
		"update connectors set protocol_version = 3",
		"update connectors set protocol_version = 2",
	} {
		if strings.Contains(upSQL, forbidden) {
			t.Errorf("0074 up migration accepts non-contract nested fence path %q", forbidden)
		}
	}
}

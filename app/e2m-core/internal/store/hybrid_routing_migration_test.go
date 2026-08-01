package store

import (
	"strings"
	"testing"
)

func TestHybridRoutingExecutionMigrationDefinesFencedContract(t *testing.T) {
	upRaw, err := migrationsFS.ReadFile("migrations/0077_hybrid_routing_execution.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	downRaw, err := migrationsFS.ReadFile("migrations/0077_hybrid_routing_execution.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down := strings.ToLower(string(upRaw)), strings.ToLower(string(downRaw))
	for _, required := range []string{
		"add column if not exists routing_generation bigint not null default 0",
		"add column if not exists key_version bigint not null default 1",
		"unique (id,user_id,instance_id,resource_class,key_version)",
		"create table if not exists hybrid_gateway_bindings",
		"foreign key (virtual_key_id,user_id,instance_id,resource_class,virtual_key_version)",
		"references virtual_keys(id,user_id,instance_id,resource_class,key_version)",
		"unique (instance_id,resource_class)",
		"create table if not exists hybrid_routing_executions",
		"unique (id,user_id,instance_id,generation)",
		"on hybrid_routing_executions(instance_id)",
		"where status in ('pending','applying')",
		"add column if not exists execution_scope text",
		"execution_scope = 'hybrid_routing'",
		"plan_id is null and scheduling_generation is null",
		"foreign key (execution_id,user_id,instance_id,execution_generation)",
		"new.execution_scope is distinct from old.execution_scope",
		"new.execution_id is distinct from old.execution_id",
		"new.execution_generation is distinct from old.execution_generation",
		"from hybrid_routing_executions where id=new.execution_id for share",
		"execution_status<>'applying'",
		"routing_generation=new.execution_generation",
		"left(fence_scope,char_length(hybrid_scope_prefix))<>hybrid_scope_prefix",
		"trg_guard_hybrid_routing_execution_connector_task",
		"trg_guard_hybrid_allocation_generation_connector_task",
		"status='executing'",
	} {
		if !strings.Contains(up, required) {
			t.Errorf("up migration missing %q", required)
		}
	}
	for _, required := range []string{
		"cannot downgrade while a hybrid routing connector task is executing",
		"where execution_scope='hybrid_routing' and status in ('pending','leased')",
		"drop constraint if exists connector_tasks_hybrid_execution_owner_fkey",
		"drop table if exists hybrid_routing_executions",
		"drop table if exists hybrid_gateway_bindings",
		"drop column if exists routing_generation",
		"drop constraint if exists virtual_keys_hybrid_binding_identity_key",
		"drop column if exists key_version",
		"create trigger trg_enforce_connector_task_route_plan_fence",
		"connector task route plan execution identity is immutable",
	} {
		if !strings.Contains(down, required) {
			t.Errorf("down migration missing %q", required)
		}
	}
	if strings.Index(down, "drop constraint if exists connector_tasks_hybrid_execution_owner_fkey") >
		strings.Index(down, "drop table if exists hybrid_routing_executions") {
		t.Fatal("connector task FK must be dropped before hybrid execution table")
	}
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresConnectorTaskCurrentClosedTypeSet(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	user, err := st.CreateUser(ctx, contracts.User{
		Email: "connector-task-types-" + newID("test") + "@example.com",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "Connector task closed set", Kind: contracts.InstanceKindNewAPI,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorID := newID("conn-types")
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connectorID, TokenHash: newID("enroll-hash"),
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: connectorID, InstanceID: instance.ID, Version: "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: newID("connector-hash"),
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		ID: newID("pool-types"), Name: "Connector task closed set", Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatalf("create route-plan pool: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: newID("plan-types"), UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, SchedulingGeneration: 1,
	})
	if err != nil {
		t.Fatalf("create route plan: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connector_tasks WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM route_plans WHERE id=$1`, plan.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM upstream_pools WHERE id=$1`, pool.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connectors WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connector_enrollments WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})

	fence := contracts.GatewaySchedulingFence{
		Scope: "auto-switch/plan/" + plan.ID, Version: plan.SchedulingGeneration, Sequence: 1,
	}
	trafficInput, _ := json.Marshal(contracts.ConnectorGatewayTrafficShareSetInput{
		AccountID: "account-zero", Weight: 0, Fence: fence,
	})
	barrierInput, _ := json.Marshal(contracts.ConnectorGatewaySchedulingBarrierInput{Fence: fence})
	collectInput, _ := json.Marshal(contracts.ConnectorUpstreamIntelligenceCollectInput{
		SchemaVersion: 1, SourceID: "uisrc-postgres", Reason: contracts.ConnectorUpstreamIntelligenceCollectManualRefresh,
	})
	inputByType := map[contracts.ConnectorTaskType]json.RawMessage{
		contracts.ConnectorTaskGatewayTrafficShareSet:      trafficInput,
		contracts.ConnectorTaskGatewaySchedulingBarrier:    barrierInput,
		contracts.ConnectorTaskUpstreamIntelligenceCollect: collectInput,
	}
	taskTypes := make([]string, 0, len(connectorTaskTypesAt0041())+2)
	taskTypes = append(taskTypes, connectorTaskTypesAt0041()...)
	taskTypes = append(taskTypes,
		string(contracts.ConnectorTaskGatewayTrafficShareSet),
		string(contracts.ConnectorTaskUpstreamIntelligenceCollect),
	)
	for _, rawTaskType := range taskTypes {
		taskType := contracts.ConnectorTaskType(rawTaskType)
		t.Run(rawTaskType, func(t *testing.T) {
			input := inputByType[taskType]
			if input == nil {
				input = json.RawMessage(`{}`)
			}
			if _, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
				UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID,
				Type: taskType, SchemaVersion: 1, Input: input,
				IdempotencyKey: "closed-set:" + rawTaskType,
			}); err != nil {
				t.Fatalf("create %s task: %v", taskType, err)
			}
		})
	}

	// Exercise PostgreSQL's CHECK directly: Store's protocol guard already
	// rejects unknown types, so reaching this statement proves the schema also
	// remains fail closed independently of application validation.
	if _, err := st.pool.Exec(ctx, `INSERT INTO connector_tasks (
		id, user_id, instance_id, connector_id, type, schema_version, risk_level,
		status, input, idempotency_key, max_attempts, available_at, expires_at
	) VALUES ($1,$2,$3,$4,$5,1,'L1','pending','{}'::jsonb,$6,3,now(),now()+interval '1 minute')`,
		newID("task-unknown"), user.ID, instance.ID, connector.ID, "gateway.raw.request", newID("idem-unknown")); err == nil {
		t.Fatal("PostgreSQL connector task CHECK accepted an unknown task type")
	}
}

func TestPostgresConnectorTaskRetryAndExpiryPolicy(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("open postgres store: %v", err)
	}
	t.Cleanup(st.Close)

	user, err := st.CreateUser(ctx, contracts.User{
		Email: "connector-task-" + newID("test") + "@example.com",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "Connector task policy", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorID := newID("conn-test")
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connectorID, TokenHash: newID("enroll-hash"),
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: connectorID, InstanceID: instance.ID, Version: "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: newID("connector-hash"),
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connector_tasks WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connectors WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM connector_enrollments WHERE connector_id=$1`, connector.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM instances WHERE id=$1`, instance.ID)
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID)
	})

	task, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID,
		Type: contracts.ConnectorTaskGatewayAccountsList, MaxAttempts: 3,
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	leased, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{
		ConnectorID: connector.ID, LeaseSeconds: 30,
	})
	if err != nil || len(leased) != 1 || leased[0].ID != task.ID {
		t.Fatalf("lease task: tasks=%+v err=%v", leased, err)
	}
	completed, err := st.CompleteConnectorTask(ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID, LeaseNonce: leased[0].LeaseNonce,
		Error: contracts.ConnectorTaskError{Code: "gateway_timeout", Retryable: true},
	})
	if err != nil {
		t.Fatalf("complete retryable task: %v", err)
	}
	if completed.Status != contracts.ConnectorTaskPending || completed.LeaseOwner != "" ||
		completed.LeaseNonce != "" || completed.LeaseUntil != nil ||
		completed.AvailableAt.Before(completed.UpdatedAt.Add(connectorTaskRetryDelay(1)-time.Second)) {
		t.Fatalf("retry policy not persisted: %+v", completed)
	}
	if early, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID}); err != nil || len(early) != 0 {
		t.Fatalf("task leased before retry delay: tasks=%+v err=%v", early, err)
	}

	if _, err := st.pool.Exec(ctx,
		`UPDATE connector_tasks SET status='leased', result='{"accounts":[]}'::jsonb,
		 error='{"code":"gateway_timeout","retryable":true}'::jsonb,
		 lease_owner=$2, lease_nonce='old-nonce', lease_until=statement_timestamp()+interval '1 minute',
		 expires_at=statement_timestamp()-interval '1 second' WHERE id=$1`, task.ID, connector.ID); err != nil {
		t.Fatalf("seed legacy expired lease: %v", err)
	}
	if _, err := st.CompleteConnectorTask(ctx, task.ID, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID, LeaseNonce: "old-nonce", Success: true,
		Result: []byte(`{"accounts":[]}`),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired completion error=%v, want ErrConflict", err)
	}
	expired, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get expired task: %v", err)
	}
	if expired.Status != contracts.ConnectorTaskExpired || expired.Error != (contracts.ConnectorTaskError{Code: "expired"}) ||
		expired.LeaseOwner != "" || expired.LeaseNonce != "" || expired.LeaseUntil != nil || len(expired.Result) != 0 {
		t.Fatalf("stable expiry not persisted: %+v", expired)
	}
}

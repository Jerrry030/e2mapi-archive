package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestPostgresResolveConnectorTaskExecutionIsTerminalAndAudited(t *testing.T) {
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
		Email: "connector-resolve-" + newID("test") + "@example.com",
		Roles: []contracts.UserRole{contracts.UserRoleClient}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID, Name: "Connector execution resolution", Kind: contracts.InstanceKindNewAPI,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorID := newID("conn-resolve")
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connectorID, TokenHash: newID("enroll-token"),
	})
	if err != nil {
		t.Fatalf("create enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: connectorID, InstanceID: instance.ID, Version: "0.3.0",
		ProtocolVersion: contracts.ConnectorProtocolVersion, TokenHash: newID("connector-token"),
	})
	if err != nil {
		t.Fatalf("use enrollment: %v", err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		ID: newID("pool-resolve"), Name: "Execution resolution pool", Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: newID("plan-resolve"), UserID: user.ID, InstanceID: instance.ID,
		PoolID: pool.ID, Status: contracts.RoutePlanPublished, SchedulingGeneration: 1,
	})
	if err != nil {
		t.Fatalf("create route plan: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.pool.Exec(context.Background(), `DELETE FROM operation_audits WHERE target_type='connector_task' AND target_id IN (SELECT id FROM connector_tasks WHERE connector_id=$1)`, connector.ID)
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
	input := contracts.ConnectorGatewayTrafficShareSetInput{AccountID: "account-a", Weight: 25, Fence: fence}
	rawInput, _ := json.Marshal(input)
	task, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID,
		Type: contracts.ConnectorTaskGatewayTrafficShareSet, SchemaVersion: 1, Input: rawInput,
	})
	if err != nil {
		t.Fatalf("create fenced task: %v", err)
	}
	leased, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID})
	if err != nil || len(leased) != 1 || leased[0].ID != task.ID {
		t.Fatalf("lease fenced task: tasks=%+v err=%v", leased, err)
	}
	executing, err := st.BeginConnectorTaskExecution(ctx, task.ID, contracts.ConnectorTaskExecutionRequest{
		ConnectorID: connector.ID, LeaseNonce: leased[0].LeaseNonce,
	})
	if err != nil {
		t.Fatalf("begin execution: %v", err)
	}
	nonceDigest := sha256.Sum256([]byte(executing.LeaseNonce))
	nonceRef := "sha256:" + hex.EncodeToString(nonceDigest[:])
	evidence := "Verified the exact account weight with an independent read-back."
	result, _ := json.Marshal(contracts.ConnectorGatewayTrafficShareSetResult(input))
	req := contracts.ConnectorTaskExecutionResolveRequest{
		LeaseNonce: executing.LeaseNonce, Resolution: contracts.ConnectorTaskExecutionConfirmedApplied,
		EvidenceNote: evidence, Result: result,
	}
	audit := contracts.OperationAudit{
		UserID: user.ID, InstanceID: instance.ID, ActorType: "user", ActorID: "9001",
		Action: "connector_task.resolve_execution", RiskLevel: contracts.RiskLevelL3,
		EventLevel: contracts.EventLevelCritical, TargetType: "connector_task", TargetID: task.ID,
		Result: string(req.Resolution), Details: map[string]string{
			"resolution": string(req.Resolution), "evidence_note": evidence,
			"connector_id": connector.ID, "lease_nonce_sha256": nonceRef,
		},
	}
	resolved, err := st.ResolveConnectorTaskExecution(ctx, task.ID, req, audit)
	if err != nil {
		t.Fatalf("resolve execution: %v", err)
	}
	if resolved.Status != contracts.ConnectorTaskSucceeded || resolved.LeaseOwner != "" ||
		resolved.LeaseNonce != "" || resolved.LeaseUntil != nil || string(resolved.Result) != string(result) {
		t.Fatalf("resolution was not terminal: %+v", resolved)
	}
	if _, err := st.ResolveConnectorTaskExecution(ctx, task.ID, req, audit); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeated resolution error=%v, want ErrConflict", err)
	}
	audits, err := st.ListAuditsByTarget(ctx, "connector_task", task.ID)
	if err != nil || len(audits) != 1 {
		t.Fatalf("atomic resolution audit: audits=%+v err=%v", audits, err)
	}
	if audits[0].Details["lease_nonce_sha256"] != nonceRef || audits[0].Details["lease_nonce"] != "" {
		t.Fatalf("audit leaked or lost execution correlation: %+v", audits[0].Details)
	}
}

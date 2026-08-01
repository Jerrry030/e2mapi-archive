package httpapi

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

type countingConnectorTaskStore struct {
	store.Store
	createCalls int
}

func (s *countingConnectorTaskStore) CreateConnectorTask(ctx context.Context, task contracts.ConnectorTask) (contracts.ConnectorTask, error) {
	s.createCalls++
	return s.Store.CreateConnectorTask(ctx, task)
}

func TestConnectorTaskAuditDetailsUsesStoredInventoryNames(t *testing.T) {
	srv, base, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "connector-audit-inventory@e2m.local", contracts.UserRoleOwner)
	instance, err := base.CreateInstance(ctx, contracts.Instance{
		UserID: owner.ID, Name: "库存实例", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := base.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: owner.ID, InstanceID: instance.ID, ConnectorID: "connector-a",
		TokenHash: "inventory-enrollment", ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := base.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID: "connector-a", InstanceID: instance.ID, Version: "0.1.0-test", ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash: "inventory-token",
	}); err != nil {
		t.Fatal(err)
	}
	wrapped := &countingConnectorTaskStore{Store: base}
	srv.store = wrapped
	inventory, err := wrapped.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: owner.ID, InstanceID: instance.ID, ConnectorID: "connector-a",
		Type: contracts.ConnectorTaskGatewayAccountsList, Status: contracts.ConnectorTaskSucceeded,
		SchemaVersion: 1, Input: []byte(`{}`),
		Result:         []byte(`{"accounts":[{"id":"opaque-from","display_name":"主账号"},{"id":"opaque-to","display_name":"备用账号"}]}`),
		IdempotencyKey: "inventory-completed", MaxAttempts: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = inventory
	before := wrapped.createCalls
	details := srv.connectorTaskAuditDetails(ctx, contracts.ConnectorTask{
		UserID: owner.ID, InstanceID: instance.ID, ConnectorID: "connector-a",
		Type:  contracts.ConnectorTaskGatewaySwitch,
		Input: []byte(`{"disable_account_id":"opaque-from","enable_account_id":"opaque-to"}`),
	})
	if wrapped.createCalls != before {
		t.Fatalf("enrichment created connector work: before=%d after=%d", before, wrapped.createCalls)
	}
	if details["instance_name"] != "库存实例" || details["from_account_name"] != "主账号" ||
		details["to_account_name"] != "备用账号" {
		t.Fatalf("inventory names=%+v", details)
	}
}

func TestConnectorTaskAuditDetailsUsesStoredBusinessNamesWithoutLeakingOpaqueIDs(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "connector-audit-names@e2m.local", contracts.UserRoleOwner)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: owner.ID, Name: "生产实例", Kind: contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "稳定池", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateUpstreamChannel(ctx, contracts.UpstreamChannel{
		PoolID: pool.ID, DisplayName: "主上游", Status: contracts.UpstreamChannelActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		UserID: owner.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertPublishedBinding(ctx, contracts.PublishedBinding{
		PlanID: plan.ID, InstanceID: instance.ID, ChannelID: channel.ID,
		RemoteID: "opaque-account-a", State: contracts.BindingActive,
	}); err != nil {
		t.Fatal(err)
	}

	before, err := st.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{InstanceID: instance.ID})
	if err != nil {
		t.Fatal(err)
	}
	task := contracts.ConnectorTask{
		UserID: owner.ID, InstanceID: instance.ID, ConnectorID: "connector-a",
		Type:  contracts.ConnectorTaskGatewaySwitch,
		Input: []byte(`{"disable_account_id":"opaque-account-a","enable_account_id":"opaque-account-b"}`),
		Error: contracts.ConnectorTaskError{Code: "gateway_timeout"},
	}
	details := srv.connectorTaskAuditDetails(ctx, task)
	after, err := st.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{InstanceID: instance.ID})
	if err != nil || len(after) != len(before) {
		t.Fatalf("enrichment created connector work: before=%d after=%d err=%v", len(before), len(after), err)
	}
	if details["instance_name"] != "生产实例" || details["from_account_name"] != "主上游" ||
		details["reason_code"] != "gateway_timeout" {
		t.Fatalf("business details=%+v", details)
	}
	if details["to_account_name"] != "" {
		t.Fatalf("unknown account ID was copied into a display name: %+v", details)
	}

	safe := ownerSafeAudit(contracts.OperationAudit{
		TargetID: "task-secret", ErrorMessage: "provider response",
		WorkflowRunID: "workflow-secret", Details: details,
	})
	if safe.TargetID != "" || safe.ErrorMessage != "" || safe.WorkflowRunID != "" ||
		safe.Details["from_account_id"] != "" || safe.Details["to_account_id"] != "" ||
		safe.Details["from_account_name"] != "主上游" || safe.Details["reason_code"] != "gateway_timeout" {
		t.Fatalf("owner projection=%+v", safe)
	}
}

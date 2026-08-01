package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"e2m.local/contracts"
)

func TestCompleteTrafficShareTaskRequiresExactPersistedReceipt(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()
	user := createLoginUser(t, authSvc, "traffic-share-completion@e2m.local", contracts.UserRoleOwner)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Traffic share gateway",
		Kind:   contracts.InstanceKindNewAPI,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-token-traffic-share-completion"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-traffic-share-completion", connectorToken)
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{
		Name: "Traffic share completion", Status: contracts.UpstreamPoolActive,
	})
	if err != nil {
		t.Fatalf("create upstream pool: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-zero", UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, SchedulingGeneration: 4,
	})
	if err != nil {
		t.Fatalf("create route plan: %v", err)
	}
	input := contracts.ConnectorGatewayTrafficShareSetInput{
		AccountID: "account-zero",
		Weight:    0,
		Fence: contracts.GatewaySchedulingFence{
			Scope: "recommendation-rollout/" + plan.ID, Version: plan.SchedulingGeneration, Sequence: 2,
		},
	}
	task, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID:         user.ID,
		InstanceID:     instance.ID,
		ConnectorID:    connector.ID,
		Type:           contracts.ConnectorTaskGatewayTrafficShareSet,
		SchemaVersion:  1,
		Input:          trafficShareCompletionJSON(t, input),
		IdempotencyKey: "traffic-share-zero-completion",
		MaxAttempts:    3,
	})
	if err != nil {
		t.Fatalf("create traffic-share task: %v", err)
	}
	leased := leaseOneTask(t, h, connectorToken, connector.ID)
	w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/execute", connectorToken, contracts.ConnectorTaskExecutionRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  leased.LeaseNonce,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("begin execution = %d %s", w.Code, w.Body.String())
	}
	assertTaskExecutingWithoutResult(t, st, task.ID)

	mismatch := contracts.ConnectorGatewayTrafficShareSetResult(input)
	mismatch.Weight = 10
	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  leased.LeaseNonce,
		Success:     true,
		Result:      trafficShareCompletionJSON(t, mismatch),
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_task_result") {
		t.Fatalf("mismatched receipt = %d %s", w.Code, w.Body.String())
	}
	assertTaskExecutingWithoutResult(t, st, task.ID)

	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  leased.LeaseNonce,
		Success:     true,
		Result:      trafficShareCompletionJSON(t, contracts.ConnectorGatewayTrafficShareSetResult(input)),
	})
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("exact receipt = %d %s", w.Code, w.Body.String())
	}
	completed, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if completed.Status != contracts.ConnectorTaskSucceeded {
		t.Fatalf("completed task = %+v", completed)
	}
	var receipt contracts.ConnectorGatewayTrafficShareSetResult
	if err := json.Unmarshal(completed.Result, &receipt); err != nil || receipt != (contracts.ConnectorGatewayTrafficShareSetResult(input)) {
		t.Fatalf("stored receipt = %+v err=%v", receipt, err)
	}
	audits, err := st.ListAudits(ctx, user.ID)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	found := false
	for _, audit := range audits {
		if audit.TargetID != task.ID {
			continue
		}
		found = true
		if audit.Action != "connector_task.complete.gateway.account.traffic_share.set" ||
			audit.Details["account_id"] != input.AccountID ||
			audit.Details["traffic_share"] != "0" ||
			audit.Details["fence_scope"] != input.Fence.Scope ||
			audit.Details["fence_version"] != "4" || audit.Details["fence_sequence"] != "2" {
			t.Fatalf("traffic-share audit = %+v", audit)
		}
	}
	if !found {
		t.Fatal("traffic-share completion audit not found")
	}
}

func assertTaskExecutingWithoutResult(t *testing.T, st interface {
	GetConnectorTask(context.Context, string) (contracts.ConnectorTask, error)
}, taskID string) {
	t.Helper()
	task, err := st.GetConnectorTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get rejected executing task: %v", err)
	}
	if task.Status != contracts.ConnectorTaskExecuting || len(task.Result) != 0 {
		t.Fatalf("rejected completion changed executing task state: %+v", task)
	}
}

func TestValidateTrafficShareCompletionAcceptsExactZeroReceipt(t *testing.T) {
	input := contracts.ConnectorGatewayTrafficShareSetInput{
		AccountID: "account-zero",
		Weight:    0,
		Fence: contracts.GatewaySchedulingFence{
			Scope: "recommendation-rollout/plan-zero", Version: 4, Sequence: 2,
		},
	}
	task := trafficShareCompletionTask(t, input)
	req := contracts.ConnectorTaskCompleteRequest{
		Success: true,
		Result:  trafficShareCompletionJSON(t, contracts.ConnectorGatewayTrafficShareSetResult(input)),
	}

	if err := validateConnectorTaskCompletion(task, &req); err != nil {
		t.Fatalf("exact zero receipt: %v", err)
	}
	var got contracts.ConnectorGatewayTrafficShareSetResult
	if err := json.Unmarshal(req.Result, &got); err != nil {
		t.Fatalf("decode canonical result: %v", err)
	}
	if got != (contracts.ConnectorGatewayTrafficShareSetResult(input)) {
		t.Fatalf("canonical receipt = %+v, want %+v", got, input)
	}
}

func TestValidateTrafficShareCompletionRejectsUnknownOrInvalidFields(t *testing.T) {
	valid := contracts.ConnectorGatewayTrafficShareSetInput{
		AccountID: "account-a",
		Weight:    25,
		Fence: contracts.GatewaySchedulingFence{
			Scope: "recommendation-rollout/plan-a", Version: 3, Sequence: 1,
		},
	}
	task := trafficShareCompletionTask(t, valid)
	tests := []struct {
		name    string
		result  string
		wantErr string
	}{
		{
			name:    "unknown field",
			result:  `{"account_id":"account-a","weight":25,"fence":{"scope":"recommendation-rollout/plan-a","version":3,"sequence":1},"url":"https://must-not-cross.example"}`,
			wantErr: "unknown field",
		},
		{
			name:    "negative weight",
			result:  `{"account_id":"account-a","weight":-1,"fence":{"scope":"recommendation-rollout/plan-a","version":3,"sequence":1}}`,
			wantErr: "valid account_id, weight, and complete scheduling fence",
		},
		{
			name:    "weight over one hundred",
			result:  `{"account_id":"account-a","weight":101,"fence":{"scope":"recommendation-rollout/plan-a","version":3,"sequence":1}}`,
			wantErr: "valid account_id, weight, and complete scheduling fence",
		},
		{
			name:    "missing fence sequence",
			result:  `{"account_id":"account-a","weight":25,"fence":{"scope":"recommendation-rollout/plan-a","version":3}}`,
			wantErr: "valid account_id, weight, and complete scheduling fence",
		},
		{
			name:    "sensitive fence scope",
			result:  `{"account_id":"account-a","weight":25,"fence":{"scope":"https://secret.example/token","version":3,"sequence":1}}`,
			wantErr: "valid account_id, weight, and complete scheduling fence",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := contracts.ConnectorTaskCompleteRequest{Success: true, Result: json.RawMessage(test.result)}
			err := validateConnectorTaskCompletion(task, &req)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateTrafficShareCompletionRequiresExactPersistedCommand(t *testing.T) {
	input := contracts.ConnectorGatewayTrafficShareSetInput{
		AccountID: "account-a",
		Weight:    25,
		Fence: contracts.GatewaySchedulingFence{
			Scope: "recommendation-rollout/plan-a", Version: 3, Sequence: 2,
		},
	}
	task := trafficShareCompletionTask(t, input)
	matching := contracts.ConnectorGatewayTrafficShareSetResult(input)

	wrongAccount := matching
	wrongAccount.AccountID = "account-b"
	wrongWeight := matching
	wrongWeight.Weight = 50
	wrongFence := matching
	wrongFence.Fence.Sequence++
	for name, result := range map[string]contracts.ConnectorGatewayTrafficShareSetResult{
		"account": wrongAccount,
		"weight":  wrongWeight,
		"fence":   wrongFence,
	} {
		t.Run(name, func(t *testing.T) {
			req := contracts.ConnectorTaskCompleteRequest{Success: true, Result: trafficShareCompletionJSON(t, result)}
			err := validateConnectorTaskCompletion(task, &req)
			if err == nil || !strings.Contains(err.Error(), "does not exactly match") {
				t.Fatalf("error = %v, want exact-match rejection", err)
			}
		})
	}
}

func TestValidateTrafficShareCompletionFailsClosedOnInvalidPersistedInput(t *testing.T) {
	task := contracts.ConnectorTask{
		Type:  contracts.ConnectorTaskGatewayTrafficShareSet,
		Input: json.RawMessage(`{"account_id":"account-a","weight":25,"fence":{"scope":"rollout","version":1,"sequence":1},"header":"Bearer secret"}`),
	}
	req := contracts.ConnectorTaskCompleteRequest{
		Success: true,
		Result:  json.RawMessage(`{"account_id":"account-a","weight":25,"fence":{"scope":"rollout","version":1,"sequence":1}}`),
	}
	if err := validateConnectorTaskCompletion(task, &req); err == nil || !strings.Contains(err.Error(), "does not exactly match") {
		t.Fatalf("invalid persisted input error = %v", err)
	}
}

func trafficShareCompletionTask(t *testing.T, input contracts.ConnectorGatewayTrafficShareSetInput) contracts.ConnectorTask {
	t.Helper()
	return contracts.ConnectorTask{
		Type:  contracts.ConnectorTaskGatewayTrafficShareSet,
		Input: trafficShareCompletionJSON(t, input),
	}
}

func trafficShareCompletionJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

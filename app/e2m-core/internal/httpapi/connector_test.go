package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestInstanceCreateReturnsBootstrapOnlyConnectorInstall(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	srv.SetPublicBaseURL("https://core.example.test")
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "instance-install@e2m.local", contracts.UserRoleOwner)
	token, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	w := do(t, h, http.MethodPost, "/api/v1/instances", token, map[string]any{
		"name": "Local sub2api",
		"kind": contracts.InstanceKindSub2API,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create instance: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)

	var created instanceResponse
	decodeResponse(t, w, &created)
	if created.ID == "" || created.UserID != user.ID || created.Name != "Local sub2api" || created.Kind != contracts.InstanceKindSub2API {
		t.Fatalf("unexpected instance: %+v", created.Instance)
	}
	if created.ConnectorInstall == nil {
		t.Fatal("instance creation did not return a connector install")
	}
	assertBootstrapOnlyInstall(t, *created.ConnectorInstall, created.ID)

	persisted, err := st.GetInstance(ctx, created.ID)
	if err != nil {
		t.Fatalf("get created instance: %v", err)
	}
	if persisted.Name != created.Name || persisted.Kind != created.Kind || persisted.UserID != user.ID {
		t.Fatalf("unexpected persisted instance: %+v", persisted)
	}

	for _, field := range []string{"endpoint_url", "endpoint_ref", "agent_id", "project_id"} {
		for _, target := range []struct {
			name   string
			method string
			path   string
		}{
			{name: "create", method: http.MethodPost, path: "/api/v1/instances"},
			{name: "update", method: http.MethodPut, path: "/api/v1/instances/" + created.ID},
		} {
			t.Run(target.name+" rejects "+field, func(t *testing.T) {
				marker := "must-not-reach-core-" + strings.ReplaceAll(field, "_", "-")
				w := do(t, h, target.method, target.path, token, map[string]any{
					"name": "No deprecated instance identity " + field,
					"kind": contracts.InstanceKindSub2API,
					field:  marker,
				})
				if w.Code != http.StatusBadRequest {
					t.Fatalf("%s must be rejected: %d %s", field, w.Code, w.Body.String())
				}
			})
		}
	}

	w = do(t, h, http.MethodPost, "/api/v1/instances", token, map[string]any{
		"name":      "Secret must fail",
		"kind":      contracts.InstanceKindSub2API,
		"admin_key": "not-allowed-in-core",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("admin_key must be rejected, got %d %s", w.Code, w.Body.String())
	}
}

func TestConnectorEnrollmentIsExactSingleUseAndProtocolV1(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "enrollment@e2m.local", contracts.UserRoleOwner)
	instanceA, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Gateway A",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance A: %v", err)
	}
	instanceB, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Gateway B",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance B: %v", err)
	}
	userToken, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	w := do(t, h, http.MethodPost, "/api/v1/connectors/enrollments", userToken, map[string]any{
		"name": "missing instance",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("instance_id must be required, got %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodPost, "/api/v1/connectors/enrollments", userToken, map[string]any{
		"instance_id":  instanceA.ID,
		"connector_id": "connector-a",
		"name":         "Connector A",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create enrollment: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)
	var enrollment contracts.CreateConnectorEnrollmentResponse
	decodeResponse(t, w, &enrollment)
	if enrollment.Token == "" || enrollment.Enrollment.InstanceID != instanceA.ID || enrollment.Enrollment.ConnectorID != "connector-a" {
		t.Fatalf("enrollment is not bound to the requested identity: %+v", enrollment.Enrollment)
	}

	enrollPath := "/api/v1/connectors/enroll"
	w = do(t, h, http.MethodPost, enrollPath, "", map[string]any{
		"enrollment_token": enrollment.Token,
		"instance_id":      instanceA.ID,
		"connector_id":     "connector-other",
		"protocol_version": contracts.ConnectorProtocolVersion,
		"version":          "0.1.0-test",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("wrong connector_id must not consume enrollment: %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodPost, enrollPath, "", map[string]any{
		"enrollment_token": enrollment.Token,
		"instance_id":      instanceB.ID,
		"connector_id":     enrollment.Enrollment.ConnectorID,
		"protocol_version": contracts.ConnectorProtocolVersion,
		"version":          "0.1.0-test",
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("wrong instance_id must not consume enrollment: %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodPost, enrollPath, "", map[string]any{
		"enrollment_token": enrollment.Token,
		"instance_id":      instanceA.ID,
		"connector_id":     enrollment.Enrollment.ConnectorID,
		"protocol_version": contracts.ConnectorProtocolVersion + 1,
	})
	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("unsupported enrollment protocol must be rejected with 426, got %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodPost, enrollPath, "", map[string]any{
		"enrollment_token": enrollment.Token,
		"instance_id":      instanceA.ID,
		"connector_id":     enrollment.Enrollment.ConnectorID,
		"protocol_version": contracts.ConnectorProtocolVersion,
		"version":          "Bearer secret",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-semver connector version must be rejected, got %d %s", w.Code, w.Body.String())
	}

	for _, field := range []string{"endpoint_url", "endpoint_ref", "agent_id", "project_id"} {
		w = do(t, h, http.MethodPost, enrollPath, "", map[string]any{
			"enrollment_token": enrollment.Token,
			"instance_id":      instanceA.ID,
			"connector_id":     enrollment.Enrollment.ConnectorID,
			"protocol_version": contracts.ConnectorProtocolVersion,
			field:              "deprecated",
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("enrollment must reject deprecated %s, got %d %s", field, w.Code, w.Body.String())
		}
	}

	validEnroll := map[string]any{
		"enrollment_token": enrollment.Token,
		"instance_id":      instanceA.ID,
		"connector_id":     enrollment.Enrollment.ConnectorID,
		"protocol_version": contracts.ConnectorProtocolVersion,
		"version":          "1.0.0",
	}
	w = do(t, h, http.MethodPost, enrollPath, "", validEnroll)
	if w.Code != http.StatusCreated {
		t.Fatalf("enroll exact connector: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)
	var enrolled contracts.ConnectorEnrollResponse
	decodeResponse(t, w, &enrolled)
	if enrolled.ConnectorToken == "" ||
		enrolled.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		enrolled.NextPollAfter != "5s" ||
		enrolled.Connector.ID != enrollment.Enrollment.ConnectorID ||
		enrolled.Connector.InstanceID != instanceA.ID ||
		enrolled.Connector.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("unexpected enrolled connector: %+v", enrolled)
	}
	if strings.Contains(w.Body.String(), "token_hash") {
		t.Fatalf("connector token hash leaked: %s", w.Body.String())
	}
	bound, err := st.GetInstance(ctx, instanceA.ID)
	if err != nil {
		t.Fatalf("get bound instance: %v", err)
	}
	if bound.ConnectorID != enrolled.Connector.ID {
		t.Fatalf("enrollment did not bind the exact instance: %+v", bound)
	}

	w = do(t, h, http.MethodPost, enrollPath, "", validEnroll)
	if w.Code != http.StatusConflict {
		t.Fatalf("enrollment token must be single-use, got %d %s", w.Code, w.Body.String())
	}

	_, connectorB := createStoredConnector(t, st, user.ID, instanceB, "connector-b", "connector-token-b")
	taskB := createAccountsTask(t, st, user.ID, instanceB.ID, connectorB.ID, "instance-b-task", 3)

	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease", enrolled.ConnectorToken, contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     connectorB.ID,
		ProtocolVersion: contracts.ConnectorProtocolVersion,
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("token bound to another instance must not lease: %d %s", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease", "invalid-connector-token", contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     enrolled.Connector.ID,
		ProtocolVersion: contracts.ConnectorProtocolVersion,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid connector token must not lease: %d %s", w.Code, w.Body.String())
	}
	persistedTaskB, err := st.GetConnectorTask(ctx, taskB.ID)
	if err != nil {
		t.Fatalf("get connector B task: %v", err)
	}
	if persistedTaskB.Status != contracts.ConnectorTaskPending {
		t.Fatalf("failed identity checks changed task state: %+v", persistedTaskB)
	}
}

func TestTypedConnectorTasksLeaseAndComplete(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "typed-task@e2m.local", contracts.UserRoleOwner)
	userToken, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Task gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-token-typed-task"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-typed", connectorToken)

	task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "accounts-success", 3)
	w := leaseTasks(t, h, connectorToken, connector.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("lease typed task: %d %s", w.Code, w.Body.String())
	}
	var leased contracts.ConnectorTaskLeaseResponse
	decodeResponse(t, w, &leased)
	if len(leased.Tasks) != 1 || leased.Tasks[0].ID != task.ID {
		t.Fatalf("unexpected leased tasks: %+v", leased.Tasks)
	}
	if leased.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("unexpected lease protocol version: %d", leased.ProtocolVersion)
	}
	if leased.Tasks[0].Type != contracts.ConnectorTaskGatewayAccountsList ||
		leased.Tasks[0].SchemaVersion != 1 ||
		leased.Tasks[0].RiskLevel != contracts.RiskLevelL0 ||
		strings.TrimSpace(leased.Tasks[0].LeaseNonce) == "" {
		t.Fatalf("task did not retain its typed contract: %+v", leased.Tasks[0])
	}

	result := contracts.ConnectorGatewayAccountsListResult{Accounts: []contracts.GatewayAccount{
		{
			ID:          "account-1",
			Platform:    "openai",
			Status:      "active",
			Schedulable: true,
			DisplayName: "Primary",
		},
	}}
	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  leased.Tasks[0].LeaseNonce,
		Success:     true,
		Result:      mustJSON(t, result),
	})
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("complete accounts task: %d %s", w.Code, w.Body.String())
	}
	completed, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if completed.Status != contracts.ConnectorTaskSucceeded || completed.Error.Code != "" {
		t.Fatalf("unexpected completed task: %+v", completed)
	}
	var savedResult contracts.ConnectorGatewayAccountsListResult
	if err := json.Unmarshal(completed.Result, &savedResult); err != nil {
		t.Fatalf("decode typed result: %v", err)
	}
	if len(savedResult.Accounts) != 1 || savedResult.Accounts[0].ID != "account-1" || !savedResult.Accounts[0].Schedulable {
		t.Fatalf("unexpected accounts DTO: %+v", savedResult)
	}
	assertConnectorTaskCompletionAudit(t, st, user.ID, task.ID,
		"connector_task.complete.gateway.accounts.list", "accepted", "")
	audits, err := st.ListAudits(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range audits {
		if audit.TargetID == task.ID && audit.Details["account_count"] != "1" {
			t.Fatalf("connector account-list audit details=%+v", audit.Details)
		}
	}

	errorTask := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "accounts-error", 1)
	errorLease := leaseOneTask(t, h, connectorToken, connector.ID)
	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+errorTask.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  errorLease.LeaseNonce,
		Success:     false,
		Error: contracts.ConnectorTaskError{
			Code:      "gateway_timeout",
			Retryable: true,
		},
	})
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("complete task with error DTO: %d %s", w.Code, w.Body.String())
	}
	completed, err = st.GetConnectorTask(ctx, errorTask.ID)
	if err != nil {
		t.Fatalf("get failed task: %v", err)
	}
	if completed.Status != contracts.ConnectorTaskFailed ||
		completed.Error.Code != "gateway_timeout" ||
		!completed.Error.Retryable ||
		len(completed.Result) != 0 {
		t.Fatalf("unexpected failed task: %+v", completed)
	}
	assertConnectorTaskCompletionAudit(t, st, user.ID, errorTask.ID,
		"connector_task.complete.gateway.accounts.list", "failed", "gateway_timeout")

	mismatch := leaseTasks(t, h, connectorToken, connector.ID)
	if mismatch.Code != http.StatusOK {
		t.Fatalf("lease before runtime protocol mismatch: %d %s", mismatch.Code, mismatch.Body.String())
	}
	mismatch = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease", connectorToken, contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     connector.ID,
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		RuntimeState: contracts.ConnectorRuntimeState{
			ProtocolVersion: contracts.ConnectorProtocolVersion + 1,
		},
	})
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("runtime protocol mismatch must be rejected, got %d %s", mismatch.Code, mismatch.Body.String())
	}

	before, err := st.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{UserID: user.ID})
	if err != nil {
		t.Fatalf("list tasks before removed endpoint check: %v", err)
	}
	w = do(t, h, http.MethodPost, "/api/v1/connector-tasks", userToken, map[string]any{
		"type": contracts.ConnectorTaskGatewayAccountsList,
	})
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("public connector-task creation endpoint must not exist, got %d %s", w.Code, w.Body.String())
	}
	after, err := st.ListConnectorTasks(ctx, contracts.ConnectorTaskFilter{UserID: user.ID})
	if err != nil {
		t.Fatalf("list tasks after removed endpoint check: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("removed endpoint created a task: before=%d after=%d", len(before), len(after))
	}

	w = do(t, h, http.MethodGet, "/api/v1/connector-tasks", userToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list connector task summaries: %d %s", w.Code, w.Body.String())
	}
	var summaries []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decode connector task summaries: %v", err)
	}
	if len(summaries) == 0 {
		t.Fatal("expected connector task history")
	}
	for _, forbidden := range []string{"input", "result", "lease_nonce", "lease_owner", "idempotency_key", "user_id"} {
		if _, exists := summaries[0][forbidden]; exists {
			t.Fatalf("connector task summary exposed %q: %+v", forbidden, summaries[0])
		}
	}
}

func TestConnectorTaskSummaryProjectsLifecycleTargetsWithoutRawInput(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "lifecycle-summary@e2m.local", contracts.UserRoleOwner)
	token, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: user.ID, Name: "Lifecycle summary", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-lifecycle-summary", "connector-token-lifecycle-summary")
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Lifecycle summary", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatalf("create lifecycle pool: %v", err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{
		ID: "plan-summary", UserID: user.ID, InstanceID: instance.ID, PoolID: pool.ID,
		Status: contracts.RoutePlanPublished, SchedulingGeneration: 9,
	})
	if err != nil {
		t.Fatalf("create lifecycle route plan: %v", err)
	}
	raw := mustJSON(t, contracts.ConnectorGatewayAccountDeleteInput{
		AccountID: "account-summary", Ownership: contracts.GatewayAccountPlatformManaged,
		Fence: &contracts.GatewaySchedulingFence{
			Scope: "auto-switch/plan/" + plan.ID, Version: plan.SchedulingGeneration, Sequence: 2,
		},
	})
	_, err = st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: user.ID, InstanceID: instance.ID, ConnectorID: connector.ID,
		Type: contracts.ConnectorTaskGatewayAccountDelete, SchemaVersion: 1,
		Input: raw, IdempotencyKey: "private-idempotency-key", MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("create lifecycle task: %v", err)
	}

	w := do(t, h, http.MethodGet, "/api/v1/connector-tasks?instance_id="+instance.ID, token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list lifecycle summaries: %d %s", w.Code, w.Body.String())
	}
	var summaries []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &summaries); err != nil {
		t.Fatalf("decode lifecycle summaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0]["target_account_id"] != "account-summary" {
		t.Fatalf("unexpected lifecycle correlation fields: %+v", summaries)
	}
	fence, ok := summaries[0]["scheduling_fence"].(map[string]any)
	if !ok || fence["scope"] != "auto-switch/plan/"+plan.ID || fence["version"] != float64(9) || fence["sequence"] != float64(2) {
		t.Fatalf("unexpected safe scheduling fence summary: %+v", summaries)
	}
	for _, forbidden := range []string{"input", "result", "lease_nonce", "lease_owner", "idempotency_key", "user_id", "credential_binding_id"} {
		if _, exists := summaries[0][forbidden]; exists {
			t.Fatalf("lifecycle summary exposed %q: %+v", forbidden, summaries[0])
		}
		if strings.Contains(w.Body.String(), "binding-must-stay-private") || strings.Contains(w.Body.String(), "private-idempotency-key") {
			t.Fatal("lifecycle summary response exposed private task data")
		}
	}
}

func TestValidateQualityProbeCompletionUsesStrictTypedResult(t *testing.T) {
	valid := contracts.ConnectorTaskCompleteRequest{
		Success: true,
		Result: mustJSON(t, contracts.ConnectorGatewayQualityProbeResult{
			Success: false, Status: http.StatusTooManyRequests, ErrorType: contracts.ErrorRateLimit,
			FirstTokenMS: 100, TotalMS: 160, ObservedAt: time.Now().UTC(),
			Capability: contracts.QualityProbeTextStream, EndpointPath: contracts.QualityProbeEndpointResponses,
		}),
	}
	if err := validateConnectorCompletion(contracts.ConnectorTaskGatewayQualityProbe, &valid); err != nil {
		t.Fatalf("valid quality probe result: %v", err)
	}

	invalid := contracts.ConnectorTaskCompleteRequest{
		Success: true,
		Result:  json.RawMessage(`{"success":true,"status":200,"observed_at":"2026-07-13T12:00:00Z","url":"https://must-not-cross"}`),
	}
	if err := validateConnectorCompletion(contracts.ConnectorTaskGatewayQualityProbe, &invalid); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("loose quality result error=%v", err)
	}

	invalid = contracts.ConnectorTaskCompleteRequest{
		Success: true,
		Result: mustJSON(t, contracts.ConnectorGatewayQualityProbeResult{
			Success: true, Status: 200, ErrorType: contracts.ErrorNetwork, ObservedAt: time.Now().UTC(),
			Capability: contracts.QualityProbeTextStream, EndpointPath: contracts.QualityProbeEndpointResponses,
		}),
	}
	if err := validateConnectorCompletion(contracts.ConnectorTaskGatewayQualityProbe, &invalid); err == nil || !strings.Contains(err.Error(), "cannot include error_type") {
		t.Fatalf("inconsistent quality result error=%v", err)
	}

	for _, result := range []contracts.ConnectorGatewayQualityProbeResult{
		{Success: true, Status: http.StatusInternalServerError, ObservedAt: time.Now().UTC()},
		{Success: true, Status: http.StatusOK, TotalMS: 100, ObservedAt: time.Now().UTC()},
		{Success: true, Status: http.StatusOK, FirstTokenMS: 50, ObservedAt: time.Now().UTC()},
		{Success: false, Status: http.StatusServiceUnavailable, ObservedAt: time.Now().UTC()},
	} {
		invalid = contracts.ConnectorTaskCompleteRequest{Success: true, Result: mustJSON(t, result)}
		if err := validateConnectorCompletion(contracts.ConnectorTaskGatewayQualityProbe, &invalid); err == nil {
			t.Fatalf("contradictory quality result accepted: %+v", result)
		}
	}
}

func TestValidateBindingProofCompletionRequiresCanonicalDigestOnly(t *testing.T) {
	valid := contracts.ConnectorTaskCompleteRequest{
		Success: true,
		Result:  mustJSON(t, contracts.ConnectorGatewayBindingProofResult{Proof: strings.Repeat("a", contracts.ConnectorGatewayBindingProofHexLength)}),
	}
	if err := validateConnectorCompletion(contracts.ConnectorTaskGatewayBindingProof, &valid); err != nil {
		t.Fatalf("valid binding proof: %v", err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"proof":"abcd"}`),
		json.RawMessage(`{"proof":"` + strings.Repeat("A", contracts.ConnectorGatewayBindingProofHexLength) + `"}`),
		json.RawMessage(`{"proof":"` + strings.Repeat("a", contracts.ConnectorGatewayBindingProofHexLength) + `","key":"must-not-cross"}`),
	} {
		request := contracts.ConnectorTaskCompleteRequest{Success: true, Result: raw}
		if err := validateConnectorCompletion(contracts.ConnectorTaskGatewayBindingProof, &request); err == nil {
			t.Fatalf("invalid binding proof accepted: %s", raw)
		}
	}
}

func TestConnectorLeaseSanitizesReportedRuntimeState(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "runtime-boundary@e2m.local", contracts.UserRoleOwner)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Runtime boundary gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-token-runtime-boundary"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-runtime-boundary", connectorToken)

	endpointURL := "https://gateway.example.test/admin?token=runtime-url-secret"
	bearer := "Bearer runtime-bearer-secret"
	rawBody := `{"authorization":"Bearer raw-body-secret","body":"runtime-raw-body-secret"}`
	w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease", connectorToken, contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     connector.ID,
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		Version:         "1.0.0",
		RuntimeState: contracts.ConnectorRuntimeState{
			ProtocolVersion:   contracts.ConnectorProtocolVersion,
			GatewayConfigured: true,
			GatewayKind:       endpointURL,
			GatewayStatus:     rawBody,
			ErrorCode:         bearer,
			Capabilities: []contracts.ConnectorTaskType{
				contracts.ConnectorTaskGatewayHealth,
				contracts.ConnectorTaskType(endpointURL),
				contracts.ConnectorTaskGatewayHealth,
				contracts.ConnectorTaskGatewaySwitch,
			},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("lease with untrusted runtime state: %d %s", w.Code, w.Body.String())
	}
	assertNoConnectorLeak(t, w.Body.Bytes(), endpointURL, bearer, "runtime-url-secret", "runtime-bearer-secret", "raw-body-secret", "runtime-raw-body-secret")

	persisted, err := st.GetConnector(ctx, connector.ID)
	if err != nil {
		t.Fatalf("get connector after runtime report: %v", err)
	}
	if persisted.Version != "1.0.0" {
		t.Fatalf("unexpected connector version: %q", persisted.Version)
	}
	if persisted.Gateway.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		!persisted.Gateway.GatewayConfigured || persisted.Gateway.GatewayKind != "" ||
		persisted.Gateway.GatewayStatus != "" || persisted.Gateway.ErrorCode != "" {
		t.Fatalf("unsafe runtime strings were not cleared: %+v", persisted.Gateway)
	}
	wantCapabilities := []contracts.ConnectorTaskType{
		contracts.ConnectorTaskGatewayHealth,
		contracts.ConnectorTaskGatewaySwitch,
	}
	assertConnectorCapabilities(t, persisted.Gateway.Capabilities, wantCapabilities)
	assertNoConnectorLeak(t, persisted, endpointURL, bearer, "runtime-url-secret", "runtime-bearer-secret", "raw-body-secret", "runtime-raw-body-secret")

	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease", connectorToken, contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     connector.ID,
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		Version:         "1.2.3+build.4",
		RuntimeState: contracts.ConnectorRuntimeState{
			ProtocolVersion: contracts.ConnectorProtocolVersion,
			GatewayKind:     " NEWAPI ",
			GatewayStatus:   " OK ",
			ErrorCode:       " GATEWAY_UNAVAILABLE ",
			Capabilities: []contracts.ConnectorTaskType{
				contracts.ConnectorTaskGatewayHealth,
				contracts.ConnectorTaskGatewayAccountsList,
				contracts.ConnectorTaskGatewaySchedulableSet,
				contracts.ConnectorTaskGatewaySwitch,
				contracts.ConnectorTaskGatewaySchedulingBarrier,
				contracts.ConnectorTaskType("gateway.raw.response"),
			},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("lease with allowed runtime state: %d %s", w.Code, w.Body.String())
	}
	persisted, err = st.GetConnector(ctx, connector.ID)
	if err != nil {
		t.Fatalf("get connector after allowed runtime report: %v", err)
	}
	if persisted.Version != "1.2.3+build.4" ||
		persisted.Gateway.GatewayKind != "newapi" || persisted.Gateway.GatewayStatus != "ok" ||
		persisted.Gateway.ErrorCode != "gateway_unavailable" {
		t.Fatalf("allowed runtime state was not normalized: %+v", persisted)
	}
	assertConnectorCapabilities(t, persisted.Gateway.Capabilities, []contracts.ConnectorTaskType{
		contracts.ConnectorTaskGatewayHealth,
		contracts.ConnectorTaskGatewayAccountsList,
		contracts.ConnectorTaskGatewaySchedulableSet,
		contracts.ConnectorTaskGatewaySwitch,
		contracts.ConnectorTaskGatewaySchedulingBarrier,
	})
}

func TestStoredProtocolV2ConnectorMustHandshakeBeforeLeasingV3Work(t *testing.T) {
	srv, rawStore, authSvc := newTestServer(t)
	st := rawStore.(*store.MemoryStore)
	compatibilityStore := &httpProtocolUpgradeStore{Store: st, protocolVersion: 2}
	srv.store = compatibilityStore
	h := srv.Routes()
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, testUserEmail(t, "v2-http-owner"), contracts.UserRoleClient)
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: owner.ID, Name: "v2 HTTP upgrade", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	connectorToken := "connector-v2-http-token"
	_, connector := createStoredConnector(t, st, owner.ID, instance, "connector-v2-http", connectorToken)
	task := createAccountsTask(t, st, owner.ID, instance.ID, connector.ID, "v2-http-task", 3)

	stored, err := compatibilityStore.GetConnectorByTokenHash(ctx, hashConnectorToken(connectorToken))
	if err != nil || stored.ProtocolVersion != 2 || stored.Gateway.ProtocolVersion != 2 {
		t.Fatalf("preserved v2 token identity=%+v err=%v", stored, err)
	}
	// A real v2 request is rejected before heartbeat upgrade and cannot lease.
	w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease", connectorToken, contracts.ConnectorTaskLeaseRequest{
		ConnectorID: connector.ID, Version: "0.2.0", ProtocolVersion: 2,
		RuntimeState: contracts.ConnectorRuntimeState{ProtocolVersion: 2},
	})
	if w.Code != http.StatusUpgradeRequired {
		t.Fatalf("v2 lease=%d %s", w.Code, w.Body.String())
	}
	persisted, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil || persisted.Status != contracts.ConnectorTaskPending || persisted.Attempts != 0 {
		t.Fatalf("rejected v2 lease changed task=%+v err=%v", persisted, err)
	}

	// The first genuine v3 runtime handshake atomically upgrades the stored
	// identity in RecordConnectorSeen, then leasing is allowed in the same call.
	w = leaseTasks(t, h, connectorToken, connector.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("v3 upgrade lease=%d %s", w.Code, w.Body.String())
	}
	var response contracts.ConnectorTaskLeaseResponse
	decodeResponse(t, w, &response)
	if len(response.Tasks) != 1 || response.Tasks[0].ID != task.ID {
		t.Fatalf("tasks after v3 upgrade=%+v", response.Tasks)
	}
	stored, err = compatibilityStore.GetConnector(ctx, connector.ID)
	if err != nil || stored.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		stored.Gateway.ProtocolVersion != contracts.ConnectorProtocolVersion {
		t.Fatalf("stored identity was not atomically upgraded=%+v err=%v", stored, err)
	}
}

// httpProtocolUpgradeStore models a preserved protocol-v2 row at the HTTP
// boundary while the Memory Store package separately tests its concrete row
// behavior. RecordConnectorSeen is the only transition that upgrades it.
type httpProtocolUpgradeStore struct {
	store.Store
	mu              sync.Mutex
	protocolVersion int
}

func (s *httpProtocolUpgradeStore) GetConnectorByTokenHash(ctx context.Context, tokenHash string) (contracts.Connector, error) {
	connector, err := s.Store.GetConnectorByTokenHash(ctx, tokenHash)
	return s.project(connector), err
}

func (s *httpProtocolUpgradeStore) GetConnector(ctx context.Context, id string) (contracts.Connector, error) {
	connector, err := s.Store.GetConnector(ctx, id)
	return s.project(connector), err
}

func (s *httpProtocolUpgradeStore) RecordConnectorSeen(ctx context.Context, id, version string, runtime contracts.ConnectorRuntimeState) (contracts.Connector, error) {
	connector, err := s.Store.RecordConnectorSeen(ctx, id, version, runtime)
	if err != nil {
		return connector, err
	}
	s.mu.Lock()
	s.protocolVersion = contracts.ConnectorProtocolVersion
	s.mu.Unlock()
	return s.project(connector), nil
}

func (s *httpProtocolUpgradeStore) project(connector contracts.Connector) contracts.Connector {
	s.mu.Lock()
	defer s.mu.Unlock()
	connector.ProtocolVersion = s.protocolVersion
	connector.Gateway.ProtocolVersion = s.protocolVersion
	return connector
}

func TestValidateConnectorCompletionAcceptsSchedulingBarrierResult(t *testing.T) {
	req := contracts.ConnectorTaskCompleteRequest{Success: true, Result: json.RawMessage(`{}`)}
	if err := validateConnectorCompletion(contracts.ConnectorTaskGatewaySchedulingBarrier, &req); err != nil {
		t.Fatalf("valid scheduling barrier completion: %v", err)
	}
	if string(req.Result) != `{}` {
		t.Fatalf("canonical barrier result = %s", req.Result)
	}

	invalid := contracts.ConnectorTaskCompleteRequest{Success: true, Result: json.RawMessage(`{"gateway_called":false}`)}
	if err := validateConnectorCompletion(contracts.ConnectorTaskGatewaySchedulingBarrier, &invalid); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected strict barrier result error, got %v", err)
	}
}

func TestConnectorCompletionDropsReportedMessageFromTaskAndAudit(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "completion-boundary@e2m.local", contracts.UserRoleOwner)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Completion boundary gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-token-completion-boundary"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-completion-boundary", connectorToken)
	task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "completion-message-boundary", 1)
	leased := leaseOneTask(t, h, connectorToken, connector.ID)

	endpointURL := "https://gateway.example.test/admin?token=completion-url-secret"
	bearer := "Bearer completion-bearer-secret"
	rawBody := `{"error":"completion-raw-body-secret","authorization":"Bearer raw-body-token"}`
	reportedMessage := endpointURL + "\nAuthorization: " + bearer + "\nraw body: " + rawBody
	w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, map[string]any{
		"connector_id": connector.ID,
		"lease_nonce":  leased.LeaseNonce,
		"success":      false,
		"error": map[string]any{
			"code":      "gateway_timeout",
			"message":   reportedMessage,
			"retryable": true,
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("completion message must be rejected: %d %s", w.Code, w.Body.String())
	}
	assertNoConnectorLeak(t, w.Body.Bytes(), endpointURL, bearer, "completion-url-secret", "completion-bearer-secret", "completion-raw-body-secret", "raw-body-token")

	persisted, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get completed connector task: %v", err)
	}
	if persisted.Status != contracts.ConnectorTaskLeased || persisted.Error.Code != "" {
		t.Fatalf("rejected connector error changed task: %+v", persisted)
	}
	assertNoConnectorLeak(t, persisted, endpointURL, bearer, "completion-url-secret", "completion-bearer-secret", "completion-raw-body-secret", "raw-body-token")

	audits, err := st.ListAudits(ctx, user.ID)
	if err != nil {
		t.Fatalf("list connector completion audits: %v", err)
	}
	for _, audit := range audits {
		if audit.TargetType != "connector_task" || audit.TargetID != task.ID {
			continue
		}
		t.Fatal("rejected connector completion wrote an audit")
	}
	assertNoConnectorLeak(t, audits, endpointURL, bearer, "completion-url-secret", "completion-bearer-secret", "completion-raw-body-secret", "raw-body-token")
}

func TestConnectorTaskCompletionRejectsStaleLeaseNonce(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "stale-lease@e2m.local", contracts.UserRoleOwner)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Stale lease gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-token-stale-lease"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-stale-lease", connectorToken)
	task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "stale-lease-task", 3)

	first := leaseOneTask(t, h, connectorToken, connector.ID)
	w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  first.LeaseNonce,
		Success:     false,
		Error: contracts.ConnectorTaskError{
			Code:      "gateway_timeout",
			Retryable: true,
		},
	})
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("retry first attempt: %d %s", w.Code, w.Body.String())
	}
	retried, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get retried task: %v", err)
	}
	if retried.Status != contracts.ConnectorTaskPending || retried.LeaseNonce != "" || retried.LeaseUntil != nil {
		t.Fatalf("retry did not clear lease state: %+v", retried)
	}
	time.Sleep(time.Until(retried.AvailableAt) + 10*time.Millisecond)

	second := leaseOneTask(t, h, connectorToken, connector.ID)
	if second.LeaseNonce == first.LeaseNonce {
		t.Fatal("re-lease reused the stale attempt nonce")
	}
	result := mustJSON(t, contracts.ConnectorGatewayAccountsListResult{Accounts: []contracts.GatewayAccount{}})
	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  first.LeaseNonce,
		Success:     true,
		Result:      result,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale completion must be rejected: %d %s", w.Code, w.Body.String())
	}
	persisted, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task after stale completion: %v", err)
	}
	if persisted.Status != contracts.ConnectorTaskLeased || persisted.LeaseNonce != second.LeaseNonce || len(persisted.Result) != 0 {
		t.Fatalf("stale completion changed the current attempt: %+v", persisted)
	}

	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		Success:     true,
		Result:      result,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing lease nonce must be rejected: %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, contracts.ConnectorTaskCompleteRequest{
		ConnectorID: connector.ID,
		LeaseNonce:  second.LeaseNonce,
		Success:     true,
		Result:      result,
	})
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("complete current attempt: %d %s", w.Code, w.Body.String())
	}
	completed, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if completed.Status != contracts.ConnectorTaskSucceeded || completed.LeaseNonce != "" || completed.LeaseUntil != nil {
		t.Fatalf("current completion did not clear lease state: %+v", completed)
	}
}

func TestBoundInstanceConnectorReinstallReusesIdentity(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "connector-reinstall@e2m.local", contracts.UserRoleOwner)
	userToken, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Reinstall gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	oldToken := "connector-token-before-reinstall"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-reinstall", oldToken)

	w := do(t, h, http.MethodPost, "/api/v1/instances/"+instance.ID+"/connector-install", userToken, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create reinstall enrollment: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)
	var install contracts.CreateConnectorEnrollmentResponse
	decodeResponse(t, w, &install)
	if install.Enrollment.ConnectorID != connector.ID || install.InstallGuide.ConnectorID != connector.ID {
		t.Fatalf("reinstall changed connector identity: connector=%s install=%+v", connector.ID, install)
	}
	assertBootstrapOnlyInstall(t, install, instance.ID)

	w = do(t, h, http.MethodPost, "/api/v1/connectors/enroll", "", map[string]any{
		"enrollment_token": install.Token,
		"connector_id":     connector.ID,
		"instance_id":      instance.ID,
		"version":          "0.1.0-test",
		"protocol_version": contracts.ConnectorProtocolVersion,
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("enroll reinstalled connector: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)
	var enrolled contracts.ConnectorEnrollResponse
	decodeResponse(t, w, &enrolled)
	if enrolled.ProtocolVersion != contracts.ConnectorProtocolVersion ||
		enrolled.Connector.ID != connector.ID ||
		enrolled.Connector.InstanceID != instance.ID ||
		enrolled.ConnectorToken == "" {
		t.Fatalf("reinstall did not preserve the exact v1 identity: %+v", enrolled)
	}
	if w := leaseTasks(t, h, oldToken, connector.ID); w.Code != http.StatusUnauthorized {
		t.Fatalf("pre-reinstall connector token must be replaced, got %d %s", w.Code, w.Body.String())
	}
}

func TestInstanceConnectorInstallReplacesUnusedEnrollment(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "connector-install-reissue@e2m.local", contracts.UserRoleOwner)
	userToken, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Interrupted install gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	firstResponse := do(t, h, http.MethodPost, "/api/v1/instances/"+instance.ID+"/connector-install", userToken, nil)
	if firstResponse.Code != http.StatusCreated {
		t.Fatalf("create first install: %d %s", firstResponse.Code, firstResponse.Body.String())
	}
	var first contracts.CreateConnectorEnrollmentResponse
	decodeResponse(t, firstResponse, &first)

	secondResponse := do(t, h, http.MethodPost, "/api/v1/instances/"+instance.ID+"/connector-install", userToken, nil)
	if secondResponse.Code != http.StatusCreated {
		t.Fatalf("replace unused install: %d %s", secondResponse.Code, secondResponse.Body.String())
	}
	var second contracts.CreateConnectorEnrollmentResponse
	decodeResponse(t, secondResponse, &second)
	if second.Token == first.Token || second.Enrollment.ID == first.Enrollment.ID {
		t.Fatalf("replacement reused one-time material: first=%+v second=%+v", first.Enrollment, second.Enrollment)
	}

	oldEnrollment := do(t, h, http.MethodPost, "/api/v1/connectors/enroll", "", map[string]any{
		"enrollment_token": first.Token,
		"connector_id":     first.Enrollment.ConnectorID,
		"instance_id":      instance.ID,
		"version":          "0.1.0-test",
		"protocol_version": contracts.ConnectorProtocolVersion,
	})
	if oldEnrollment.Code != http.StatusUnauthorized {
		t.Fatalf("replaced token still works: %d %s", oldEnrollment.Code, oldEnrollment.Body.String())
	}

	newEnrollment := do(t, h, http.MethodPost, "/api/v1/connectors/enroll", "", map[string]any{
		"enrollment_token": second.Token,
		"connector_id":     second.Enrollment.ConnectorID,
		"instance_id":      instance.ID,
		"version":          "0.1.0-test",
		"protocol_version": contracts.ConnectorProtocolVersion,
	})
	if newEnrollment.Code != http.StatusCreated {
		t.Fatalf("replacement token failed: %d %s", newEnrollment.Code, newEnrollment.Body.String())
	}
}

func TestTypedConnectorTaskCompletionRejectsUnknownAndRawResponse(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "typed-rejection@e2m.local", contracts.UserRoleOwner)
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Strict DTO gateway",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-token-strict-dto"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-strict", connectorToken)

	t.Run("unknown result field", func(t *testing.T) {
		task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "unknown-result", 3)
		leased := leaseOneTask(t, h, connectorToken, connector.ID)
		w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, map[string]any{
			"connector_id": connector.ID,
			"lease_nonce":  leased.LeaseNonce,
			"success":      true,
			"result": map[string]any{
				"accounts":         []any{},
				"unexpected_field": true,
			},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unknown typed result field must be rejected, got %d %s", w.Code, w.Body.String())
		}
		assertTaskStillLeasedWithoutResult(t, st, task.ID)
	})

	t.Run("legacy raw response", func(t *testing.T) {
		task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "raw-response", 3)
		leased := leaseOneTask(t, h, connectorToken, connector.ID)
		w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, map[string]any{
			"connector_id": connector.ID,
			"lease_nonce":  leased.LeaseNonce,
			"success":      true,
			"response": map[string]any{
				"status": http.StatusOK,
				"body":   `{"accounts":[]}`,
			},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("legacy raw response must be rejected, got %d %s", w.Code, w.Body.String())
		}
		assertTaskStillLeasedWithoutResult(t, st, task.ID)
	})

	t.Run("success without result", func(t *testing.T) {
		task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "missing-result", 3)
		leased := leaseOneTask(t, h, connectorToken, connector.ID)
		w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, map[string]any{
			"connector_id": connector.ID,
			"lease_nonce":  leased.LeaseNonce,
			"success":      true,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("successful completion without result must be rejected, got %d %s", w.Code, w.Body.String())
		}
		assertTaskStillLeasedWithoutResult(t, st, task.ID)
	})

	t.Run("accounts missing array", func(t *testing.T) {
		task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "missing-accounts", 3)
		leased := leaseOneTask(t, h, connectorToken, connector.ID)
		w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, map[string]any{
			"connector_id": connector.ID,
			"lease_nonce":  leased.LeaseNonce,
			"success":      true,
			"result":       map[string]any{},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("accounts result without array must be rejected, got %d %s", w.Code, w.Body.String())
		}
		assertTaskStillLeasedWithoutResult(t, st, task.ID)
	})

	t.Run("failed completion with result", func(t *testing.T) {
		task := createAccountsTask(t, st, user.ID, instance.ID, connector.ID, "failure-with-result", 3)
		leased := leaseOneTask(t, h, connectorToken, connector.ID)
		w := do(t, h, http.MethodPost, "/api/v1/connectors/tasks/"+task.ID+"/complete", connectorToken, map[string]any{
			"connector_id": connector.ID,
			"lease_nonce":  leased.LeaseNonce,
			"success":      false,
			"result":       map[string]any{"accounts": []any{}},
			"error":        map[string]any{"code": "failed"},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("failed completion with result must be rejected, got %d %s", w.Code, w.Body.String())
		}
		assertTaskStillLeasedWithoutResult(t, st, task.ID)
	})
}

func TestConnectorTokenRotateAndRevoke(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "rotate-revoke@e2m.local", contracts.UserRoleOwner)
	userToken, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Rotating connector",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	oldToken := "connector-token-before-rotation"
	_, connector := createStoredConnector(t, st, user.ID, instance, "connector-rotate", oldToken)

	w := do(t, h, http.MethodPost, "/api/v1/connectors/"+connector.ID+"/rotate-token", userToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("rotate connector token: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)
	var rotated contracts.RotateConnectorTokenResponse
	decodeResponse(t, w, &rotated)
	if rotated.ConnectorToken == "" || rotated.ConnectorToken == oldToken || rotated.Connector.Status != contracts.ConnectorStatusOffline {
		t.Fatalf("unexpected rotation response: %+v", rotated)
	}
	if strings.Contains(w.Body.String(), "token_hash") {
		t.Fatalf("connector token hash leaked: %s", w.Body.String())
	}

	w = leaseTasks(t, h, oldToken, connector.ID)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old token must stop working after rotation, got %d %s", w.Code, w.Body.String())
	}
	w = leaseTasks(t, h, rotated.ConnectorToken, connector.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("rotated token must work, got %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, http.MethodPost, "/api/v1/connectors/"+connector.ID+"/revoke", userToken, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke connector: %d %s", w.Code, w.Body.String())
	}
	var revoked contracts.Connector
	decodeResponse(t, w, &revoked)
	if revoked.Status != contracts.ConnectorStatusRevoked || revoked.RevokedAt == nil {
		t.Fatalf("unexpected revoked connector: %+v", revoked)
	}
	w = leaseTasks(t, h, rotated.ConnectorToken, connector.ID)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked connector token must stop working, got %d %s", w.Code, w.Body.String())
	}
}

func TestPlatformAdminCanBindAndUnbindInstanceConnector(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	owner := createLoginUser(t, authSvc, "connector-binding-owner@e2m.local", contracts.UserRoleOwner)
	otherOwner := createLoginUser(t, authSvc, "connector-binding-other@e2m.local", contracts.UserRoleOwner)
	admin := createLoginUser(t, authSvc, "connector-binding-admin@e2m.local", contracts.UserRolePlatformAdmin)
	otherToken, _, err := authSvc.Login(ctx, otherOwner.Email, "password123")
	if err != nil {
		t.Fatalf("login other owner: %v", err)
	}
	adminToken, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("login platform admin: %v", err)
	}
	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: owner.ID,
		Name:   "Admin-managed connector",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	_, connector := createStoredConnector(t, st, owner.ID, instance, "connector-admin-binding", "connector-binding-token")

	w := do(t, h, http.MethodPut, "/api/v1/instances/"+instance.ID+"/connector", adminToken, map[string]any{
		"connector_id": "",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("platform admin unbind connector: %d %s", w.Code, w.Body.String())
	}
	var unbound contracts.Instance
	decodeResponse(t, w, &unbound)
	if unbound.ConnectorID != "" {
		t.Fatalf("empty connector_id must unbind instance, got %+v", unbound)
	}

	w = do(t, h, http.MethodPut, "/api/v1/instances/"+instance.ID+"/connector", adminToken, map[string]any{
		"connector_id": connector.ID,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("platform admin bind connector: %d %s", w.Code, w.Body.String())
	}
	var bound contracts.Instance
	decodeResponse(t, w, &bound)
	if bound.ConnectorID != connector.ID {
		t.Fatalf("connector was not bound: %+v", bound)
	}

	w = do(t, h, http.MethodPut, "/api/v1/instances/"+instance.ID+"/connector", otherToken, map[string]any{
		"connector_id": "",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-account owner must not unbind connector: %d %s", w.Code, w.Body.String())
	}
	persisted, err := st.GetInstance(ctx, instance.ID)
	if err != nil {
		t.Fatalf("get instance after denied unbind: %v", err)
	}
	if persisted.ConnectorID != connector.ID {
		t.Fatalf("denied unbind changed connector binding: %+v", persisted)
	}
}

func TestUserDisableDrainsBeforeRevokingConnectorLifecycle(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "connector-disabled-owner@example.com", contracts.UserRoleClient)
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: owner.ID, Name: "Disabled owner", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-disabled-token"
	_, connector := createStoredConnector(t, st, owner.ID, instance, "connector-disabled", connectorToken)
	task := createAccountsTask(t, st, owner.ID, instance.ID, connector.ID, "disable-lifecycle", 3)
	leased := leaseOneTask(t, h, connectorToken, connector.ID)
	if leased.ID != task.ID {
		t.Fatalf("leased task=%s, want %s", leased.ID, task.ID)
	}
	pendingEnrollmentToken := "connector-disabled-pending-enrollment"
	pending, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID: owner.ID, InstanceID: instance.ID, ConnectorID: connector.ID,
		TokenHash: hashConnectorToken(pendingEnrollmentToken), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("create pending enrollment: %v", err)
	}

	disabled := owner
	disabled.Enabled = false
	if _, err := st.UpdateUser(ctx, disabled); err != nil {
		t.Fatalf("disable owner: %v", err)
	}
	if w := leaseTasks(t, h, connectorToken, connector.ID); w.Code != http.StatusOK {
		t.Fatalf("draining owner connector must stay authenticated: got %d %s", w.Code, w.Body.String())
	}
	w := do(t, h, http.MethodPost, "/api/v1/connectors/enroll", "", map[string]any{
		"enrollment_token": pendingEnrollmentToken,
		"connector_id":     pending.ConnectorID,
		"instance_id":      pending.InstanceID,
		"version":          "0.1.0-test",
		"protocol_version": contracts.ConnectorProtocolVersion,
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("disabled owner enrollment: got %d %s", w.Code, w.Body.String())
	}
	persistedConnector, err := st.GetConnector(ctx, connector.ID)
	if err != nil || persistedConnector.Status == contracts.ConnectorStatusRevoked || persistedConnector.TokenHash == "" {
		t.Fatalf("drain channel was revoked too early: connector=%+v err=%v", persistedConnector, err)
	}
	persistedInstance, err := st.GetInstance(ctx, instance.ID)
	if err != nil || persistedInstance.ConnectorID != connector.ID {
		t.Fatalf("instance connector was unbound too early: instance=%+v err=%v", persistedInstance, err)
	}
	persistedTask, err := st.GetConnectorTask(ctx, task.ID)
	if err != nil || persistedTask.Status != contracts.ConnectorTaskLeased || persistedTask.LeaseNonce == "" {
		t.Fatalf("already-leased drain-safe task was invalidated: task=%+v err=%v", persistedTask, err)
	}
	updated, err := st.GetUser(ctx, owner.ID)
	if err != nil || updated.DeactivationStatus != contracts.UserDeactivationDraining {
		t.Fatalf("user deactivation state=%+v err=%v", updated, err)
	}
}

func assertBootstrapOnlyInstall(t *testing.T, install contracts.CreateConnectorEnrollmentResponse, instanceID string) {
	t.Helper()
	guide := install.InstallGuide
	if install.Token == "" || guide.InstanceID != instanceID || guide.ConnectorID == "" {
		t.Fatalf("install is not bound to the instance: %+v", install)
	}
	if guide.LocalConfigURL != "http://127.0.0.1:18081" || guide.LocalConfigPort != 18081 {
		t.Fatalf("local configuration UI must be loopback-only: %+v", guide)
	}
	if guide.DataVolumeName == "" || guide.DataVolumeName == guide.EnrollmentTokenFile {
		t.Fatalf("connector data needs an independent named volume: %+v", guide)
	}

	compose := guide.DockerComposeYAML
	for _, want := range []string{
		"E2M_CONNECTOR_ENROLL_TOKEN_FILE",
		"/run/secrets/e2m_connector_enrollment",
		guide.EnrollmentTokenFile,
		guide.DataVolumeName + ":/var/lib/e2m-agent",
		"127.0.0.1:18081:18081",
	} {
		if !strings.Contains(compose, want) {
			t.Fatalf("compose install is missing %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "E2M_USER_ID") || strings.Contains(guide.DockerRunCommand, "E2M_USER_ID") {
		t.Fatalf("install guide must not let the connector self-report user identity: %+v", guide)
	}
	if strings.Contains(compose, install.Token) || strings.Contains(compose, "E2M_CONNECTOR_ENROLL_TOKEN:") {
		t.Fatalf("compose must reference the enrollment token file, not inline the token:\n%s", compose)
	}
	if !strings.Contains(compose, "volumes:\n  "+guide.DataVolumeName+": {}") {
		t.Fatalf("compose does not declare an independent data volume:\n%s", compose)
	}

	serialized, err := json.Marshal(guide)
	if err != nil {
		t.Fatalf("marshal install guide: %v", err)
	}
	lower := strings.ToLower(string(serialized))
	for _, forbidden := range []string{
		"gateway_url",
		"gateway_base_url",
		"gateway_auth",
		"endpoint_url",
		"endpoint_ref",
		"admin_key",
		"api_key_file",
		"credential_ref",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("install guide contains forbidden gateway configuration %q: %s", forbidden, serialized)
		}
	}
}

func createStoredConnector(
	t *testing.T,
	st store.Store,
	userID int64,
	instance contracts.Instance,
	connectorID string,
	connectorToken string,
) (contracts.ConnectorEnrollment, contracts.Connector) {
	t.Helper()
	ctx := context.Background()
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID:      userID,
		InstanceID:  instance.ID,
		ConnectorID: connectorID,
		Name:        connectorID,
		TokenHash:   hashConnectorToken("enrollment-" + connectorToken),
	})
	if err != nil {
		t.Fatalf("create stored enrollment: %v", err)
	}
	connector, used, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID:              connectorID,
		InstanceID:      instance.ID,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       hashConnectorToken(connectorToken),
	})
	if err != nil {
		t.Fatalf("use stored enrollment: %v", err)
	}
	if used.UsedAt == nil || connector.InstanceID != instance.ID || connector.ID != connectorID {
		t.Fatalf("stored connector was not bound exactly: enrollment=%+v connector=%+v", used, connector)
	}
	return used, connector
}

func createAccountsTask(
	t *testing.T,
	st store.Store,
	userID int64,
	instanceID string,
	connectorID string,
	idempotencyKey string,
	maxAttempts int,
) contracts.ConnectorTask {
	t.Helper()
	task, err := st.CreateConnectorTask(context.Background(), contracts.ConnectorTask{
		UserID:         userID,
		InstanceID:     instanceID,
		ConnectorID:    connectorID,
		Type:           contracts.ConnectorTaskGatewayAccountsList,
		SchemaVersion:  1,
		Input:          mustJSON(t, contracts.ConnectorGatewayAccountsListInput{}),
		IdempotencyKey: idempotencyKey,
		MaxAttempts:    maxAttempts,
	})
	if err != nil {
		t.Fatalf("create accounts task: %v", err)
	}
	return task
}

func leaseTasks(t *testing.T, h http.Handler, token, connectorID string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease", token, contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     connectorID,
		MaxTasks:        1,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		RuntimeState: contracts.ConnectorRuntimeState{
			ProtocolVersion: contracts.ConnectorProtocolVersion,
		},
	})
}

func leaseOneTask(t *testing.T, h http.Handler, token, connectorID string) contracts.ConnectorTask {
	t.Helper()
	w := leaseTasks(t, h, token, connectorID)
	if w.Code != http.StatusOK {
		t.Fatalf("lease task: %d %s", w.Code, w.Body.String())
	}
	var response contracts.ConnectorTaskLeaseResponse
	decodeResponse(t, w, &response)
	if response.NextPollAfter != "5s" {
		t.Fatalf("next_poll_after = %q, want 5s", response.NextPollAfter)
	}
	if len(response.Tasks) != 1 || strings.TrimSpace(response.Tasks[0].LeaseNonce) == "" {
		t.Fatalf("expected one task with a lease nonce, got %+v", response.Tasks)
	}
	return response.Tasks[0]
}

func assertConnectorTaskCompletionAudit(
	t *testing.T,
	st store.Store,
	userID int64,
	taskID, action, result, errorCode string,
) {
	t.Helper()
	audits, err := st.ListAudits(context.Background(), userID)
	if err != nil {
		t.Fatalf("list connector task audits: %v", err)
	}
	for _, audit := range audits {
		if audit.TargetType == "connector_task" && audit.TargetID == taskID {
			if !audit.EventLevel.Valid() {
				t.Fatalf("connector task audit missing event level: %+v", audit)
			}
			if audit.Action != action || audit.Result != result || audit.ErrorMessage != errorCode {
				t.Fatalf("connector task audit=%+v want action=%q result=%q error=%q",
					audit, action, result, errorCode)
			}
			return
		}
	}
	t.Fatalf("connector task audit not found for %q", taskID)
}

func assertTaskStillLeasedWithoutResult(t *testing.T, st store.Store, taskID string) {
	t.Helper()
	task, err := st.GetConnectorTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get rejected task: %v", err)
	}
	if task.Status != contracts.ConnectorTaskLeased || len(task.Result) != 0 {
		t.Fatalf("rejected completion changed task state: %+v", task)
	}
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return raw
}

func decodeResponse(t *testing.T, w *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func requireNoStore(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if value := w.Header().Get("Cache-Control"); !strings.Contains(strings.ToLower(value), "no-store") {
		t.Fatalf("sensitive response must be no-store, got Cache-Control %q", value)
	}
}

func assertConnectorCapabilities(t *testing.T, got, want []contracts.ConnectorTaskType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("unexpected connector capabilities: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected connector capabilities: got=%v want=%v", got, want)
		}
	}
}

func assertNoConnectorLeak(t *testing.T, value any, forbidden ...string) {
	t.Helper()
	var serialized string
	switch value := value.(type) {
	case string:
		serialized = value
	case []byte:
		serialized = string(value)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal value for leak check: %v", err)
		}
		serialized = string(raw)
	}
	for _, marker := range forbidden {
		if marker != "" && strings.Contains(serialized, marker) {
			t.Fatalf("connector-supplied value %q leaked into %s", marker, serialized)
		}
	}
}

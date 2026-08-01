package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
)

func TestConnectorTaskExecutionResolutionRequiresPlatformAdminAndWritesAtomicAudit(t *testing.T) {
	tests := []struct {
		name       string
		resolution contracts.ConnectorTaskExecutionResolution
		result     json.RawMessage
		wantStatus contracts.ConnectorTaskStatus
		wantError  string
		revoke     bool
	}{
		{name: "confirmed applied", resolution: contracts.ConnectorTaskExecutionConfirmedApplied, result: json.RawMessage(`{"remote_id":"account-plan-fence"}`), wantStatus: contracts.ConnectorTaskSucceeded},
		{name: "confirmed not applied", resolution: contracts.ConnectorTaskExecutionConfirmedNotApplied, wantStatus: contracts.ConnectorTaskFailed, wantError: "execution_abandoned"},
		{name: "revoked unverifiable", resolution: contracts.ConnectorTaskExecutionRevokedUnverifiable, wantStatus: contracts.ConnectorTaskFailed, wantError: "execution_outcome_unknown", revoke: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, rawStore, authSvc := newTestServer(t)
			st := rawStore.(*store.MemoryStore)
			h := srv.Routes()
			ctx := context.Background()
			owner := createLoginUser(t, authSvc, testUserEmail(t, "resolve-owner"), contracts.UserRoleClient)
			admin := createLoginUser(t, authSvc, testUserEmail(t, "resolve-admin"), contracts.UserRolePlatformAdmin)
			ownerToken, _, _ := authSvc.Login(ctx, owner.Email, "password123")
			adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
			instance, connector, task, leased := createExecutingHTTPConnectorTask(t, st, owner.ID)

			path := "/api/v1/connector-tasks/" + task.ID + "/resolve-execution"
			body := contracts.ConnectorTaskExecutionResolveRequest{
				LeaseNonce: leased.LeaseNonce, Resolution: test.resolution,
				EvidenceNote: "remote gateway state verified by operator", Result: test.result,
			}
			if w := do(t, h, http.MethodPost, path, "", body); w.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated resolve=%d %s", w.Code, w.Body.String())
			}
			if w := do(t, h, http.MethodPost, path, ownerToken, body); w.Code != http.StatusForbidden {
				t.Fatalf("owner resolve=%d %s", w.Code, w.Body.String())
			}
			if persisted, err := st.GetConnectorTask(ctx, task.ID); err != nil || persisted.Status != contracts.ConnectorTaskExecuting {
				t.Fatalf("denied resolve changed task=%+v err=%v", persisted, err)
			}
			if test.revoke {
				if _, err := st.RevokeConnector(ctx, connector.ID); err != nil {
					t.Fatal(err)
				}
			}
			w := do(t, h, http.MethodPost, path, adminToken, body)
			if w.Code != http.StatusOK {
				t.Fatalf("admin resolve=%d %s", w.Code, w.Body.String())
			}
			requireNoStore(t, w)
			var summary map[string]any
			decodeResponse(t, w, &summary)
			for _, forbidden := range []string{"input", "result", "lease_nonce", "lease_owner", "idempotency_key", "user_id"} {
				if _, exists := summary[forbidden]; exists {
					t.Fatalf("resolution response leaked %s: %+v", forbidden, summary)
				}
			}
			persisted, err := st.GetConnectorTask(ctx, task.ID)
			if err != nil || persisted.Status != test.wantStatus || persisted.Error.Code != test.wantError ||
				persisted.LeaseNonce != "" || persisted.LeaseOwner != "" || persisted.LeaseUntil != nil {
				t.Fatalf("resolved task=%+v err=%v", persisted, err)
			}
			if test.resolution == contracts.ConnectorTaskExecutionConfirmedApplied && string(persisted.Result) != string(test.result) {
				t.Fatalf("canonical applied result=%s", persisted.Result)
			}
			audits, err := st.ListAudits(ctx, owner.ID)
			if err != nil || len(audits) != 1 {
				t.Fatalf("atomic audit count=%d err=%v", len(audits), err)
			}
			audit := audits[0]
			wantHash := sha256.Sum256([]byte(leased.LeaseNonce))
			if audit.UserID != owner.ID || audit.InstanceID != instance.ID || audit.ActorID != admin.Email ||
				audit.Action != "connector_task.resolve_execution" || audit.RiskLevel != contracts.RiskLevelL3 ||
				audit.EventLevel != contracts.EventLevelCritical || audit.Details["lease_nonce_sha256"] != fmt.Sprintf("sha256:%x", wantHash) ||
				audit.Details["lease_nonce"] != "" || strings.Contains(mustJSONString(t, audit), leased.LeaseNonce) {
				t.Fatalf("unsafe or incomplete resolution audit=%+v", audit)
			}
		})
	}
}

func TestConnectorTaskExecutionResolutionRejectsInvalidClaimsWithoutMutation(t *testing.T) {
	tests := []struct {
		name string
		body func(contracts.ConnectorTask) any
		want int
	}{
		{name: "wrong nonce", want: http.StatusConflict, body: func(contracts.ConnectorTask) any {
			return contracts.ConnectorTaskExecutionResolveRequest{LeaseNonce: "wrong", Resolution: contracts.ConnectorTaskExecutionConfirmedNotApplied, EvidenceNote: "verified not applied"}
		}},
		{name: "unknown field", want: http.StatusBadRequest, body: func(task contracts.ConnectorTask) any {
			return map[string]any{"lease_nonce": task.LeaseNonce, "resolution": contracts.ConnectorTaskExecutionConfirmedNotApplied, "evidence_note": "verified not applied", "unknown": true}
		}},
		{name: "unsafe evidence", want: http.StatusBadRequest, body: func(task contracts.ConnectorTask) any {
			return contracts.ConnectorTaskExecutionResolveRequest{LeaseNonce: task.LeaseNonce, Resolution: contracts.ConnectorTaskExecutionConfirmedNotApplied, EvidenceNote: "Authorization: Bearer secret"}
		}},
		{name: "non-applied result", want: http.StatusBadRequest, body: func(task contracts.ConnectorTask) any {
			return contracts.ConnectorTaskExecutionResolveRequest{LeaseNonce: task.LeaseNonce, Resolution: contracts.ConnectorTaskExecutionConfirmedNotApplied, EvidenceNote: "verified not applied", Result: json.RawMessage(`{}`)}
		}},
		{name: "invalid applied result", want: http.StatusBadRequest, body: func(task contracts.ConnectorTask) any {
			return contracts.ConnectorTaskExecutionResolveRequest{LeaseNonce: task.LeaseNonce, Resolution: contracts.ConnectorTaskExecutionConfirmedApplied, EvidenceNote: "verified applied", Result: json.RawMessage(`{"unexpected":true}`)}
		}},
		{name: "unrevoked unverifiable", want: http.StatusConflict, body: func(task contracts.ConnectorTask) any {
			return contracts.ConnectorTaskExecutionResolveRequest{LeaseNonce: task.LeaseNonce, Resolution: contracts.ConnectorTaskExecutionRevokedUnverifiable, EvidenceNote: "unable to verify remote state"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, rawStore, authSvc := newTestServer(t)
			st := rawStore.(*store.MemoryStore)
			admin := createLoginUser(t, authSvc, testUserEmail(t, "invalid-resolve-admin"), contracts.UserRolePlatformAdmin)
			owner := createLoginUser(t, authSvc, testUserEmail(t, "invalid-resolve-owner"), contracts.UserRoleClient)
			adminToken, _, _ := authSvc.Login(context.Background(), admin.Email, "password123")
			_, _, task, leased := createExecutingHTTPConnectorTask(t, st, owner.ID)
			task.LeaseNonce = leased.LeaseNonce
			w := do(t, srv.Routes(), http.MethodPost, "/api/v1/connector-tasks/"+task.ID+"/resolve-execution", adminToken, test.body(task))
			if w.Code != test.want {
				t.Fatalf("status=%d body=%s want=%d", w.Code, w.Body.String(), test.want)
			}
			persisted, err := st.GetConnectorTask(context.Background(), task.ID)
			if err != nil || persisted.Status != contracts.ConnectorTaskExecuting || persisted.LeaseNonce != leased.LeaseNonce {
				t.Fatalf("rejected resolution changed task=%+v err=%v", persisted, err)
			}
			audits, _ := st.ListAudits(context.Background(), owner.ID)
			if len(audits) != 0 {
				t.Fatalf("rejected resolution wrote audit=%+v", audits)
			}
		})
	}
}

func createExecutingHTTPConnectorTask(t *testing.T, st *store.MemoryStore, ownerID int64) (contracts.Instance, contracts.Connector, contracts.ConnectorTask, contracts.ConnectorTask) {
	t.Helper()
	ctx := context.Background()
	fixtureHash := sha256.Sum256([]byte(t.Name()))
	connectorID := fmt.Sprintf("connector-resolve-%x", fixtureHash)
	instance, err := st.CreateInstance(ctx, contracts.Instance{UserID: ownerID, Name: "Resolution gateway", Kind: contracts.InstanceKindSub2API})
	if err != nil {
		t.Fatal(err)
	}
	_, connector := createStoredConnector(t, st, ownerID, instance, connectorID, "token-"+connectorID)
	pool, err := st.CreateUpstreamPool(ctx, contracts.UpstreamPool{Name: "Resolution pool", Status: contracts.UpstreamPoolActive})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := st.CreateRoutePlan(ctx, contracts.RoutePlan{UserID: ownerID, InstanceID: instance.ID, PoolID: pool.ID, Status: contracts.RoutePlanPublished, SchedulingGeneration: 7})
	if err != nil {
		t.Fatal(err)
	}
	fence := contracts.GatewaySchedulingFence{Scope: "auto-switch/plan/" + plan.ID, Version: plan.SchedulingGeneration, Sequence: 1}
	task, err := st.CreateConnectorTask(ctx, contracts.ConnectorTask{
		UserID: ownerID, InstanceID: instance.ID, ConnectorID: connector.ID,
		PlanID: plan.ID, SchedulingGeneration: plan.SchedulingGeneration,
		Type: contracts.ConnectorTaskGatewaySchedulableSet, SchemaVersion: 1,
		Input:          mustJSON(t, contracts.ConnectorGatewaySchedulableSetInput{AccountID: "account-plan-fence", Schedulable: false, Fence: &fence}),
		IdempotencyKey: "resolution-" + connectorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	leasedTasks, err := st.LeaseConnectorTasks(ctx, contracts.ConnectorTaskLeaseRequest{ConnectorID: connector.ID, ProtocolVersion: contracts.ConnectorProtocolVersion})
	if err != nil || len(leasedTasks) != 1 {
		t.Fatalf("lease=%+v err=%v", leasedTasks, err)
	}
	leased := leasedTasks[0]
	if _, err := st.BeginConnectorTaskExecution(ctx, task.ID, contracts.ConnectorTaskExecutionRequest{ConnectorID: connector.ID, LeaseNonce: leased.LeaseNonce}); err != nil {
		t.Fatal(err)
	}
	return instance, connector, task, leased
}

func mustJSONString(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

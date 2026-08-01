package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/store"
	"e2m.local/core/internal/vault"
)

func TestPlatformDistributionManagementIsNativeAndOwnerScoped(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	srv.EnableInsecureSupplyUpstreams()
	handler := srv.Routes()
	ctx := context.Background()

	admin := createLoginUser(t, authSvc, "platform-native-admin@example.com", contracts.UserRolePlatformAdmin)
	owner := createLoginUser(t, authSvc, "platform-native-owner@example.com", contracts.UserRoleOwner)
	other := createLoginUser(t, authSvc, "platform-native-other@example.com", contracts.UserRoleOwner)
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")
	ownerToken, _, _ := authSvc.Login(ctx, owner.Email, "password123")
	otherToken, _, _ := authSvc.Login(ctx, other.Email, "password123")

	groupResponse := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, "group-once", map[string]any{
		"name": "Local stable", "description": "native E2M group", "provider": "internal-provider", "region": "internal-region", "models": []string{"gpt-4o-mini"},
		"status": "active", "resource_class": "stable",
	})
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", groupResponse.Code, groupResponse.Body.String())
	}
	var group contracts.UpstreamPool
	if err := json.Unmarshal(groupResponse.Body.Bytes(), &group); err != nil || group.ID == "" {
		t.Fatalf("decode group: err=%v group=%+v", err, group)
	}
	if group.DeliveryMode != contracts.UpstreamDeliverySupplyGateway {
		t.Fatalf("group must be native supply gateway, got %+v", group)
	}
	if retry := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, "group-once", map[string]any{"name": "ignored retry"}); retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), group.ID) {
		t.Fatalf("idempotent group retry: %d %s", retry.Code, retry.Body.String())
	}
	if visible := do(t, handler, http.MethodGet, "/api/v1/platform/groups", ownerToken, nil); visible.Code != http.StatusOK || !strings.Contains(visible.Body.String(), group.ID) ||
		strings.Contains(visible.Body.String(), "internal-provider") || strings.Contains(visible.Body.String(), "internal-region") || strings.Contains(visible.Body.String(), "delivery_mode") || strings.Contains(visible.Body.String(), "labels") {
		t.Fatalf("owner could not list downstream platform groups: %d %s", visible.Code, visible.Body.String())
	}
	if visible := do(t, handler, http.MethodGet, "/api/v1/platform/groups/"+group.ID, ownerToken, nil); visible.Code != http.StatusOK || strings.Contains(visible.Body.String(), "internal-provider") || strings.Contains(visible.Body.String(), "internal-region") {
		t.Fatalf("owner group detail leaked operational metadata: %d %s", visible.Code, visible.Body.String())
	}
	if visible := do(t, handler, http.MethodGet, "/api/v1/platform/groups", adminToken, nil); visible.Code != http.StatusOK || !strings.Contains(visible.Body.String(), "internal-provider") || !strings.Contains(visible.Body.String(), "internal-region") {
		t.Fatalf("admin group catalog lost operational metadata: %d %s", visible.Code, visible.Body.String())
	}

	upstreamResponse := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken, "upstream-once", map[string]any{
		"group_id": group.ID,
		"name":     "local mock",
		"base_url": "http://mock-openai:8093/v1",
		"api_key":  "super-secret-upstream-key",
		"models":   []string{"gpt-4o-mini"},
		"prices": map[string]any{"gpt-4o-mini": map[string]any{
			"input_micros_per_million": 1000, "output_micros_per_million": 2000,
		}},
		"capacity": map[string]any{"max_concurrency": 4},
		"status":   "active",
	})
	if upstreamResponse.Code != http.StatusCreated {
		t.Fatalf("create upstream: %d %s", upstreamResponse.Code, upstreamResponse.Body.String())
	}
	if strings.Contains(upstreamResponse.Body.String(), "super-secret-upstream-key") {
		t.Fatalf("upstream plaintext leaked: %s", upstreamResponse.Body.String())
	}
	var upstream platformUpstreamResponse
	if err := json.Unmarshal(upstreamResponse.Body.Bytes(), &upstream); err != nil || upstream.ID == "" || upstream.GroupID != group.ID || !upstream.APIKeyConfigured {
		t.Fatalf("decode upstream: err=%v upstream=%+v", err, upstream)
	}
	endpoint, err := st.GetSupplyChannelEndpoint(ctx, upstream.ID)
	if err != nil || !endpoint.AllowInsecure || endpoint.SecretRef == "" {
		t.Fatalf("native endpoint not persisted: err=%v endpoint=%+v", err, endpoint)
	}

	keyResponse := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/keys", ownerToken, "key-once", map[string]any{
		"group_id": group.ID, "name": "production", "status": "active",
	})
	if keyResponse.Code != http.StatusCreated {
		t.Fatalf("create key: %d %s", keyResponse.Code, keyResponse.Body.String())
	}
	var createdKey platformKeyCreateResponse
	if err := json.Unmarshal(keyResponse.Body.Bytes(), &createdKey); err != nil || createdKey.PlaintextKey == "" {
		t.Fatalf("decode key: err=%v response=%+v", err, createdKey)
	}
	if createdKey.Key.UserID != owner.ID || createdKey.Key.GroupID != group.ID || createdKey.Key.InstanceID != "" {
		t.Fatalf("key must bind directly to owner/group: %+v", createdKey.Key)
	}
	if retry := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/keys", ownerToken, "key-once", map[string]any{"group_id": group.ID, "name": "retry"}); retry.Code != http.StatusOK || !strings.Contains(retry.Body.String(), createdKey.PlaintextKey) {
		t.Fatalf("idempotent key retry: %d %s", retry.Code, retry.Body.String())
	}
	valueResponse := do(t, handler, http.MethodGet, "/api/v1/platform/keys/"+createdKey.Key.ID+"/value", ownerToken, nil)
	var keyValue platformKeyValueResponse
	if valueResponse.Code != http.StatusOK || json.Unmarshal(valueResponse.Body.Bytes(), &keyValue) != nil || keyValue.Value != createdKey.PlaintextKey ||
		valueResponse.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("owner could not retrieve key value: %d headers=%v body=%s", valueResponse.Code, valueResponse.Header(), valueResponse.Body.String())
	}
	if listed := do(t, handler, http.MethodGet, "/api/v1/platform/keys", ownerToken, nil); listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), createdKey.PlaintextKey) || strings.Contains(listed.Body.String(), "secret_ref") || strings.Contains(listed.Body.String(), "token_hash") {
		t.Fatalf("ordinary key list leaked sensitive material: %d %s", listed.Code, listed.Body.String())
	}
	if denied := do(t, handler, http.MethodGet, "/api/v1/platform/keys/"+createdKey.Key.ID+"/value?user_id="+userIDString(owner.ID), otherToken, nil); denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), createdKey.PlaintextKey) {
		t.Fatalf("cross-owner key value read must fail: %d %s", denied.Code, denied.Body.String())
	}
	audits, err := st.ListAudits(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	viewAuditFound := false
	for _, audit := range audits {
		if audit.Action == "platform_key.view" && audit.TargetID == createdKey.Key.ID && audit.Result == "accepted" {
			viewAuditFound = true
			break
		}
	}
	if !viewAuditFound {
		t.Fatal("platform key value access was not audited")
	}

	adjustment := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/wallet-adjustments", adminToken, "credit-once", map[string]any{
		"user_id": owner.ID, "amount_micros": 100_000_000, "reason": "test credit",
	})
	if adjustment.Code != http.StatusOK {
		t.Fatalf("adjust wallet: %d %s", adjustment.Code, adjustment.Body.String())
	}
	if retry := doWithIdempotency(t, handler, http.MethodPost, "/api/v1/platform/wallet-adjustments", adminToken, "credit-once", map[string]any{"user_id": owner.ID, "amount_micros": 100_000_000, "reason": "retry"}); retry.Code != http.StatusOK {
		t.Fatalf("idempotent wallet retry: %d %s", retry.Code, retry.Body.String())
	}
	walletResponse := do(t, handler, http.MethodGet, "/api/v1/platform/wallet", ownerToken, nil)
	var wallet contracts.Wallet
	if walletResponse.Code != http.StatusOK || json.Unmarshal(walletResponse.Body.Bytes(), &wallet) != nil || wallet.AvailableMicros != 100_000_000 {
		t.Fatalf("owner wallet: status=%d wallet=%+v body=%s", walletResponse.Code, wallet, walletResponse.Body.String())
	}

	usageResponse := do(t, handler, http.MethodGet, "/api/v1/platform/usage?limit=20", ownerToken, nil)
	if usageResponse.Code != http.StatusOK || !strings.Contains(usageResponse.Body.String(), `"items":[]`) {
		t.Fatalf("owner usage: %d %s", usageResponse.Code, usageResponse.Body.String())
	}
	crossOwner := do(t, handler, http.MethodGet, "/api/v1/platform/keys?user_id="+userIDString(owner.ID), otherToken, nil)
	if crossOwner.Code != http.StatusForbidden {
		t.Fatalf("cross-owner key read must fail: %d %s", crossOwner.Code, crossOwner.Body.String())
	}
}

func TestPlatformDistributionRejectsUnsafeOrAmbiguousInputs(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	admin := createLoginUser(t, authSvc, "platform-validation-admin@example.com", contracts.UserRolePlatformAdmin)
	adminToken, _, _ := authSvc.Login(context.Background(), admin.Email, "password123")
	handler := srv.Routes()

	groupResponse := do(t, handler, http.MethodPost, "/api/v1/platform/groups", adminToken, map[string]any{
		"name": "economy", "models": []string{"gpt-test"}, "resource_class": "economy",
	})
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", groupResponse.Code, groupResponse.Body.String())
	}
	var group contracts.UpstreamPool
	_ = json.Unmarshal(groupResponse.Body.Bytes(), &group)

	insecure := do(t, handler, http.MethodPost, "/api/v1/platform/upstreams", adminToken, map[string]any{
		"group_id": group.ID, "name": "unsafe", "base_url": "http://upstream.local/v1", "api_key": "secret",
		"models": []string{"gpt-test"}, "prices": map[string]any{"gpt-test": map[string]any{"input_micros_per_million": 1, "output_micros_per_million": 1}},
	})
	if insecure.Code != http.StatusBadRequest {
		t.Fatalf("HTTP upstream must fail closed: %d %s", insecure.Code, insecure.Body.String())
	}
	missingIdempotency := do(t, handler, http.MethodPost, "/api/v1/platform/wallet-adjustments", adminToken, map[string]any{
		"user_id": admin.ID, "amount_micros": 10, "reason": "unsafe duplicate",
	})
	if missingIdempotency.Code != http.StatusBadRequest {
		t.Fatalf("wallet adjustment must require idempotency: %d %s", missingIdempotency.Code, missingIdempotency.Body.String())
	}
}

func TestPlatformUpstreamConnectionTestAndSafeDelete(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	srv.EnableInsecureSupplyUpstreams()
	admin := createLoginUser(t, authSvc, "platform-test-admin@example.com", contracts.UserRolePlatformAdmin)
	token, _, _ := authSvc.Login(context.Background(), admin.Email, "password123")
	handler := srv.Routes()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer connection-secret" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-test"}]}`))
	}))
	defer upstream.Close()

	groupResponse := do(t, handler, http.MethodPost, "/api/v1/platform/groups", token, map[string]any{
		"name": "connection test group", "models": []string{"gpt-test"}, "resource_class": "stable",
	})
	if groupResponse.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", groupResponse.Code, groupResponse.Body.String())
	}
	var group contracts.UpstreamPool
	if err := json.Unmarshal(groupResponse.Body.Bytes(), &group); err != nil {
		t.Fatal(err)
	}
	upstreamResponse := do(t, handler, http.MethodPost, "/api/v1/platform/upstreams", token, map[string]any{
		"group_id": group.ID, "name": "test upstream", "base_url": upstream.URL + "/v1", "api_key": "connection-secret",
		"models": []string{"gpt-test"}, "prices": map[string]any{"gpt-test": map[string]any{"input_micros_per_million": 1, "output_micros_per_million": 1}},
	})
	if upstreamResponse.Code != http.StatusCreated {
		t.Fatalf("create upstream: %d %s", upstreamResponse.Code, upstreamResponse.Body.String())
	}
	var view platformUpstreamResponse
	if err := json.Unmarshal(upstreamResponse.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	testResponse := do(t, handler, http.MethodPost, "/api/v1/platform/upstreams/"+view.ID+"/test", token, nil)
	if testResponse.Code != http.StatusOK || strings.Contains(testResponse.Body.String(), "connection-secret") {
		t.Fatalf("connection test response leaked or failed: %d %s", testResponse.Code, testResponse.Body.String())
	}
	var result platformUpstreamTestResponse
	if err := json.Unmarshal(testResponse.Body.Bytes(), &result); err != nil || !result.OK || result.ModelCount != 1 || len(result.Models) != 1 || result.Models[0] != "gpt-test" {
		t.Fatalf("connection test result: err=%v result=%+v", err, result)
	}

	deleted := do(t, handler, http.MethodDelete, "/api/v1/platform/upstreams/"+view.ID, token, nil)
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"enabled":false`) {
		t.Fatalf("safe upstream delete: %d %s", deleted.Code, deleted.Body.String())
	}
	endpoint, err := st.GetSupplyChannelEndpoint(context.Background(), view.ID)
	if err != nil || endpoint.Enabled {
		t.Fatalf("upstream endpoint should be disabled: err=%v endpoint=%+v", err, endpoint)
	}
	groupDelete := do(t, handler, http.MethodDelete, "/api/v1/platform/groups/"+group.ID, token, nil)
	if groupDelete.Code != http.StatusAccepted {
		t.Fatalf("group retirement: %d %s", groupDelete.Code, groupDelete.Body.String())
	}
	retired, err := st.GetUpstreamPool(context.Background(), group.ID)
	if err != nil || retired.Status != contracts.UpstreamPoolRetired {
		t.Fatalf("unused group should complete retirement immediately: err=%v group=%+v", err, retired)
	}
}

func TestPlatformUpstreamCreateQuarantinesChannelWhenVaultStoreFails(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	srv.SetVault(failingPlatformVault{})
	admin := createLoginUser(t, authSvc, "platform-vault-failure-admin@example.com", contracts.UserRolePlatformAdmin)
	token, _, _ := authSvc.Login(context.Background(), admin.Email, "password123")
	handler := srv.Routes()

	groupResponse := do(t, handler, http.MethodPost, "/api/v1/platform/groups", token, map[string]any{
		"name": "vault failure group", "models": []string{"gpt-test"}, "resource_class": "stable",
	})
	var group contracts.UpstreamPool
	if groupResponse.Code != http.StatusCreated || json.Unmarshal(groupResponse.Body.Bytes(), &group) != nil {
		t.Fatalf("create group: %d %s", groupResponse.Code, groupResponse.Body.String())
	}
	created := do(t, handler, http.MethodPost, "/api/v1/platform/upstreams", token, map[string]any{
		"group_id": group.ID, "name": "failed credential", "base_url": "https://upstream.example/v1", "api_key": "never-stored",
		"models": []string{"gpt-test"}, "prices": map[string]any{"gpt-test": map[string]any{"input_micros_per_million": 1, "output_micros_per_million": 1}},
	})
	if created.Code != http.StatusInternalServerError || strings.Contains(created.Body.String(), "never-stored") {
		t.Fatalf("vault failure should be safe: %d %s", created.Code, created.Body.String())
	}
	channels, err := st.ListUpstreamChannels(context.Background(), group.ID)
	if err != nil || len(channels) != 1 || channels[0].Status != contracts.UpstreamChannelMaintenance || channels[0].InventoryState != contracts.UpstreamInventoryQuarantined {
		t.Fatalf("failed vault write must quarantine channel: err=%v channels=%+v", err, channels)
	}
	if _, err := st.GetSupplyChannelEndpoint(context.Background(), channels[0].ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed vault write must not persist endpoint, got %v", err)
	}
}

type failingPlatformVault struct{}

func (failingPlatformVault) Store(context.Context, string, string) (string, error) {
	return "", errors.New("test vault store failed")
}

func (failingPlatformVault) Resolve(context.Context, string) (vault.Secret, error) {
	return vault.Secret{}, vault.ErrNotFound
}

func (failingPlatformVault) Delete(context.Context, string) error { return nil }

func (failingPlatformVault) ListRefs(context.Context) ([]string, error) { return nil, nil }

func doWithIdempotency(t *testing.T, h http.Handler, method, path, token, idempotencyKey string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		requestBody = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, requestBody)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

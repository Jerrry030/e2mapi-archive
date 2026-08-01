package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

func TestClientSecretsAreLimitedToNotificationCredentials(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	h := srv.Routes()
	ctx := context.Background()

	client := createLoginUser(t, authSvc, "a-owner@e2m.local", contracts.UserRoleOwner)
	other := createStoreUser(t, st, "b-owner@e2m.local", contracts.UserRoleOwner)
	token, _, _ := authSvc.Login(ctx, client.Email, "password123")

	for _, kind := range []string{"upstream", "proxy"} {
		w := do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
			"user_id": client.ID, "kind": kind, "name": "blocked", "value": "must-not-store",
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("client %s secret write should be 403, got %d %s", kind, w.Code, w.Body.String())
		}
	}

	w := do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
		"user_id": client.ID,
		"kind":    "notification",
		"name":    "Operations webhook",
		"value":   "https://hooks.example.com/client-events",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("notification secret write: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "client-events") {
		t.Fatalf("secret plaintext leaked in response: %s", w.Body.String())
	}
	var created contracts.UpsertSecretResponse
	decodeResponse(t, w, &created)
	wantPrefix := "credential_ref:user/" + userIDString(client.ID) + "/notification/"
	if !strings.HasPrefix(created.Secret.Ref, wantPrefix) {
		t.Fatalf("unexpected ref %q", created.Secret.Ref)
	}

	w = do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
		"user_id": other.ID,
		"kind":    "notification",
		"name":    "Other webhook",
		"value":   "https://hooks.example.com/other-events",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-user secret write should be 403, got %d %s", w.Code, w.Body.String())
	}

	// Even if a legacy upstream ref already exists under the client namespace,
	// clients cannot enumerate or delete it after the role boundary is tightened.
	legacyRef := buildSecretRef(client.ID, contracts.SecretKindUpstream, "legacy")
	if _, err := v.Store(ctx, legacyRef, "legacy-upstream-value"); err != nil {
		t.Fatalf("seed legacy secret: %v", err)
	}
	w = do(t, h, http.MethodGet, "/api/v1/secrets", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list secrets: %d %s", w.Code, w.Body.String())
	}
	var listed []contracts.SecretRef
	decodeResponse(t, w, &listed)
	if len(listed) != 1 || listed[0].Kind != contracts.SecretKindNotification {
		t.Fatalf("client should only see notification secrets, got %+v", listed)
	}
	w = do(t, h, http.MethodDelete, "/api/v1/secrets?ref="+legacyRef, token, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("client legacy upstream secret delete should be 403, got %d %s", w.Code, w.Body.String())
	}
	w = do(t, h, http.MethodDelete, "/api/v1/secrets?ref="+created.Secret.Ref, token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("delete notification secret: %d %s", w.Code, w.Body.String())
	}
	if _, err := v.Resolve(ctx, created.Secret.Ref); !errors.Is(err, vault.ErrNotFound) {
		t.Fatalf("expected deleted notification secret to be gone, got %v", err)
	}
}
func TestSupplierSecretsAreLimitedToSupplyCredentials(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "supplier@e2m.local", contracts.UserRoleSupplier)
	token, _, _ := authSvc.Login(ctx, "supplier@e2m.local", "password123")

	w := do(t, h, "POST", "/api/v1/secrets", token, map[string]any{
		"user_id": user.ID,
		"kind":    "upstream",
		"name":    "claude",
		"value":   "upstream-secret",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("supplier should write upstream secret: %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, "POST", "/api/v1/secrets", token, map[string]any{
		"user_id": user.ID,
		"kind":    "gateway_admin",
		"name":    "gateway",
		"value":   "admin-secret",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("supplier must not write gateway admin secret, got %d %s", w.Code, w.Body.String())
	}

	w = do(t, h, "GET", "/api/v1/secrets", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list secrets: %d %s", w.Code, w.Body.String())
	}
	var listed []contracts.SecretRef
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 || listed[0].Kind != contracts.SecretKindUpstream {
		t.Fatalf("supplier should only see supply-safe secrets, got %+v", listed)
	}
}

func TestDualRoleCanManageUnionOfSecretKinds(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	h := srv.Routes()
	ctx := context.Background()
	user := createLoginUser(t, authSvc, "dual-secret-role@e2m.local", contracts.UserRoleOwner, contracts.UserRoleSupplier)
	token, _, _ := authSvc.Login(ctx, user.Email, "password123")

	values := map[string]string{
		"notification": "https://hooks.example.com/dual-events",
		"upstream":     "upstream-secret",
		"proxy":        "proxy-secret",
	}
	for kind, value := range values {
		w := do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
			"user_id": user.ID, "kind": kind, "name": kind, "value": value,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("dual-role %s secret write: got %d %s", kind, w.Code, w.Body.String())
		}
	}
}
func TestGatewayAdminSecretsRejectedForCoreStorage(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	srv.SetVault(vault.NewMemoryVault())
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "owner-gateway-secret@e2m.local", contracts.UserRoleOwner)
	token, _, _ := authSvc.Login(ctx, "owner-gateway-secret@e2m.local", "password123")

	w := do(t, h, "POST", "/api/v1/secrets", token, map[string]any{
		"user_id": user.ID,
		"kind":    "gateway_admin",
		"name":    "gateway",
		"value":   "admin-secret",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("gateway admin secret must stay out of core, got %d %s", w.Code, w.Body.String())
	}
}

func TestNotificationSecretRequiresSafeHTTPSWebhook(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	h := srv.Routes()
	ctx := context.Background()
	user := createLoginUser(t, authSvc, "owner-notification-secret@e2m.local", contracts.UserRoleOwner)
	token, _, _ := authSvc.Login(ctx, user.Email, "password123")

	for _, value := range []string{
		"not-a-url",
		"http://hooks.example.com/events",
		"https://127.0.0.1/events",
		"https://[64:ff9b:1:fffe::7f00:1]/events",
	} {
		w := do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
			"user_id": user.ID, "kind": "notification", "name": "ops", "value": value,
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe notification value %q: got %d %s", value, w.Code, w.Body.String())
		}
	}
	w := do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
		"user_id": user.ID, "kind": "notification", "name": "ops", "value": "https://hooks.example.com/events",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("safe notification value: got %d %s", w.Code, w.Body.String())
	}
}

func TestSecretRefIsServerGeneratedAndKindsAreClosed(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	h := srv.Routes()
	ctx := context.Background()
	user := createLoginUser(t, authSvc, "closed-secret-kind@e2m.local", contracts.UserRoleSupplier)
	token, _, _ := authSvc.Login(ctx, user.Email, "password123")

	w := do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
		"user_id": user.ID,
		"ref":     "credential_ref:user/" + userIDString(user.ID) + "/gateway_admin/disguised",
		"kind":    "upstream",
		"name":    "safe-name",
		"value":   "business-secret",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("server-generated ref request: %d %s", w.Code, w.Body.String())
	}
	var created contracts.UpsertSecretResponse
	decodeResponse(t, w, &created)
	if strings.Contains(created.Secret.Ref, "gateway_admin") || !strings.Contains(created.Secret.Ref, "/upstream/safe-name") {
		t.Fatalf("caller-controlled ref affected generated ref: %q", created.Secret.Ref)
	}

	w = do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
		"user_id": user.ID,
		"kind":    "other",
		"name":    "opaque",
		"value":   "not-allowed",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown secret kind must be rejected: %d %s", w.Code, w.Body.String())
	}
}

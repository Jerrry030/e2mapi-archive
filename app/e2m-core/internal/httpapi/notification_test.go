package httpapi

import (
	"context"
	"net/http"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/vault"
)

func TestUpdateNotificationRouteUsesPersistedOwner(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "notification-owner@example.com", contracts.UserRoleClient)
	attacker := createLoginUser(t, authSvc, "notification-attacker@example.com", contracts.UserRoleClient)
	attackerToken, _, err := authSvc.Login(ctx, attacker.Email, "password123")
	if err != nil {
		t.Fatalf("login attacker: %v", err)
	}
	route, err := st.CreateNotificationRoute(ctx, contracts.NotificationRoute{
		UserID: owner.ID, Name: "Owner webhook", Channel: contracts.NotificationChannelWebhook,
		TargetRef: "credential_ref:user/owner/notification/webhook", MinRiskLevel: contracts.RiskLevelL1,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}

	w := do(t, srv.Routes(), http.MethodPut, "/api/v1/notification-routes/"+route.ID, attackerToken, map[string]any{
		"user_id":        attacker.ID,
		"name":           "Stolen route",
		"channel":        contracts.NotificationChannelWebhook,
		"target_ref":     "credential_ref:user/attacker/notification/webhook",
		"min_risk_level": contracts.RiskLevelL0,
		"enabled":        true,
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-user update: got %d %s", w.Code, w.Body.String())
	}
	persisted, err := st.GetNotificationRoute(ctx, route.ID)
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if persisted.UserID != owner.ID || persisted.Name != route.Name || persisted.TargetRef != route.TargetRef {
		t.Fatalf("denied update changed route: before=%+v after=%+v", route, persisted)
	}
}

func TestAdminCannotTransferNotificationRoute(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "notification-admin-owner@example.com", contracts.UserRoleClient)
	other := createLoginUser(t, authSvc, "notification-admin-other@example.com", contracts.UserRoleClient)
	admin := createLoginUser(t, authSvc, "notification-admin@example.com", contracts.UserRoleAdmin)
	adminToken, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	route, err := st.CreateNotificationRoute(ctx, contracts.NotificationRoute{
		UserID: owner.ID, Name: "Owner webhook", Channel: contracts.NotificationChannelWebhook,
		TargetRef: "credential_ref:user/owner/notification/webhook", MinRiskLevel: contracts.RiskLevelL1,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	w := do(t, srv.Routes(), http.MethodPut, "/api/v1/notification-routes/"+route.ID, adminToken, map[string]any{
		"user_id":        other.ID,
		"name":           route.Name,
		"channel":        route.Channel,
		"target_ref":     route.TargetRef,
		"min_risk_level": route.MinRiskLevel,
		"enabled":        route.Enabled,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("owner transfer: got %d %s", w.Code, w.Body.String())
	}
}

func TestAdminCreateNotificationRouteRequiresClientTarget(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	ctx := context.Background()
	admin := createLoginUser(t, authSvc, "notification-create-admin@example.com", contracts.UserRoleAdmin)
	supplier := createLoginUser(t, authSvc, "notification-create-supplier@example.com", contracts.UserRoleSupplier)
	token, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	w := do(t, srv.Routes(), http.MethodPost, "/api/v1/notification-routes", token, map[string]any{
		"user_id": supplier.ID, "name": "invalid owner route", "channel": contracts.NotificationChannelWebhook,
		"target_ref": "credential_ref:user/supplier/notification/webhook", "min_risk_level": contracts.RiskLevelL1,
		"enabled": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("supplier-only notification target: got %d %s", w.Code, w.Body.String())
	}
}

func TestValidRouteRequiresOwnerScopedNotificationCredential(t *testing.T) {
	t.Parallel()
	base := contracts.NotificationRoute{
		UserID: 1, Name: "operations", Channel: contracts.NotificationChannelWebhook,
		MinRiskLevel: contracts.RiskLevelL1, Enabled: true,
	}
	for _, target := range []string{
		"http://hooks.example.com/events",
		"https://hooks.example.com/events",
		"credential_ref:user/2/notification/ops",
		"credential_ref:user/1/upstream/ops",
	} {
		input := base
		input.TargetRef = target
		if msg := validRoute(input); msg == "" {
			t.Fatalf("validRoute accepted unsafe webhook target %q", target)
		}
	}
	base.TargetRef = "credential_ref:user/1/notification/ops"
	if msg := validRoute(base); msg != "" {
		t.Fatalf("validRoute rejected owner notification ref: %s", msg)
	}
}

func TestCreateNotificationRouteValidatesVaultTarget(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "notification-vault-owner@example.com", contracts.UserRoleClient)
	token, _, err := authSvc.Login(ctx, owner.Email, "password123")
	if err != nil {
		t.Fatalf("login owner: %v", err)
	}
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	ref := buildSecretRef(owner.ID, contracts.SecretKindNotification, "ops")
	base := map[string]any{
		"user_id": owner.ID, "name": "operations", "channel": contracts.NotificationChannelWebhook,
		"target_ref": ref, "min_risk_level": contracts.RiskLevelL1, "enabled": true,
	}

	if w := do(t, srv.Routes(), http.MethodPost, "/api/v1/notification-routes", token, base); w.Code != http.StatusBadRequest {
		t.Fatalf("missing target: got %d %s", w.Code, w.Body.String())
	}
	_, _ = v.Store(ctx, ref, "http://169.254.169.254/latest/meta-data")
	if w := do(t, srv.Routes(), http.MethodPost, "/api/v1/notification-routes", token, base); w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe target: got %d %s", w.Code, w.Body.String())
	}
	_, _ = v.Store(ctx, ref, "https://hooks.example.com/events")
	if w := do(t, srv.Routes(), http.MethodPost, "/api/v1/notification-routes", token, base); w.Code != http.StatusCreated {
		t.Fatalf("safe target: got %d %s", w.Code, w.Body.String())
	}
}

func TestAdminWebhookRouteCannotUseAnotherOwnersCredential(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	ctx := context.Background()
	admin := createLoginUser(t, authSvc, "notification-ref-admin@example.com", contracts.UserRoleAdmin)
	owner := createLoginUser(t, authSvc, "notification-ref-owner@example.com", contracts.UserRoleClient)
	other := createLoginUser(t, authSvc, "notification-ref-other@example.com", contracts.UserRoleClient)
	token, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	otherRef := buildSecretRef(other.ID, contracts.SecretKindNotification, "ops")
	_, _ = v.Store(ctx, otherRef, "https://hooks.example.com/events")
	w := do(t, srv.Routes(), http.MethodPost, "/api/v1/notification-routes", token, map[string]any{
		"user_id": owner.ID, "name": "operations", "channel": contracts.NotificationChannelWebhook,
		"target_ref": otherRef, "min_risk_level": contracts.RiskLevelL1, "enabled": true,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("cross-owner target ref: got %d %s", w.Code, w.Body.String())
	}
}

func TestSystemNotificationChannelsDefaultOnlyFromEmptyTarget(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "notification-system-owner@example.com", contracts.UserRoleClient)
	token, _, err := authSvc.Login(ctx, owner.Email, "password123")
	if err != nil {
		t.Fatalf("login owner: %v", err)
	}
	for _, tc := range []struct {
		channel contracts.NotificationChannel
		want    string
	}{
		{channel: contracts.NotificationChannelFeishu, want: "system:feishu"},
		{channel: contracts.NotificationChannelQQ, want: "system:qq"},
	} {
		w := do(t, srv.Routes(), http.MethodPost, "/api/v1/notification-routes", token, map[string]any{
			"user_id": owner.ID, "name": string(tc.channel), "channel": tc.channel,
			"target_ref": "", "min_risk_level": contracts.RiskLevelL1, "enabled": true,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s route: got %d %s", tc.channel, w.Code, w.Body.String())
		}
		var route contracts.NotificationRoute
		decodeResponse(t, w, &route)
		if route.TargetRef != tc.want {
			t.Fatalf("%s target_ref = %q, want %q", tc.channel, route.TargetRef, tc.want)
		}
		invalid := do(t, srv.Routes(), http.MethodPost, "/api/v1/notification-routes", token, map[string]any{
			"user_id": owner.ID, "name": "invalid-" + string(tc.channel), "channel": tc.channel,
			"target_ref": "caller-secret-target", "min_risk_level": contracts.RiskLevelL1, "enabled": true,
		})
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("explicit invalid %s target: got %d %s", tc.channel, invalid.Code, invalid.Body.String())
		}
	}
}

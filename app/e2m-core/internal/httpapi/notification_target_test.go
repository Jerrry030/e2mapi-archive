package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"e2m.local/contracts"
	"e2m.local/core/internal/notify"
	"e2m.local/core/internal/vault"
)

func personalTargetTestServer(t *testing.T) (*Server, string, contracts.User, *vault.MemoryVault) {
	t.Helper()
	srv, st, authSvc := newTestServer(t)
	owner := createLoginUser(t, authSvc, testUserEmail(t, "personal-target-owner"), contracts.UserRoleOwner)
	token, _, err := authSvc.Login(context.Background(), owner.Email, "password123")
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemoryVault()
	srv.SetVault(v)
	router := notify.NewRouter(nil, nil)
	router.SetSecretResolver(v)
	router.SetDeliveryStore(st)
	srv.SetNotificationRouter(router)
	return srv, token, owner, v
}

func TestPersonalNotificationTargetLifecycleIsRedactedAndPreservesSecrets(t *testing.T) {
	srv, token, owner, v := personalTargetTestServer(t)
	h := srv.Routes()
	put := func(body map[string]any) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPut, "/api/v1/notification-targets/feishu", token, body)
	}
	first := put(map[string]any{
		"user_id": owner.ID, "webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/abc_DEF-123", "signing_secret": "top-secret",
	})
	if first.Code != http.StatusOK {
		t.Fatalf("create target: %d %s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), "abc_DEF") || strings.Contains(first.Body.String(), "top-secret") || strings.Contains(first.Body.String(), "/hook/") {
		t.Fatalf("target response leaked credential: %s", first.Body.String())
	}
	second := put(map[string]any{"user_id": owner.ID, "webhook_url": "", "signing_secret": ""})
	if second.Code != http.StatusOK {
		t.Fatalf("preserve update: %d %s", second.Code, second.Body.String())
	}
	ref, _ := notify.PersonalNotificationTargetRef(owner.ID, contracts.NotificationChannelFeishu)
	stored, err := v.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := notify.DecodePersonalTargetCredential(stored.Value)
	if err != nil || credential.SigningSecret != "top-secret" || !strings.Contains(credential.WebhookURL, "abc_DEF") {
		t.Fatalf("secret was not preserved: %+v err=%v", credential, err)
	}
	cleared := put(map[string]any{"user_id": owner.ID, "clear_signing_secret": true})
	if cleared.Code != http.StatusOK || strings.Contains(cleared.Body.String(), "top-secret") {
		t.Fatalf("clear secret: %d %s", cleared.Code, cleared.Body.String())
	}
	listedTargets := do(t, h, http.MethodGet, "/api/v1/notification-targets", token, nil)
	if listedTargets.Code != http.StatusOK || strings.Contains(listedTargets.Body.String(), "abc_DEF") || strings.Contains(listedTargets.Body.String(), "/hook/") {
		t.Fatalf("target list leaked credential: %d %s", listedTargets.Code, listedTargets.Body.String())
	}
	listedSecrets := do(t, h, http.MethodGet, "/api/v1/secrets", token, nil)
	if listedSecrets.Code != http.StatusOK || strings.Contains(listedSecrets.Body.String(), "personal-feishu") {
		t.Fatalf("generic secret list exposed reserved target: %d %s", listedSecrets.Code, listedSecrets.Body.String())
	}
	genericDelete := do(t, h, http.MethodDelete, "/api/v1/secrets?ref="+ref, token, nil)
	if genericDelete.Code != http.StatusBadRequest {
		t.Fatalf("generic secret delete bypassed target API: %d %s", genericDelete.Code, genericDelete.Body.String())
	}
}

func TestPersonalNotificationTargetOwnerBoundaryAndReservedSecretAPI(t *testing.T) {
	srv, token, owner, _ := personalTargetTestServer(t)
	h := srv.Routes()
	// A supplier-only account cannot manage owner notification targets.
	otherSrv, _, otherAuth := newTestServer(t)
	otherSrv.SetVault(vault.NewMemoryVault())
	supplier := createLoginUser(t, otherAuth, testUserEmail(t, "target-supplier"), contracts.UserRoleSupplier)
	supplierToken, _, _ := otherAuth.Login(context.Background(), supplier.Email, "password123")
	denied := do(t, otherSrv.Routes(), http.MethodPut, "/api/v1/notification-targets/feishu", supplierToken, map[string]any{
		"webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/token",
	})
	if denied.Code != http.StatusForbidden {
		t.Fatalf("supplier target write=%d %s", denied.Code, denied.Body.String())
	}

	cross := do(t, h, http.MethodGet, "/api/v1/notification-targets?user_id="+strconv.FormatInt(owner.ID+999, 10), token, nil)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-owner list=%d %s", cross.Code, cross.Body.String())
	}
	reserved := do(t, h, http.MethodPost, "/api/v1/secrets", token, map[string]any{
		"user_id": owner.ID, "kind": "notification", "name": "personal-feishu", "value": "https://hooks.example.com/x",
	})
	if reserved.Code != http.StatusBadRequest {
		t.Fatalf("reserved secret write=%d %s", reserved.Code, reserved.Body.String())
	}
}

func TestDeletePersonalNotificationTargetRejectsRouteAndActiveOutbox(t *testing.T) {
	srv, token, owner, v := personalTargetTestServer(t)
	h := srv.Routes()
	create := do(t, h, http.MethodPut, "/api/v1/notification-targets/qq", token, map[string]any{
		"user_id": owner.ID, "onebot_url": "https://qq.example.com", "access_token": "token", "group_id": "12345678",
	})
	if create.Code != http.StatusOK {
		t.Fatalf("create QQ target=%d %s", create.Code, create.Body.String())
	}
	ref, _ := notify.PersonalNotificationTargetRef(owner.ID, contracts.NotificationChannelQQ)
	routeResponse := do(t, h, http.MethodPost, "/api/v1/notification-routes", token, map[string]any{
		"user_id": owner.ID, "name": "personal QQ", "channel": "qq", "target_ref": ref,
		"min_risk_level": "L1", "enabled": true,
	})
	if routeResponse.Code != http.StatusCreated {
		t.Fatalf("create route=%d %s", routeResponse.Code, routeResponse.Body.String())
	}
	var route contracts.NotificationRoute
	_ = json.Unmarshal(routeResponse.Body.Bytes(), &route)
	blocked := do(t, h, http.MethodDelete, "/api/v1/notification-targets/qq", token, nil)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "notification_target_in_use") {
		t.Fatalf("enabled route delete=%d %s", blocked.Code, blocked.Body.String())
	}

	route.Enabled = false
	updated := do(t, h, http.MethodPut, "/api/v1/notification-routes/"+route.ID, token, route)
	if updated.Code != http.StatusOK {
		t.Fatalf("disable route=%d %s", updated.Code, updated.Body.String())
	}
	_, _, _ = srv.notificationRouter.EnqueueRoute(context.Background(), notify.Event{
		UserID: owner.ID, EventLevel: contracts.EventLevelWarning, RiskLevel: contracts.RiskLevelL1,
	}, route, contracts.NotificationDeliveryKindTest, true)
	blocked = do(t, h, http.MethodDelete, "/api/v1/notification-targets/qq", token, nil)
	if blocked.Code != http.StatusConflict {
		t.Fatalf("active outbox delete=%d %s", blocked.Code, blocked.Body.String())
	}
	if _, err := v.Resolve(context.Background(), ref); err != nil {
		t.Fatalf("blocked delete removed credential: %v", err)
	}
}

func TestPersonalRouteTestDoesNotRequireSystemChannel(t *testing.T) {
	srv, token, owner, _ := personalTargetTestServer(t)
	h := srv.Routes()
	createdTarget := do(t, h, http.MethodPut, "/api/v1/notification-targets/feishu", token, map[string]any{
		"user_id": owner.ID, "webhook_url": "https://open.feishu.cn/open-apis/bot/v2/hook/test_token",
	})
	if createdTarget.Code != http.StatusOK {
		t.Fatalf("create target=%d %s", createdTarget.Code, createdTarget.Body.String())
	}
	ref, _ := notify.PersonalNotificationTargetRef(owner.ID, contracts.NotificationChannelFeishu)
	createdRoute := do(t, h, http.MethodPost, "/api/v1/notification-routes", token, map[string]any{
		"user_id": owner.ID, "name": "my Feishu", "channel": "feishu", "target_ref": ref,
		"min_risk_level": "L0", "enabled": true,
	})
	if createdRoute.Code != http.StatusCreated {
		t.Fatalf("create route=%d %s", createdRoute.Code, createdRoute.Body.String())
	}
	var route contracts.NotificationRoute
	_ = json.Unmarshal(createdRoute.Body.Bytes(), &route)
	queued := do(t, h, http.MethodPost, "/api/v1/notification-routes/"+route.ID+"/test", token, nil)
	if queued.Code != http.StatusAccepted {
		t.Fatalf("personal test depended on system channel: %d %s", queued.Code, queued.Body.String())
	}
}

func TestPersonalQQUpdatePreservesAndCannotClearRequiredToken(t *testing.T) {
	srv, token, owner, v := personalTargetTestServer(t)
	h := srv.Routes()
	first := do(t, h, http.MethodPut, "/api/v1/notification-targets/qq", token, map[string]any{
		"user_id": owner.ID, "onebot_url": "https://qq.example.com", "access_token": "very-secret-token", "group_id": "12345678",
	})
	if first.Code != http.StatusOK || strings.Contains(first.Body.String(), "very-secret-token") || strings.Contains(first.Body.String(), "12345678") {
		t.Fatalf("QQ create/redaction=%d %s", first.Code, first.Body.String())
	}
	second := do(t, h, http.MethodPut, "/api/v1/notification-targets/qq", token, map[string]any{
		"user_id": owner.ID, "onebot_url": "", "access_token": "", "group_id": "",
	})
	if second.Code != http.StatusOK {
		t.Fatalf("QQ preserve update=%d %s", second.Code, second.Body.String())
	}
	ref, _ := notify.PersonalNotificationTargetRef(owner.ID, contracts.NotificationChannelQQ)
	stored, _ := v.Resolve(context.Background(), ref)
	credential, err := notify.DecodePersonalTargetCredential(stored.Value)
	if err != nil || credential.AccessToken != "very-secret-token" || credential.GroupID != 12345678 {
		t.Fatalf("QQ fields not preserved: %+v err=%v", credential, err)
	}
	clear := do(t, h, http.MethodPut, "/api/v1/notification-targets/qq", token, map[string]any{
		"user_id": owner.ID, "clear_access_token": true,
	})
	if clear.Code != http.StatusBadRequest {
		t.Fatalf("required QQ token was cleared: %d %s", clear.Code, clear.Body.String())
	}
}

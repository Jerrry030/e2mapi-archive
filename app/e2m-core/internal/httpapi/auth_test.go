package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/adapters"
	"e2m.local/core/internal/auth"
	"e2m.local/core/internal/orchestrator"
	"e2m.local/core/internal/store"
)

func newTestServer(t *testing.T) (*Server, store.Store, *auth.Service) {
	t.Helper()
	st := store.NewMemoryStore(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	orch := orchestrator.New(st, map[contracts.InstanceKind]adapters.GatewayAdapter{})
	authSvc := auth.NewService(st)
	srv := NewServer(st, orch, nil, nil, nil, authSvc, NewEventBus(), nil)
	if err := srv.ConfigureUpstreamIntelligenceCursorKeyring("test", map[string][]byte{
		"test": []byte("0123456789abcdef0123456789abcdef"),
	}); err != nil {
		t.Fatalf("configure test cursor key: %v", err)
	}
	srv.SetBusinessFeatureFlags(BusinessFeatureFlags{
		Billing: true, Payments: true, Supply: true, HybridSupply: true,
		UpstreamRecommendations: true, UpstreamOptimizationApply: true,
	})
	return srv, st, authSvc
}

func do(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		raw, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(raw))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestUnauthenticatedRejected(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()

	// Protected endpoint without a token -> 401.
	if w := do(t, h, "GET", "/api/v1/instances", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", w.Code)
	}
	// Healthz is public.
	if w := do(t, h, "GET", "/healthz", "", nil); w.Code != http.StatusOK {
		t.Fatalf("expected 200 for healthz, got %d", w.Code)
	}
}

func TestQueryBearerTokenIsAcceptedOnlyForEvents(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	user := createLoginUser(t, authSvc, "query-token@e2m.local", contracts.UserRoleOwner)
	sessionToken, _, err := authSvc.Login(ctx, user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	w := do(t, h, http.MethodGet, "/api/v1/instances?access_token="+sessionToken, "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("ordinary API must reject query bearer token, got %d %s", w.Code, w.Body.String())
	}

	instance, err := st.CreateInstance(ctx, contracts.Instance{
		UserID: user.ID,
		Name:   "Query token connector",
		Kind:   contracts.InstanceKindSub2API,
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	connectorToken := "connector-query-token"
	enrollment, err := st.CreateConnectorEnrollment(ctx, contracts.ConnectorEnrollment{
		UserID:      user.ID,
		InstanceID:  instance.ID,
		ConnectorID: "connector-query-token",
		TokenHash:   hashConnectorToken("query-enrollment-token"),
	})
	if err != nil {
		t.Fatalf("create connector enrollment: %v", err)
	}
	connector, _, err := st.UseConnectorEnrollment(ctx, enrollment.TokenHash, contracts.Connector{
		ID:              enrollment.ConnectorID,
		InstanceID:      instance.ID,
		Version:         "0.1.0-test",
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		TokenHash:       hashConnectorToken(connectorToken),
	})
	if err != nil {
		t.Fatalf("enroll connector: %v", err)
	}
	w = do(t, h, http.MethodPost, "/api/v1/connectors/tasks/lease?access_token="+connectorToken, "", contracts.ConnectorTaskLeaseRequest{
		ConnectorID:     connector.ID,
		ProtocolVersion: contracts.ConnectorProtocolVersion,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("connector API must reject query bearer token, got %d %s", w.Code, w.Body.String())
	}

	admin := createLoginUser(t, authSvc, "query-token-admin@e2m.local", contracts.UserRolePlatformAdmin)
	adminSessionToken, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	testHTTP := httptest.NewServer(h)
	defer testHTTP.Close()
	resp, err := testHTTP.Client().Get(testHTTP.URL + "/api/v1/events/stream?access_token=" + adminSessionToken)
	if err != nil {
		t.Fatalf("open event stream with query bearer token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("event stream should accept query bearer token, got %d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/event-stream") {
		t.Fatalf("unexpected event stream content type %q", contentType)
	}
}

func TestEventStreamRejectsOwnerBecauseDetailedEventsContainInternalIdentity(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	owner := createLoginUser(t, authSvc, "events-owner@e2m.local", contracts.UserRoleOwner)
	token, _, err := authSvc.Login(context.Background(), owner.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	w := do(t, srv.Routes(), http.MethodGet, "/api/v1/events/stream", token, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("owner event stream: want 403, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestOwnerAuditResponseRedactsInternalEvidenceButAdminRetainsIt(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	ctx := context.Background()
	owner := createLoginUser(t, authSvc, "audit-redaction-owner@e2m.local", contracts.UserRoleOwner)
	admin := createLoginUser(t, authSvc, "audit-redaction-admin@e2m.local", contracts.UserRolePlatformAdmin)
	_, err := st.AppendAudit(ctx, contracts.OperationAudit{
		UserID: owner.ID, InstanceID: "known-owner-instance", ActorType: "workflow", ActorID: "auto-switch",
		Action: "account.disable_schedulable", RiskLevel: contracts.RiskLevelL1,
		TargetType: "account", TargetID: "remote-secret-key-id", RequestHash: "request-secret-hash",
		Result: "failed", ErrorMessage: "provider secret response", ApprovalID: "approval-secret",
		WorkflowRunID: "workflow-secret",
		Details:       map[string]string{"instance_name": "生产实例", "channel_id": "managed-channel-secret", "reason_code": "gateway_timeout"},
	})
	if err != nil {
		t.Fatalf("append audit: %v", err)
	}
	ownerToken, _, _ := authSvc.Login(ctx, owner.Email, "password123")
	adminToken, _, _ := authSvc.Login(ctx, admin.Email, "password123")

	ownerResponse := do(t, srv.Routes(), http.MethodGet, "/api/v1/audits", ownerToken, nil)
	if ownerResponse.Code != http.StatusOK {
		t.Fatalf("owner audits: %d %s", ownerResponse.Code, ownerResponse.Body.String())
	}
	var ownerAudits []contracts.OperationAudit
	if err := json.Unmarshal(ownerResponse.Body.Bytes(), &ownerAudits); err != nil || len(ownerAudits) != 1 {
		t.Fatalf("decode owner audits: err=%v audits=%+v", err, ownerAudits)
	}
	got := ownerAudits[0]
	if got.Action != "account.disable_schedulable" || got.Result != "failed" || got.InstanceID != "known-owner-instance" {
		t.Fatalf("owner lost operation facts: %+v", got)
	}
	if got.TargetID != "" || got.RequestHash != "" || got.ErrorMessage != "" || got.ApprovalID != "" || got.WorkflowRunID != "" ||
		got.Details["channel_id"] != "" || got.Details["instance_name"] != "生产实例" || got.Details["reason_code"] != "gateway_timeout" {
		t.Fatalf("owner audit leaked internal evidence: %+v", got)
	}

	adminResponse := do(t, srv.Routes(), http.MethodGet, "/api/v1/audits?user_id="+userIDString(owner.ID), adminToken, nil)
	if adminResponse.Code != http.StatusOK {
		t.Fatalf("admin audits: %d %s", adminResponse.Code, adminResponse.Body.String())
	}
	var adminAudits []contracts.OperationAudit
	if err := json.Unmarshal(adminResponse.Body.Bytes(), &adminAudits); err != nil || len(adminAudits) != 1 {
		t.Fatalf("decode admin audits: err=%v audits=%+v", err, adminAudits)
	}
	full := adminAudits[0]
	if full.TargetID != "remote-secret-key-id" || full.ErrorMessage != "provider secret response" ||
		full.RequestHash != "request-secret-hash" || full.ApprovalID != "approval-secret" || full.WorkflowRunID != "workflow-secret" {
		t.Fatalf("administrator should retain full audit evidence: %+v", full)
	}
}

func TestLoginFlowAndMe(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	h := srv.Routes()
	if _, err := authSvc.CreateUser(context.Background(), "admin@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	w := do(t, h, "POST", "/api/v1/auth/login", "", map[string]string{"email": "admin@e2m.local", "password": "password123"})
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)
	var lr struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &lr)
	if lr.Token == "" {
		t.Fatal("no token returned")
	}

	w = do(t, h, "GET", "/api/v1/auth/me", lr.Token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("me: %d", w.Code)
	}

	// Bad password -> 401.
	if w := do(t, h, "POST", "/api/v1/auth/login", "", map[string]string{"email": "admin@e2m.local", "password": "nope"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad password, got %d", w.Code)
	}
}

func TestUserScopingFailClosed(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	// Two users, each with one instance.
	userA := createLoginUser(t, authSvc, "a-admin@e2m.local", contracts.UserRoleOwner)
	userB := createStoreUser(t, st, "b-owner@e2m.local", contracts.UserRoleOwner)
	_, _ = st.CreateInstance(ctx, contracts.Instance{UserID: userA.ID, Name: "a-inst", Kind: contracts.InstanceKindSub2API})
	instB, _ := st.CreateInstance(ctx, contracts.Instance{UserID: userB.ID, Name: "b-inst", Kind: contracts.InstanceKindSub2API})

	token, _, _ := authSvc.Login(ctx, "a-admin@e2m.local", "password123")

	// Listing instances returns only user A's resources (plus seeded demo data is
	// not user A, so it must be filtered out).
	w := do(t, h, "GET", "/api/v1/instances", token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list instances: %d", w.Code)
	}
	var got []contracts.Instance
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	for _, in := range got {
		if in.UserID != userA.ID {
			t.Fatalf("user A saw instance from user %d", in.UserID)
		}
	}

	// Explicitly requesting user B's data -> 403.
	if w := do(t, h, "GET", "/api/v1/instances?user_id="+userIDString(userB.ID), token, nil); w.Code != http.StatusForbidden {
		t.Fatalf("cross-user list should be 403, got %d", w.Code)
	}

	// Reading user B's instance accounts -> 403 (fail-closed on the instance).
	if w := do(t, h, "GET", "/api/v1/instances/"+instB.ID+"/accounts", token, nil); w.Code != http.StatusForbidden {
		t.Fatalf("cross-user account read should be 403, got %d", w.Code)
	}
}

func TestUnknownRoleRejected(t *testing.T) {
	_, _, authSvc := newTestServer(t)
	ctx := context.Background()

	if _, err := authSvc.CreateUser(ctx, "viewer@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRole("viewer")}); err == nil {
		t.Fatal("expected removed viewer role to be rejected")
	}
}

func TestOwnerCannotManageUsers(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	_ = createLoginUser(t, authSvc, "owner@e2m.local", contracts.UserRoleOwner)
	token, _, _ := authSvc.Login(ctx, "owner@e2m.local", "password123")

	if w := do(t, h, "GET", "/api/v1/users", token, nil); w.Code != http.StatusForbidden {
		t.Fatalf("owner listing users should be 403, got %d", w.Code)
	}
}

func TestPlatformAdminUpdatesUserWithVersionAndAudit(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()
	admin := createLoginUser(t, authSvc, "admin@e2m.local", contracts.UserRoleAdmin)
	target := createLoginUser(t, authSvc, "managed@e2m.local", contracts.UserRoleClient)
	token, _, err := authSvc.Login(ctx, admin.Email, "password123")
	if err != nil {
		t.Fatalf("login admin: %v", err)
	}

	body := contracts.UpdateUserRequest{
		Email:             target.Email,
		DisplayName:       "Managed Account",
		Roles:             []contracts.UserRole{contracts.UserRoleSupplier},
		Enabled:           true,
		ExpectedUpdatedAt: target.UpdatedAt,
	}
	w := do(t, h, http.MethodPut, "/api/v1/users/"+userIDString(target.ID), token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("update user: %d %s", w.Code, w.Body.String())
	}
	var updated contracts.User
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated user: %v", err)
	}
	if updated.DisplayName != body.DisplayName || !auth.HasRole(updated, contracts.UserRoleSupplier) {
		t.Fatalf("unexpected updated user: %+v", updated)
	}

	audits, err := st.ListAudits(ctx, target.ID)
	if err != nil {
		t.Fatalf("list audits: %v", err)
	}
	found := false
	for _, audit := range audits {
		if audit.Action == "user.update" && audit.ActorID == admin.Email && audit.TargetID == userIDString(target.ID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("user update audit with real administrator not found: %+v", audits)
	}

	stale := body
	stale.DisplayName = "Stale Edit"
	w = do(t, h, http.MethodPut, "/api/v1/users/"+userIDString(target.ID), token, stale)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"stale_user"`) {
		t.Fatalf("stale update should be 409 stale_user, got %d %s", w.Code, w.Body.String())
	}
}

func TestSupplierOnlyCannotAccessOwnerSurfaces(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()

	account := createLoginUser(t, authSvc, "supplier@e2m.local", contracts.UserRoleSupplier)
	inst, _ := st.CreateInstance(ctx, contracts.Instance{UserID: account.ID, Name: "owner-gateway", Kind: contracts.InstanceKindSub2API})
	token, _, _ := authSvc.Login(ctx, "supplier@e2m.local", "password123")

	if w := do(t, h, "GET", "/api/v1/instances", token, nil); w.Code != http.StatusForbidden {
		t.Fatalf("supplier-only listing owner instances should be 403, got %d", w.Code)
	}
	if w := do(t, h, "POST", "/api/v1/instances", token, map[string]any{
		"user_id": account.ID,
		"name":    "new gateway",
		"kind":    "sub2api",
	}); w.Code != http.StatusForbidden {
		t.Fatalf("supplier-only creating owner instance should be 403, got %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/v1/instances/"+inst.ID+"/accounts", token, nil); w.Code != http.StatusForbidden {
		t.Fatalf("supplier-only reading owner instance accounts should be 403, got %d", w.Code)
	}
	if w := do(t, h, "GET", "/api/v1/connectors", token, nil); w.Code != http.StatusForbidden {
		t.Fatalf("supplier-only listing connectors should be 403, got %d", w.Code)
	}
}

func TestPublicAuthConfigAndRegisterDisabled(t *testing.T) {
	srv, _, _ := newTestServer(t)
	h := srv.Routes()

	w := do(t, h, "GET", "/api/v1/auth/public-config", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("public config: %d %s", w.Code, w.Body.String())
	}
	var cfg contracts.AuthPublicConfig
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.RegistrationEnabled || cfg.TurnstileEnabled || cfg.TurnstileSiteKey != "" {
		t.Fatalf("unexpected public config: %+v", cfg)
	}

	w = do(t, h, "POST", "/api/v1/auth/register", "", map[string]string{"email": "owner@example.com", "password": "password123", "display_name": "Owner"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected disabled 403, got %d %s", w.Code, w.Body.String())
	}
}

func TestRegisterSuccessAuditAndAccountIsolation(t *testing.T) {
	srv, st, authSvc := newTestServer(t)
	authSvc.ConfigureRegistration(auth.RegistrationConfig{Enabled: true})
	h := srv.Routes()

	w := do(t, h, "POST", "/api/v1/auth/register", "", map[string]string{
		"email":        "owner@example.com",
		"password":     "password123",
		"display_name": "Owner",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	requireNoStore(t, w)
	var res contracts.AuthRegisterResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if res.Token == "" || !auth.HasRole(res.User, contracts.UserRoleOwner) || res.User.ID == 0 {
		t.Fatalf("unexpected register response: %+v", res)
	}
	if res.User.PasswordHash != "" {
		t.Fatalf("password hash leaked in register response")
	}

	audits, _ := st.ListAudits(context.Background(), res.User.ID)
	found := false
	for _, a := range audits {
		if a.Action == "auth.register" && a.TargetID == userIDString(res.User.ID) && a.ActorID == res.User.Email {
			found = true
		}
		if a.RequestHash != "" || a.ErrorMessage != "" {
			t.Fatalf("register audit must not contain sensitive payload/error: %+v", a)
		}
	}
	if !found {
		t.Fatalf("auth.register audit not found: %+v", audits)
	}

	other := createStoreUser(t, st, "other-owner@e2m.local", contracts.UserRoleOwner)
	_, _ = st.CreateInstance(context.Background(), contracts.Instance{UserID: res.User.ID, Name: "own", Kind: contracts.InstanceKindSub2API})
	_, _ = st.CreateInstance(context.Background(), contracts.Instance{UserID: other.ID, Name: "other", Kind: contracts.InstanceKindSub2API})
	w = do(t, h, "GET", "/api/v1/instances", res.Token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list instances: %d %s", w.Code, w.Body.String())
	}
	var instances []contracts.Instance
	_ = json.Unmarshal(w.Body.Bytes(), &instances)
	for _, inst := range instances {
		if inst.UserID != res.User.ID {
			t.Fatalf("registered account saw other account data: %+v", inst)
		}
	}
	w = do(t, h, "GET", "/api/v1/instances?user_id="+userIDString(other.ID), res.Token, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected cross-user 403, got %d %s", w.Code, w.Body.String())
	}
}

func TestLegacyAgentHeartbeatRoutesRemoved(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	h := srv.Routes()
	user := createLoginUser(t, authSvc, "no-heartbeat@e2m.local", contracts.UserRoleOwner)
	token, _, err := authSvc.Login(context.Background(), user.Email, "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	for _, request := range []struct {
		method string
		path   string
	}{
		{method: http.MethodPost, path: "/api/v1/agents/heartbeat"},
		{method: http.MethodGet, path: "/api/v1/agents/heartbeats"},
	} {
		w := do(t, h, request.method, request.path, token, map[string]any{"agent_id": "forged"})
		if w.Code != http.StatusNotFound {
			t.Fatalf("removed %s %s route returned %d %s", request.method, request.path, w.Code, w.Body.String())
		}
	}
}

func TestRegisterDuplicateAndSuffixRejected(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	authSvc.ConfigureRegistration(auth.RegistrationConfig{Enabled: true, EmailSuffixWhitelist: []string{"@example.com"}})
	h := srv.Routes()

	body := map[string]string{"email": "owner@example.com", "password": "password123", "display_name": "Owner"}
	if w := do(t, h, "POST", "/api/v1/auth/register", "", body); w.Code != http.StatusCreated {
		t.Fatalf("first register: %d %s", w.Code, w.Body.String())
	}
	if w := do(t, h, "POST", "/api/v1/auth/register", "", body); w.Code != http.StatusConflict {
		t.Fatalf("duplicate should be 409, got %d %s", w.Code, w.Body.String())
	}
	if w := do(t, h, "POST", "/api/v1/auth/register", "", map[string]string{"email": "owner@blocked.test", "password": "password123", "display_name": "Blocked"}); w.Code != http.StatusBadRequest {
		t.Fatalf("suffix rejection should be 400, got %d %s", w.Code, w.Body.String())
	}
}

func TestRegisterTurnstileRequiredAndFailed(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	authSvc.ConfigureRegistration(auth.RegistrationConfig{Enabled: true, TurnstileEnabled: true, TurnstileSiteKey: "site", TurnstileSecretKey: "secret"})
	authSvc.SetTurnstileVerifier(&stubHTTPTurnstileVerifier{err: auth.ErrTurnstileVerificationFailed})
	h := srv.Routes()

	w := do(t, h, "GET", "/api/v1/auth/public-config", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("config: %d", w.Code)
	}
	var cfg contracts.AuthPublicConfig
	_ = json.Unmarshal(w.Body.Bytes(), &cfg)
	if !cfg.TurnstileEnabled || cfg.TurnstileSiteKey != "site" {
		t.Fatalf("turnstile public config wrong: %+v", cfg)
	}

	w = do(t, h, "POST", "/api/v1/auth/register", "", map[string]string{"email": "a@example.com", "password": "password123", "display_name": "A"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing token should be 400, got %d %s", w.Code, w.Body.String())
	}
	w = do(t, h, "POST", "/api/v1/auth/register", "", map[string]string{"email": "a@example.com", "password": "password123", "display_name": "A", "turnstile_token": "bad"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("failed token should be 400, got %d %s", w.Code, w.Body.String())
	}
}

type stubHTTPTurnstileVerifier struct {
	err error
}

func (v *stubHTTPTurnstileVerifier) Verify(context.Context, string, string, string, string) error {
	return v.err
}

func TestPlatformAdminCanManageAuthSystemSettings(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()
	if _, err := authSvc.CreateUser(ctx, "admin@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	token, _, err := authSvc.Login(ctx, "admin@e2m.local", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	body := map[string]any{
		"registration_enabled":                true,
		"registration_email_suffix_whitelist": []string{"example.com"},
		"turnstile_enabled":                   true,
		"turnstile_site_key":                  "site-key",
		"turnstile_secret_key":                "secret-key",
	}
	w := do(t, h, "PUT", "/api/v1/system/auth-settings", token, body)
	if w.Code != http.StatusOK {
		t.Fatalf("update settings: %d %s", w.Code, w.Body.String())
	}
	var settings contracts.AuthSystemSettings
	if err := json.Unmarshal(w.Body.Bytes(), &settings); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if !settings.RegistrationEnabled || settings.RegistrationEmailSuffixWhitelist[0] != "@example.com" || settings.TurnstileSiteKey != "site-key" {
		t.Fatalf("unexpected settings: %+v", settings)
	}
	if !settings.TurnstileSecretConfigured || settings.TurnstileSecretKey != "" {
		t.Fatalf("secret must only be reported as configured: %+v", settings)
	}

	w = do(t, h, "GET", "/api/v1/auth/public-config", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("public config: %d", w.Code)
	}
	var cfg contracts.AuthPublicConfig
	_ = json.Unmarshal(w.Body.Bytes(), &cfg)
	if !cfg.RegistrationEnabled || !cfg.TurnstileEnabled || cfg.TurnstileSiteKey != "site-key" {
		t.Fatalf("public config did not reflect settings: %+v", cfg)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("secret-key")) {
		t.Fatalf("public config leaked turnstile secret: %s", w.Body.String())
	}
}

func TestAuthSystemSettingsEmptyWhitelistReturnsArray(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()
	if _, err := authSvc.CreateUser(ctx, "admin@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	token, _, _ := authSvc.Login(ctx, "admin@e2m.local", "password123")

	w := do(t, h, "PUT", "/api/v1/system/auth-settings", token, map[string]any{
		"registration_enabled":                true,
		"registration_email_suffix_whitelist": []string{},
		"turnstile_enabled":                   false,
		"turnstile_site_key":                  "",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update settings: %d %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if got := string(raw["registration_email_suffix_whitelist"]); got != "[]" {
		t.Fatalf("empty whitelist must serialize as [], got %s", got)
	}
	if _, ok := raw["turnstile_secret_key"]; ok {
		t.Fatalf("secret key must not be serialized: %s", w.Body.String())
	}
}

func TestOwnerCannotManageAuthSystemSettings(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()
	_ = createLoginUser(t, authSvc, "owner@e2m.local", contracts.UserRoleOwner)
	token, _, err := authSvc.Login(ctx, "owner@e2m.local", "password123")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	if w := do(t, h, "GET", "/api/v1/system/auth-settings", token, nil); w.Code != http.StatusForbidden {
		t.Fatalf("owner get settings should be 403, got %d", w.Code)
	}
	if w := do(t, h, "PUT", "/api/v1/system/auth-settings", token, map[string]any{"registration_enabled": true}); w.Code != http.StatusForbidden {
		t.Fatalf("owner update settings should be 403, got %d", w.Code)
	}
}

func TestAuthSystemSettingsDriveRegistrationAndKeepSecret(t *testing.T) {
	srv, _, authSvc := newTestServer(t)
	h := srv.Routes()
	ctx := context.Background()
	if _, err := authSvc.CreateUser(ctx, "admin@e2m.local", "password123", "", []contracts.UserRole{contracts.UserRolePlatformAdmin}); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	token, _, _ := authSvc.Login(ctx, "admin@e2m.local", "password123")
	authSvc.SetTurnstileVerifier(&stubHTTPTurnstileVerifier{})

	first := map[string]any{
		"registration_enabled":                true,
		"registration_email_suffix_whitelist": []string{"example.com"},
		"turnstile_enabled":                   true,
		"turnstile_site_key":                  "site-one",
		"turnstile_secret_key":                "secret-one",
	}
	if w := do(t, h, "PUT", "/api/v1/system/auth-settings", token, first); w.Code != http.StatusOK {
		t.Fatalf("first update: %d %s", w.Code, w.Body.String())
	}
	second := map[string]any{
		"registration_enabled":                true,
		"registration_email_suffix_whitelist": []string{"example.com"},
		"turnstile_enabled":                   true,
		"turnstile_site_key":                  "site-two",
	}
	w := do(t, h, "PUT", "/api/v1/system/auth-settings", token, second)
	if w.Code != http.StatusOK {
		t.Fatalf("second update: %d %s", w.Code, w.Body.String())
	}
	var settings contracts.AuthSystemSettings
	_ = json.Unmarshal(w.Body.Bytes(), &settings)
	if !settings.TurnstileSecretConfigured || settings.TurnstileSiteKey != "site-two" {
		t.Fatalf("empty secret update should keep existing secret: %+v", settings)
	}

	w = do(t, h, "POST", "/api/v1/auth/register", "", map[string]string{
		"email":           "owner@example.com",
		"password":        "password123",
		"display_name":    "Owner",
		"turnstile_token": "ok",
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("register with persisted settings: %d %s", w.Code, w.Body.String())
	}

	clear := map[string]any{
		"registration_enabled":                true,
		"registration_email_suffix_whitelist": []string{"example.com"},
		"turnstile_enabled":                   true,
		"turnstile_site_key":                  "site-two",
		"clear_turnstile_secret":              true,
	}
	if w := do(t, h, "PUT", "/api/v1/system/auth-settings", token, clear); w.Code != http.StatusOK {
		t.Fatalf("clear secret: %d %s", w.Code, w.Body.String())
	}
	w = do(t, h, "POST", "/api/v1/auth/register", "", map[string]string{
		"email":           "next@example.com",
		"password":        "password123",
		"display_name":    "Next",
		"turnstile_token": "ok",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("cleared secret should make turnstile verification fail, got %d %s", w.Code, w.Body.String())
	}
}

package connector

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalConfigStoreSavesCredentialOnlyLocallyAndExposesPublicSummary(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	cfg := GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://sub2api:8080",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "secret-admin-key"},
	}
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save local config: %v", err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load local config: %v", err)
	}
	if loaded.Credentials.XAPIKey != "secret-admin-key" {
		t.Fatalf("credential should be present locally")
	}
	public := loaded.Public()
	if !public.GatewayConfigured || !public.CredentialConfigured["x_api_key"] {
		t.Fatalf("public summary should expose configured flags: %+v", public)
	}
	raw, err := json.Marshal(public)
	if err != nil {
		t.Fatalf("marshal public: %v", err)
	}
	if strings.Contains(string(raw), "secret-admin-key") {
		t.Fatalf("public summary leaked credential: %s", string(raw))
	}
	if public.Runtime.GatewayRequestTimeoutSeconds != 15 || public.Runtime.LogLevel != LocalLogLevelInfo {
		t.Fatalf("legacy config should expose normalized runtime defaults: %+v", public.Runtime)
	}
}

func TestLocalUIDirectAccessEstablishesSessionAndSavesConfig(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	handler := NewLocalAPIHandler(LocalAPIConfig{
		Store: store,
		Token: "local-token",
		Default: GatewayLocalConfig{
			GatewayKind: "sub2api",
			GatewayURL:  "http://sub2api:8080",
			Auth:        GatewayAuthXAPIKey,
		},
	})

	// A direct page load establishes an HttpOnly, same-site session. The browser
	// then presents that cookie automatically to local API calls.
	req := localRequest(http.MethodGet, "http://127.0.0.1/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("load local UI directly: %d %s", rec.Code, rec.Body.String())
	}
	response := rec.Result()
	wantCookieName, wantCookieValue, ok := localUISessionCredentials(req, "local-token")
	if !ok {
		t.Fatal("derive local UI session cookie")
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == wantCookieName {
			sessionCookie = cookie
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value != wantCookieValue || sessionCookie.Value == "local-token" || !sessionCookie.HttpOnly ||
		sessionCookie.SameSite != http.SameSiteStrictMode || sessionCookie.Path != "/api/local/" {
		t.Fatalf("direct page load did not issue a scoped HttpOnly session cookie: %+v", sessionCookie)
	}
	for _, forbidden := range []string{"sessionStorage", "X-E2M-Local-Token", "#token", "页面访问令牌"} {
		if strings.Contains(localUIAppJS, forbidden) || strings.Contains(LocalUIIndexHTML, forbidden) {
			t.Fatalf("local UI still exposes the legacy token flow %q", forbidden)
		}
	}
	if !strings.Contains(localUIAppJS, `credentials = "same-origin"`) {
		t.Fatal("local UI requests must use only same-origin credentials")
	}
	if strings.Count(LocalUIIndexHTML, `data-secret-toggle=`) != 3 ||
		!strings.Contains(localUIAppJS, "toggleSecretVisibility") {
		t.Fatal("local UI must provide visibility toggles for all secret credential inputs")
	}
	if !strings.Contains(LocalUIIndexHTML, `id="testButton" disabled`) ||
		!strings.Contains(LocalUIIndexHTML, `id="saveButton" disabled`) ||
		!strings.Contains(localUIAppJS, "setAuthorized(false)") {
		t.Fatal("local UI must keep gateway actions disabled until local configuration loads")
	}
	if !strings.Contains(localUIAppJS, "Docker 中的 127.0.0.1/localhost") {
		t.Fatal("local UI must explain container loopback addresses before testing")
	}
	if strings.Contains(LocalUIIndexHTML, `value="custom"`) || strings.Contains(LocalUIIndexHTML, `name="auth"`) {
		t.Fatal("local UI must only offer supported gateways and derive authentication from gateway kind")
	}
	if !strings.Contains(LocalUIIndexHTML, `id="requestTimeout"`) ||
		!strings.Contains(LocalUIIndexHTML, `id="coreTestButton" disabled`) ||
		!strings.Contains(LocalUIIndexHTML, `>测试（不保存）</button>`) {
		t.Fatal("local UI must expose runtime settings, Core test, and the named gateway test")
	}

	req = localRequest(http.MethodGet, "http://127.0.0.1/api/local/connector/config", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized without token, got %d", rec.Code)
	}

	body := `{"gateway_kind":"sub2api","gateway_url":"http://sub2api:8080","auth":"x-api-key","credentials":{"x_api_key":"secret"}}`
	req = localRequest(http.MethodPost, "http://127.0.0.1/api/local/connector/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookie)
	req.Header.Set("Origin", "http://127.0.0.1")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save config: %d %s", rec.Code, rec.Body.String())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if loaded.Credentials.XAPIKey != "secret" {
		t.Fatalf("config was not persisted locally: %+v", loaded)
	}

	containerHandler := NewLocalAPIHandler(LocalAPIConfig{
		Store:             store,
		Token:             "local-token",
		AllowPrivatePeers: true,
	})
	req = localRequest(http.MethodGet, "http://127.0.0.1/api/local/connector/config", nil)
	req.Header.Set("X-E2M-Local-Token", "local-token")
	rec = httptest.NewRecorder()
	containerHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"container_mode":true`) {
		t.Fatalf("container mode was not exposed to the local UI: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLocalUISessionCookiesAreOriginScopedAndHeaderDoesNotFallBack(t *testing.T) {
	handler := NewLocalAPIHandler(LocalAPIConfig{Store: NewLocalConfigStore(t.TempDir()), Token: "local-token"})
	pageCookie := func(target string) *http.Cookie {
		t.Helper()
		req := localRequest(http.MethodGet, target, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("%s issued %d cookies, want 1", target, len(cookies))
		}
		return cookies[0]
	}
	one := pageCookie("http://127.0.0.1:18081/")
	two := pageCookie("http://127.0.0.1:18082/")
	if one.Name == two.Name || one.Value == two.Value {
		t.Fatalf("Connector origins share a local UI cookie: one=%s two=%s", one.Name, two.Name)
	}

	req := localRequest(http.MethodGet, "http://127.0.0.1:18081/api/local/connector/config", nil)
	req.AddCookie(one)
	req.Header.Set("X-E2M-Local-Token", "wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid explicit header fell back to cookie: %d", rec.Code)
	}

	req = localRequest(http.MethodGet, "http://127.0.0.1:18081/api/local/connector/config", nil)
	req.AddCookie(one)
	req.AddCookie(one)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate session cookies were accepted: %d", rec.Code)
	}

	req = localRequest(http.MethodGet, "http://127.0.0.1:18081/localui/app.js", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if len(rec.Result().Cookies()) != 0 {
		t.Fatal("static assets must not issue a local UI session cookie")
	}
}

func TestLocalRuntimeSettingsNormalizeValidateAndPersist(t *testing.T) {
	var legacy GatewayLocalConfig
	if err := json.Unmarshal([]byte(`{"gateway_kind":"sub2api","gateway_url":"http://gateway:8080","auth":"x-api-key","credentials":{"x_api_key":"secret"}}`), &legacy); err != nil {
		t.Fatalf("decode legacy config: %v", err)
	}
	legacy.Normalize()
	if legacy.Runtime.GatewayRequestTimeoutSeconds != 15 || legacy.Runtime.LogLevel != LocalLogLevelInfo {
		t.Fatalf("legacy runtime defaults = %+v", legacy.Runtime)
	}

	store := NewLocalConfigStore(t.TempDir())
	handler := NewLocalAPIHandler(LocalAPIConfig{Store: store, Token: "local-token"})
	valid := `{"gateway_kind":"sub2api","gateway_url":"http://gateway:8080","auth":"x-api-key","credentials":{"x_api_key":"secret"},"runtime":{"gateway_request_timeout_seconds":20,"log_level":"debug"}}`
	rec := postLocalAPI(handler, "config", valid)
	if rec.Code != http.StatusOK {
		t.Fatalf("save runtime settings: %d %s", rec.Code, rec.Body.String())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load runtime settings: %v", err)
	}
	if loaded.Runtime.GatewayRequestTimeoutSeconds != 20 || loaded.Runtime.LogLevel != LocalLogLevelDebug {
		t.Fatalf("saved runtime settings = %+v", loaded.Runtime)
	}
	for _, invalid := range []string{
		`{"gateway_kind":"sub2api","gateway_url":"http://gateway:8080","auth":"x-api-key","runtime":{"gateway_request_timeout_seconds":4,"log_level":"info"}}`,
		`{"gateway_kind":"sub2api","gateway_url":"http://gateway:8080","auth":"x-api-key","runtime":{"gateway_request_timeout_seconds":15,"log_level":"trace"}}`,
	} {
		if rec := postLocalAPI(handler, "config", invalid); rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid runtime status = %d, want 400: %s", rec.Code, rec.Body.String())
		}
	}
}

func TestLocalRuntimeSettingsApplyImmediatelyAfterSave(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	var applied []LocalRuntimeSettings
	store.SetRuntimeApply(func(settings LocalRuntimeSettings) {
		applied = append(applied, settings)
	})
	if err := store.Save(GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://gateway:8080",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "secret"},
		Runtime: LocalRuntimeSettings{
			GatewayRequestTimeoutSeconds: 20,
			LogLevel:                     LocalLogLevelDebug,
		},
	}); err != nil {
		t.Fatalf("save runtime settings: %v", err)
	}
	if len(applied) != 1 || applied[0].GatewayRequestTimeoutSeconds != 20 || applied[0].LogLevel != LocalLogLevelDebug {
		t.Fatalf("runtime callback = %+v", applied)
	}
}

func TestGatewayCandidateTestUsesRuntimeTimeout(t *testing.T) {
	started := make(chan struct{}, 1)
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	handler := NewLocalAPIHandler(LocalAPIConfig{
		Store:  NewLocalConfigStore(t.TempDir()),
		Token:  "local-token",
		Client: &http.Client{Transport: transport, Timeout: time.Minute},
	})
	body := `{"gateway_kind":"sub2api","gateway_url":"http://gateway:8080","auth":"x-api-key","credentials":{"x_api_key":"secret"},"runtime":{"gateway_request_timeout_seconds":5,"log_level":"info"}}`
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { requestDone <- postLocalAPI(handler, "test", body) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("gateway test did not start")
	}
	select {
	case rec := <-requestDone:
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("timed out gateway test = %d: %s", rec.Code, rec.Body.String())
		}
	case <-time.After(7 * time.Second):
		t.Fatal("candidate gateway test did not honor five second timeout")
	}
}

func TestLocalAPIDoesNotReuseCredentialsAcrossGatewayScopeChanges(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		fresh GatewayLocalCredentials
	}{
		{
			name:  "URL origin scheme",
			body:  `{"gateway_kind":"sub2api","gateway_url":"https://gateway:8080/admin","auth":"x-api-key"}`,
			fresh: GatewayLocalCredentials{XAPIKey: "fresh-key"},
		},
		{
			name:  "URL origin host",
			body:  `{"gateway_kind":"sub2api","gateway_url":"http://other-gateway:8080/admin","auth":"x-api-key"}`,
			fresh: GatewayLocalCredentials{XAPIKey: "fresh-key"},
		},
		{
			name:  "URL origin port",
			body:  `{"gateway_kind":"sub2api","gateway_url":"http://gateway:8081/admin","auth":"x-api-key"}`,
			fresh: GatewayLocalCredentials{XAPIKey: "fresh-key"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := savedLocalConfigStore(t)
			transport := &recordingLocalTransport{}
			handler := newTestLocalAPIHandler(store, transport)

			for _, endpoint := range []string{"config", "test"} {
				rec := postLocalAPI(handler, endpoint, tt.body)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("%s without fresh credentials = %d, want %d: %s", endpoint, rec.Code, http.StatusBadRequest, rec.Body.String())
				}
			}
			if transport.callCount() != 0 {
				t.Fatal("invalid test candidate reached the gateway transport")
			}
			loaded, err := store.Load()
			if err != nil {
				t.Fatalf("load unchanged config: %v", err)
			}
			if loaded.GatewayURL != "http://gateway:8080/base" || loaded.Credentials.XAPIKey != "saved-key" {
				t.Fatalf("rejected save changed stored config: %+v", loaded)
			}

			freshBody := withLocalCredentials(t, tt.body, tt.fresh)
			rec := postLocalAPI(handler, "test", freshBody)
			if rec.Code != http.StatusOK {
				t.Fatalf("test with fresh credentials: %d %s", rec.Code, rec.Body.String())
			}
			request := transport.lastRequest()
			assertRequestUsesOnlyCredentials(t, request, tt.fresh)

			rec = postLocalAPI(handler, "config", freshBody)
			if rec.Code != http.StatusOK {
				t.Fatalf("save with fresh credentials: %d %s", rec.Code, rec.Body.String())
			}
			loaded, err = store.Load()
			if err != nil {
				t.Fatalf("load changed config: %v", err)
			}
			if loaded.Credentials != tt.fresh {
				t.Fatalf("saved credentials = %+v, want only %+v", loaded.Credentials, tt.fresh)
			}
		})
	}
}

func TestGatewayConfigRejectsUnsupportedKindAndMismatchedAuth(t *testing.T) {
	for _, cfg := range []GatewayLocalConfig{
		{GatewayKind: "custom", GatewayURL: "http://gateway:8080", Auth: GatewayAuthXAPIKey, Credentials: GatewayLocalCredentials{XAPIKey: "secret"}},
		{GatewayKind: "sub2api", GatewayURL: "http://gateway:8080", Auth: GatewayAuthBearer, Credentials: GatewayLocalCredentials{BearerToken: "secret"}},
	} {
		if err := ValidateGatewayLocalConfig(cfg); err == nil {
			t.Fatalf("expected unsupported gateway/auth pair to fail: %+v", cfg)
		}
	}
}

func TestLocalAPIReusesCredentialsForUnchangedScopeAndPathOnlyURLChange(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
	}{
		{name: "unchanged config", url: "http://gateway:8080/base"},
		{name: "path-only URL change", url: "http://gateway:8080/other/path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := savedLocalConfigStore(t)
			transport := &recordingLocalTransport{}
			handler := newTestLocalAPIHandler(store, transport)
			body := `{"gateway_kind":"sub2api","gateway_url":"` + tt.url + `","auth":"x-api-key"}`

			rec := postLocalAPI(handler, "test", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("test with retained credentials: %d %s", rec.Code, rec.Body.String())
			}
			assertRequestUsesOnlyCredentials(t, transport.lastRequest(), GatewayLocalCredentials{XAPIKey: "saved-key"})

			rec = postLocalAPI(handler, "config", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("save with retained credentials: %d %s", rec.Code, rec.Body.String())
			}
			loaded, err := store.Load()
			if err != nil {
				t.Fatalf("load saved config: %v", err)
			}
			if loaded.GatewayURL != tt.url || loaded.Credentials.XAPIKey != "saved-key" {
				t.Fatalf("saved config did not retain scoped credential: %+v", loaded)
			}
		})
	}
}

func TestLocalAPIRejectsLegacyQueryTokenAndUnsafeOrigins(t *testing.T) {
	handler := NewLocalAPIHandler(LocalAPIConfig{
		Store: NewLocalConfigStore(t.TempDir()),
		Token: "local-token",
		Default: GatewayLocalConfig{
			GatewayKind: "sub2api",
			GatewayURL:  "http://sub2api:8080",
			Auth:        GatewayAuthXAPIKey,
		},
	})
	body := `{"gateway_kind":"sub2api","gateway_url":"http://sub2api:8080","auth":"x-api-key","credentials":{"x_api_key":"secret"}}`

	tests := []struct {
		name       string
		target     string
		remoteAddr string
		token      string
		origin     string
		fetchSite  string
		wantStatus int
	}{
		{
			name:       "query token is ignored",
			target:     "http://127.0.0.1/api/local/connector/config?token=local-token",
			origin:     "http://127.0.0.1",
			fetchSite:  "same-origin",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "write without origin",
			target:     "http://127.0.0.1/api/local/connector/config",
			token:      "local-token",
			fetchSite:  "same-origin",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "cross origin write",
			target:     "http://127.0.0.1/api/local/connector/config",
			token:      "local-token",
			origin:     "http://localhost",
			fetchSite:  "same-origin",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "cross site fetch",
			target:     "http://127.0.0.1/api/local/connector/config",
			token:      "local-token",
			origin:     "http://127.0.0.1",
			fetchSite:  "cross-site",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "non loopback peer",
			target:     "http://127.0.0.1/api/local/connector/config",
			remoteAddr: "192.0.2.10:43123",
			token:      "local-token",
			origin:     "http://127.0.0.1",
			fetchSite:  "same-origin",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "non loopback host",
			target:     "http://connector.example/api/local/connector/config",
			token:      "local-token",
			origin:     "http://connector.example",
			fetchSite:  "same-origin",
			wantStatus: http.StatusForbidden,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := localRequest(http.MethodPost, tt.target, strings.NewReader(body))
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}
			req.Header.Set("Content-Type", "application/json")
			if tt.token != "" {
				req.Header.Set("X-E2M-Local-Token", tt.token)
			}
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.fetchSite != "" {
				req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Frame-Options") != "DENY" {
				t.Fatalf("security headers missing: %+v", rec.Header())
			}
		})
	}
}

func TestLocalAPIPrivatePeerRequiresExplicitContainerMode(t *testing.T) {
	request := func(handler http.Handler) *httptest.ResponseRecorder {
		req := localRequest(http.MethodGet, "http://127.0.0.1/api/local/connector/config", nil)
		req.RemoteAddr = "172.18.0.1:43123"
		req.Header.Set("X-E2M-Local-Token", "local-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	base := LocalAPIConfig{Store: NewLocalConfigStore(t.TempDir()), Token: "local-token"}
	if rec := request(NewLocalAPIHandler(base)); rec.Code != http.StatusForbidden {
		t.Fatalf("private peer without container mode = %d, want 403", rec.Code)
	}
	base.AllowPrivatePeers = true
	if rec := request(NewLocalAPIHandler(base)); rec.Code != http.StatusOK {
		t.Fatalf("private peer in container mode = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	base.Token = "wrong-token"
	if rec := request(NewLocalAPIHandler(base)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("container mode must still require the local token, got %d", rec.Code)
	}
}

func TestLocalCoreTestUsesHealthzWithoutTokenRedirectOrBodyExposure(t *testing.T) {
	var request *http.Request
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		request = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`secret response body`)),
			Request:    req,
		}, nil
	})
	diagnostics := NewLocalDiagnostics()
	handler := NewLocalAPIHandler(LocalAPIConfig{
		Store:       NewLocalConfigStore(t.TempDir()),
		Token:       "local-token",
		CoreURL:     "http://core:8080",
		Client:      &http.Client{Transport: transport},
		Diagnostics: diagnostics,
	})
	rec := postLocalAPI(handler, "core-test", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("core test: %d %s", rec.Code, rec.Body.String())
	}
	if request == nil || request.Method != http.MethodGet || request.URL.String() != "http://core:8080/healthz" {
		t.Fatalf("core health request = %+v", request)
	}
	if request.Header.Get("Authorization") != "" || request.Header.Get("X-E2M-Connector-Token") != "" || len(request.Header) != 0 {
		t.Fatalf("core health request leaked auth headers: %+v", request.Header)
	}
	if strings.Contains(rec.Body.String(), "secret response body") {
		t.Fatalf("core response body was exposed: %s", rec.Body.String())
	}

	redirectTransport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://secret.internal/healthz"}},
			Body:       io.NopCloser(strings.NewReader("redirect secret")),
			Request:    req,
		}, nil
	})
	handler = NewLocalAPIHandler(LocalAPIConfig{Token: "local-token", CoreURL: "http://core:8080", Client: &http.Client{Transport: redirectTransport}})
	rec = postLocalAPI(handler, "core-test", `{}`)
	if rec.Code != http.StatusBadGateway || strings.Contains(rec.Body.String(), "secret.internal") || strings.Contains(rec.Body.String(), "redirect secret") {
		t.Fatalf("redirect must fail without exposure: %d %s", rec.Code, rec.Body.String())
	}
}

func TestLocalDiagnosticsAPIIsTypedAndRedacted(t *testing.T) {
	diagnostics := NewLocalDiagnostics()
	now := time.Now().UTC()
	diagnostics.Record(LocalDiagnosticCoreSync, LocalDiagnosticError, now, 25*time.Millisecond, 401, "Bearer super-secret raw error")
	diagnostics.RecordRetry(LocalDiagnosticGatewayTask, now, 3, now.Add(10*time.Second), "x-api-key=super-secret")
	handler := NewLocalAPIHandler(LocalAPIConfig{Token: "local-token", Diagnostics: diagnostics})
	req := localRequest(http.MethodGet, "http://127.0.0.1/api/local/connector/diagnostics", nil)
	req.Header.Set("X-E2M-Local-Token", "local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("diagnostics: %d %s", rec.Code, rec.Body.String())
	}
	raw := rec.Body.String()
	for _, forbidden := range []string{"super-secret", "Bearer", "x-api-key", "url", "header", "body", "response", "raw_error"} {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(forbidden)) {
			t.Fatalf("diagnostics exposed forbidden content %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"error_code":"operation_failed"`) || !strings.Contains(raw, `"failure_count":3`) {
		t.Fatalf("typed diagnostics missing bounded fields: %s", raw)
	}
}

func TestGatewayCandidateTestReturnsStableErrorCodes(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantCode string
	}{
		{name: "authentication", status: http.StatusUnauthorized, wantCode: "gateway_auth_failed"},
		{name: "missing", status: http.StatusNotFound, wantCode: "gateway_resource_not_found"},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantCode: "gateway_unavailable"},
		{name: "redirect", status: http.StatusFound, wantCode: "gateway_redirect_rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostics := NewLocalDiagnostics()
			handler := NewLocalAPIHandler(LocalAPIConfig{
				Store:       savedLocalConfigStore(t),
				Token:       "local-token",
				Diagnostics: diagnostics,
				Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: tt.status,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Request:    req,
					}, nil
				})},
			})
			rec := postLocalAPI(handler, "test", `{
				"gateway_kind":"sub2api",
				"gateway_url":"http://gateway:8080/base",
				"auth":"x-api-key"
			}`)
			if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), `"error_code":"`+tt.wantCode+`"`) {
				t.Fatalf("candidate test = %d %s, want %s", rec.Code, rec.Body.String(), tt.wantCode)
			}
			result := diagnostics.Snapshot().GatewayTest
			if result.ErrorCode != tt.wantCode || result.HTTPStatus != tt.status {
				t.Fatalf("diagnostic = %+v, want code %s and status %d", result, tt.wantCode, tt.status)
			}
		})
	}
}

func TestEnsureLocalUITokenReusesExistingToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-ui.token")
	first, err := EnsureLocalUIToken(path)
	if err != nil {
		t.Fatalf("ensure first token: %v", err)
	}
	second, err := EnsureLocalUIToken(path)
	if err != nil {
		t.Fatalf("ensure second token: %v", err)
	}
	if first == "" || first != second {
		t.Fatalf("token should be stable, got %q then %q", first, second)
	}
}

func localRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.RemoteAddr = "127.0.0.1:43123"
	return req
}

func savedLocalConfigStore(t *testing.T) *LocalConfigStore {
	t.Helper()
	store := NewLocalConfigStore(t.TempDir())
	if err := store.Save(GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://gateway:8080/base",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{
			XAPIKey:      "saved-key",
			NewAPIUserID: "stale-user",
			NewAPIToken:  "stale-newapi-token",
			BearerToken:  "stale-bearer",
		},
	}); err != nil {
		t.Fatalf("save initial config: %v", err)
	}
	return store
}

func newTestLocalAPIHandler(store *LocalConfigStore, transport http.RoundTripper) http.Handler {
	return NewLocalAPIHandler(LocalAPIConfig{
		Store:  store,
		Token:  "local-token",
		Client: &http.Client{Transport: transport},
	})
}

func postLocalAPI(handler http.Handler, endpoint, body string) *httptest.ResponseRecorder {
	req := localRequest(http.MethodPost, "http://127.0.0.1/api/local/connector/"+endpoint, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-E2M-Local-Token", "local-token")
	req.Header.Set("Origin", "http://127.0.0.1")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func withLocalCredentials(t *testing.T, body string, credentials GatewayLocalCredentials) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode test payload: %v", err)
	}
	payload["credentials"] = credentials
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode test payload: %v", err)
	}
	return string(raw)
}

func assertRequestUsesOnlyCredentials(t *testing.T, req *http.Request, want GatewayLocalCredentials) {
	t.Helper()
	if req == nil {
		t.Fatal("gateway transport received no request")
	}
	wantAuthorization := ""
	if want.NewAPIToken != "" {
		wantAuthorization = "Bearer " + want.NewAPIToken
	}
	if want.BearerToken != "" {
		wantAuthorization = "Bearer " + want.BearerToken
	}
	if got := req.Header.Get("x-api-key"); got != want.XAPIKey {
		t.Fatalf("x-api-key = %q, want %q", got, want.XAPIKey)
	}
	if got := req.Header.Get("New-Api-User"); got != want.NewAPIUserID {
		t.Fatalf("New-Api-User = %q, want %q", got, want.NewAPIUserID)
	}
	if got := req.Header.Get("Authorization"); got != wantAuthorization {
		t.Fatalf("Authorization = %q, want %q", got, wantAuthorization)
	}
}

type recordingLocalTransport struct {
	mu       sync.Mutex
	requests []*http.Request
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func (transport *recordingLocalTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, req.Clone(req.Context()))
	transport.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    req,
	}, nil
}

func (transport *recordingLocalTransport) callCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return len(transport.requests)
}

func (transport *recordingLocalTransport) lastRequest() *http.Request {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) == 0 {
		return nil
	}
	return transport.requests[len(transport.requests)-1]
}

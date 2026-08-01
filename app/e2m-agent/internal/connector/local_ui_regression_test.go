package connector

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalConfigAPIRuntimePatchPreservesOmittedSettings(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	if err := store.Save(GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://gateway.invalid",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "saved-key"},
		Runtime: LocalRuntimeSettings{
			GatewayRequestTimeoutSeconds: 17,
			LogLevel:                     LocalLogLevelDebug,
			QualityProbe: LocalQualityProbeSettings{
				Enabled:            true,
				MaxRequestsPerHour: 37,
				MinIntervalSeconds: 123,
			},
		},
	}); err != nil {
		t.Fatalf("save initial config: %v", err)
	}
	handler := NewLocalAPIHandler(LocalAPIConfig{Store: store, Token: "local-token"})

	postConfigJSON(t, handler, map[string]any{
		"gateway_kind": "sub2api",
		"gateway_url":  "http://gateway.invalid",
		"auth":         "x-api-key",
		"runtime": map[string]any{
			"gateway_request_timeout_seconds": 11,
		},
	})
	loaded := loadLocalConfig(t, store)
	if loaded.Runtime.GatewayRequestTimeoutSeconds != 11 || loaded.Runtime.LogLevel != LocalLogLevelDebug {
		t.Fatalf("top-level runtime patch overwrote omitted settings: %+v", loaded.Runtime)
	}
	assertQualityProbeSettings(t, loaded.Runtime.QualityProbe, LocalQualityProbeSettings{
		Enabled: true, MaxRequestsPerHour: 37, MinIntervalSeconds: 123,
	})

	postConfigJSON(t, handler, map[string]any{
		"gateway_kind": "sub2api",
		"gateway_url":  "http://gateway.invalid",
		"auth":         "x-api-key",
		"runtime": map[string]any{
			"log_level": "error",
		},
	})
	loaded = loadLocalConfig(t, store)
	if loaded.Runtime.GatewayRequestTimeoutSeconds != 11 || loaded.Runtime.LogLevel != LocalLogLevelError {
		t.Fatalf("nested runtime patch overwrote omitted settings: %+v", loaded.Runtime)
	}
	assertQualityProbeSettings(t, loaded.Runtime.QualityProbe, LocalQualityProbeSettings{
		Enabled: true, MaxRequestsPerHour: 37, MinIntervalSeconds: 123,
	})
}

func TestLocalConfigAPIRejectsRetiredObservationSettings(t *testing.T) {
	handler := NewLocalAPIHandler(LocalAPIConfig{Store: NewLocalConfigStore(t.TempDir()), Token: "local-token"})
	for _, retired := range []string{"quality_probe", "cpa_usage_statistics_enabled"} {
		t.Run(retired, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"gateway_kind": "sub2api",
				"gateway_url":  "http://gateway.invalid",
				"auth":         "x-api-key",
				"runtime":      map[string]any{retired: map[string]any{}},
			})
			if retired == "cpa_usage_statistics_enabled" {
				raw, err = json.Marshal(map[string]any{
					"gateway_kind": "sub2api",
					"gateway_url":  "http://gateway.invalid",
					"auth":         "x-api-key",
					"runtime":      map[string]any{retired: true},
				})
			}
			if err != nil {
				t.Fatalf("encode payload: %v", err)
			}
			rec := postLocalAPI(handler, "config", string(raw))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
				t.Fatalf("retired setting response = %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestLocalConfigAPIHidesRetiredObservationSettingsAndRoutes(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	if err := store.Save(GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://gateway.invalid",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "saved-key"},
		Runtime: LocalRuntimeSettings{
			QualityProbe:              LocalQualityProbeSettings{Enabled: true},
			CPAUsageStatisticsEnabled: true,
		},
	}); err != nil {
		t.Fatalf("save legacy config: %v", err)
	}
	handler := NewLocalAPIHandler(LocalAPIConfig{Store: store, Token: "local-token"})

	req := localRequest(http.MethodGet, "http://127.0.0.1/api/local/connector/config", nil)
	req.Header.Set("X-E2M-Local-Token", "local-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET config = %d %s", rec.Code, rec.Body.String())
	}
	for _, retired := range []string{"quality_probe", "cpa_usage_statistics_enabled"} {
		if strings.Contains(rec.Body.String(), retired) {
			t.Fatalf("public config still exposes retired field %q: %s", retired, rec.Body.String())
		}
	}

	for _, path := range []string{
		"/api/local/upstream-intelligence/sources",
		"/api/local/upstream-intelligence/sources/source-1/collect",
	} {
		req = localRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		req.Header.Set("X-E2M-Local-Token", "local-token")
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("retired route %s = %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestLocalConfigAPIStoresOnlySelectedAuthenticationCredentials(t *testing.T) {
	tests := []struct {
		name string
		kind string
		auth string
		want GatewayLocalCredentials
	}{
		{
			name: "sub2api",
			kind: "sub2api",
			auth: "x-api-key",
			want: GatewayLocalCredentials{XAPIKey: "x-secret"},
		},
		{
			name: "newapi",
			kind: "newapi",
			auth: "newapi",
			want: GatewayLocalCredentials{NewAPIUserID: "42", NewAPIToken: "new-secret"},
		},
		{
			name: "cpa",
			kind: "cpa",
			auth: "bearer",
			want: GatewayLocalCredentials{BearerToken: "bearer-secret"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewLocalConfigStore(t.TempDir())
			handler := NewLocalAPIHandler(LocalAPIConfig{Store: store, Token: "local-token"})
			postConfigJSON(t, handler, map[string]any{
				"gateway_kind": tt.kind,
				"gateway_url":  "http://gateway.invalid",
				"auth":         tt.auth,
				"credentials": map[string]any{
					"x_api_key":      "x-secret",
					"newapi_user_id": "42",
					"newapi_token":   "new-secret",
					"bearer_token":   "bearer-secret",
				},
			})

			loaded := loadLocalConfig(t, store)
			if loaded.Credentials != tt.want {
				t.Fatalf("stored credentials = %+v, want only %+v", loaded.Credentials, tt.want)
			}
		})
	}
}

func TestLocalConfigAPICleansLegacyInactiveCredentials(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	if err := store.Save(GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://gateway.invalid",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{
			XAPIKey:      "saved-key",
			NewAPIUserID: "stale-user",
			NewAPIToken:  "stale-newapi-token",
			BearerToken:  "stale-bearer-token",
		},
	}); err != nil {
		t.Fatalf("save legacy config: %v", err)
	}
	handler := NewLocalAPIHandler(LocalAPIConfig{Store: store, Token: "local-token"})
	postConfigJSON(t, handler, map[string]any{
		"gateway_kind": "sub2api",
		"gateway_url":  "http://gateway.invalid",
		"auth":         "x-api-key",
	})

	loaded := loadLocalConfig(t, store)
	want := GatewayLocalCredentials{XAPIKey: "saved-key"}
	if loaded.Credentials != want {
		t.Fatalf("legacy inactive credentials survived save: %+v", loaded.Credentials)
	}
}

func TestCandidateGatewayTestDoesNotChangeSavedRuntimeHealth(t *testing.T) {
	tests := []struct {
		name           string
		gatewayStatus  int
		wantAPIStatus  int
		wantDiagnostic LocalDiagnosticStatus
	}{
		{
			name:           "candidate succeeds",
			gatewayStatus:  http.StatusOK,
			wantAPIStatus:  http.StatusOK,
			wantDiagnostic: LocalDiagnosticOK,
		},
		{
			name:           "candidate fails",
			gatewayStatus:  http.StatusUnauthorized,
			wantAPIStatus:  http.StatusBadGateway,
			wantDiagnostic: LocalDiagnosticError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewLocalConfigStore(t.TempDir())
			if err := store.Save(GatewayLocalConfig{
				GatewayKind: "sub2api",
				GatewayURL:  "http://saved-gateway.invalid",
				Auth:        GatewayAuthXAPIKey,
				Credentials: GatewayLocalCredentials{XAPIKey: "saved-key"},
			}); err != nil {
				t.Fatalf("save gateway config: %v", err)
			}
			handler := NewLocalAPIHandler(LocalAPIConfig{
				Store: store,
				Token: "local-token",
				Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: tt.gatewayStatus,
						Header:     make(http.Header),
						Body:       io.NopCloser(strings.NewReader(`{}`)),
						Request:    req,
					}, nil
				})},
			})
			rec := postLocalAPI(handler, "test", `{
				"gateway_kind":"sub2api",
				"gateway_url":"http://saved-gateway.invalid",
				"auth":"x-api-key"
			}`)
			if rec.Code != tt.wantAPIStatus {
				t.Fatalf("candidate test status = %d, want %d: %s", rec.Code, tt.wantAPIStatus, rec.Body.String())
			}

			diagnostics := store.Diagnostics().Snapshot()
			if diagnostics.GatewayTest.Status != tt.wantDiagnostic {
				t.Fatalf("candidate diagnostic = %+v, want %s", diagnostics.GatewayTest, tt.wantDiagnostic)
			}
			if diagnostics.GatewayRequest.Status != LocalDiagnosticUnknown {
				t.Fatalf("candidate test polluted saved gateway request diagnostic: %+v", diagnostics.GatewayRequest)
			}

			state := New(Config{
				ConnectorID: testConnectorID,
				InstanceID:  testInstanceID,
				ConfigStore: store,
			}).runtimeState()
			if !state.GatewayConfigured || state.GatewayStatus != "configured" || state.ErrorCode != "" {
				t.Fatalf("candidate test changed saved runtime health: %+v", state)
			}
		})
	}
}

func TestSavedGatewayHealthIsScopedToConfigGeneration(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	cfg := GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://saved-gateway.invalid",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "saved-key"},
	}
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save initial config: %v", err)
	}
	initialGeneration := store.gatewayGenerationSnapshot()
	store.recordGatewayRequest(initialGeneration, LocalDiagnosticOK, time.Now(), time.Millisecond, "")
	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
	if state := conn.runtimeState(); state.GatewayStatus != "ok" || state.ErrorCode != "" {
		t.Fatalf("initial runtime health = %+v, want ok", state)
	}

	// Logging does not change how the Connector reaches or authenticates to the
	// gateway, so its last runtime result remains relevant.
	cfg.Runtime.LogLevel = LocalLogLevelDebug
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save log-level-only change: %v", err)
	}
	if got := store.gatewayGenerationSnapshot(); got != initialGeneration {
		t.Fatalf("log-level-only save advanced gateway generation from %d to %d", initialGeneration, got)
	}
	if got := store.Diagnostics().Snapshot().GatewayRequest.Status; got != LocalDiagnosticOK {
		t.Fatalf("log-level-only save cleared runtime health: %s", got)
	}

	// A timeout change affects the saved gateway client. Clear the prior result
	// and reject any in-flight completion that was started by the old generation.
	cfg.Runtime.GatewayRequestTimeoutSeconds = DefaultGatewayRequestTimeoutSeconds + 1
	if err := store.Save(cfg); err != nil {
		t.Fatalf("save gateway semantic change: %v", err)
	}
	currentGeneration := store.gatewayGenerationSnapshot()
	if currentGeneration <= initialGeneration {
		t.Fatalf("gateway semantic save did not advance generation: old=%d current=%d", initialGeneration, currentGeneration)
	}
	if got := store.Diagnostics().Snapshot().GatewayRequest.Status; got != LocalDiagnosticUnknown {
		t.Fatalf("gateway semantic save retained old runtime health: %s", got)
	}
	if state := conn.runtimeState(); state.GatewayStatus != "configured" || state.ErrorCode != "" {
		t.Fatalf("new config inherited old runtime health: %+v", state)
	}

	store.recordGatewayRequest(initialGeneration, LocalDiagnosticError, time.Now(), time.Millisecond, "gateway_timeout")
	if got := store.Diagnostics().Snapshot().GatewayRequest.Status; got != LocalDiagnosticUnknown {
		t.Fatalf("stale in-flight result repopulated runtime health: %s", got)
	}
	store.recordGatewayRequest(currentGeneration, LocalDiagnosticError, time.Now(), time.Millisecond, "gateway_timeout")
	if state := conn.runtimeState(); state.GatewayStatus != "error" || state.ErrorCode != "gateway_timeout" {
		t.Fatalf("current generation runtime health = %+v, want gateway_timeout", state)
	}
}

func TestGatewayRuntimeSemanticsIncludeConnectionInputs(t *testing.T) {
	base := GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "https://gateway.invalid/root",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "saved-key"},
		Runtime: LocalRuntimeSettings{
			GatewayRequestTimeoutSeconds: 15,
			LogLevel:                     LocalLogLevelInfo,
			QualityProbe: LocalQualityProbeSettings{
				Enabled: true, MaxRequestsPerHour: 12, MinIntervalSeconds: 60,
			},
		},
	}
	base.Normalize()
	tests := []struct {
		name   string
		mutate func(*GatewayLocalConfig)
		want   bool
	}{
		{name: "log level only", mutate: func(cfg *GatewayLocalConfig) { cfg.Runtime.LogLevel = LocalLogLevelDebug }, want: true},
		{name: "kind", mutate: func(cfg *GatewayLocalConfig) { cfg.GatewayKind = "newapi" }},
		{name: "url", mutate: func(cfg *GatewayLocalConfig) { cfg.GatewayURL += "/v2" }},
		{name: "auth", mutate: func(cfg *GatewayLocalConfig) { cfg.Auth = GatewayAuthBearer }},
		{name: "credential", mutate: func(cfg *GatewayLocalConfig) { cfg.Credentials.XAPIKey = "replacement-key" }},
		{name: "timeout", mutate: func(cfg *GatewayLocalConfig) { cfg.Runtime.GatewayRequestTimeoutSeconds++ }},
		{name: "retired quality probe", mutate: func(cfg *GatewayLocalConfig) { cfg.Runtime.QualityProbe.Enabled = false }, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := base
			tt.mutate(&candidate)
			if got := sameGatewayRuntimeSemantics(base, candidate); got != tt.want {
				t.Fatalf("sameGatewayRuntimeSemantics() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestGatewayConfigAndGenerationAreLoadedAtomically(t *testing.T) {
	store := NewLocalConfigStore(t.TempDir())
	initial := GatewayLocalConfig{
		GatewayKind: "sub2api",
		GatewayURL:  "http://gateway-one.invalid",
		Auth:        GatewayAuthXAPIKey,
		Credentials: GatewayLocalCredentials{XAPIKey: "key-one"},
	}
	if err := store.Save(initial); err != nil {
		t.Fatalf("save initial config: %v", err)
	}
	firstCfg, firstGeneration, err := store.loadWithGatewayGeneration()
	if err != nil {
		t.Fatalf("load initial config and generation: %v", err)
	}
	if firstCfg.GatewayURL != initial.GatewayURL {
		t.Fatalf("initial gateway url = %q, want %q", firstCfg.GatewayURL, initial.GatewayURL)
	}

	updated := initial
	updated.GatewayURL = "http://gateway-two.invalid"
	updated.Credentials.XAPIKey = "key-two"
	if err := store.Save(updated); err != nil {
		t.Fatalf("save updated config: %v", err)
	}
	secondCfg, secondGeneration, err := store.loadWithGatewayGeneration()
	if err != nil {
		t.Fatalf("load updated config and generation: %v", err)
	}
	if secondCfg.GatewayURL != updated.GatewayURL || secondCfg.Credentials.XAPIKey != updated.Credentials.XAPIKey {
		t.Fatalf("updated config snapshot = %+v, want url and credential from one save", secondCfg)
	}
	if secondGeneration <= firstGeneration {
		t.Fatalf("updated generation = %d, want greater than %d", secondGeneration, firstGeneration)
	}

	conn := New(Config{ConnectorID: testConnectorID, InstanceID: testInstanceID, ConfigStore: store})
	if _, generation, err := conn.gatewayWithGeneration(); err != nil {
		t.Fatalf("build gateway with generation: %v", err)
	} else if generation != secondGeneration {
		t.Fatalf("gateway generation = %d, want atomic config generation %d", generation, secondGeneration)
	}
}

func TestLocalUIStatesCandidateAndPersistenceSemantics(t *testing.T) {
	for _, phrase := range []string{
		"本地配置",
		"候选配置",
		"已保存配置",
		"运行健康",
		"测试（不保存）",
		"保存并应用",
	} {
		if !strings.Contains(LocalUIIndexHTML, phrase) {
			t.Errorf("local UI is missing key state or action copy %q", phrase)
		}
	}
	for _, id := range []string{"modeBadge", "statusBox", "testButton", "saveButton", "bannerSaveButton"} {
		if !strings.Contains(LocalUIIndexHTML, `id="`+id+`"`) {
			t.Errorf("local UI is missing key element #%s", id)
		}
	}
	for _, contract := range []string{
		`id="dirtyBanner"`,
		`id="bannerSaveButton" disabled`,
		`type="submit" form="configForm"`,
		`id="discardButton"`,
	} {
		if !strings.Contains(LocalUIIndexHTML, contract) {
			t.Errorf("local UI is missing top save action contract %q", contract)
		}
	}
}
func TestLocalUIInteractionResetContracts(t *testing.T) {
	for _, phrase := range []string{
		`var currentTest = state.candidateTest && state.candidateTest.fingerprint === currentFingerprint`,
		`var clearOnlyChange = clearRequested && candidateFingerprintWithoutClears() === state.savedFingerprint`,
		`var canSave = dirty && (clearOnlyChange || currentTest && state.candidateTest.ok)`,
		`saveButton.disabled = saveDisabled`,
		`bannerSaveButton.disabled = saveDisabled`,
		`bannerSaveButton.title = saveTitle`,
		`var currentTestPassed = state.candidateTest && state.candidateTest.ok && state.candidateTest.fingerprint === fingerprint`,
		`if (!labels.length && !currentTestPassed)`,
		`if (labels.length && !clearOnlyChange)`,
		`测试已通过，尚未保存。`,
		`测试失败，请修正后重试。`,
		`删除凭据无需连接测试，保存时需确认。`,
		`待确认删除凭据`,
		`button.setAttribute("aria-label", "显示凭据")`,
		`button.setAttribute("title", "显示凭据")`,
		`pair[0].disabled = false`,
		`if (toggle) { toggle.disabled = false; }`,
		`resetCredentialControls();`,
	} {
		if !strings.Contains(localUIAppJS, phrase) {
			t.Errorf("local UI is missing interaction reset contract %q", phrase)
		}
	}
}

func TestLocalUISaveRequiresCurrentSuccessfulTest(t *testing.T) {
	for _, required := range []string{
		"Connector 等待网关管理接口响应的最长时间。允许 5–20 秒，默认 15 秒。",
		"请先测试当前配置。",
		"测试通过，可保存。",
		"测试通过后才能保存并应用。",
		"请先取消删除，测试并保存其他更改。",
	} {
		if !strings.Contains(LocalUIIndexHTML+localUIAppJS, required) {
			t.Errorf("local UI is missing save gate or timeout guidance %q", required)
		}
	}
}

func TestLocalUIDoesNotExposeRetiredQualityProbe(t *testing.T) {
	for _, retired := range []string{
		"qualityProbeEnabled", "qualityProbeMaxPerHour", "qualityProbeMinInterval", "quality_probe",
		"cpaUsageStatisticsEnabled", "cpa_usage_statistics_enabled", "CPA usage queue",
	} {
		if strings.Contains(LocalUIIndexHTML+localUIAppJS, retired) {
			t.Fatalf("local UI still exposes retired observation marker %q", retired)
		}
	}
}

func postConfigJSON(t *testing.T, handler http.Handler, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode config payload: %v", err)
	}
	rec := postLocalAPI(handler, "config", string(raw))
	if rec.Code != http.StatusOK {
		t.Fatalf("save config: %d %s", rec.Code, rec.Body.String())
	}
}

func loadLocalConfig(t *testing.T, store *LocalConfigStore) GatewayLocalConfig {
	t.Helper()
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return loaded
}

func assertQualityProbeSettings(t *testing.T, got, want LocalQualityProbeSettings) {
	t.Helper()
	if got != want {
		t.Fatalf("quality probe settings = %+v, want %+v", got, want)
	}
}

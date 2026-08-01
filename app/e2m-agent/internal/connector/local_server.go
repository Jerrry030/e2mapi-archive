package connector

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"e2m.local/contracts"
)

const maxLocalJSONRequestBytes int64 = 64 << 10

const localUISessionCookiePrefix = "e2m_local_ui_"

type LocalDiagnosticTarget string
type LocalDiagnosticStatus string

const (
	LocalDiagnosticCoreSync    LocalDiagnosticTarget = "core_sync"
	LocalDiagnosticCoreTest    LocalDiagnosticTarget = "core_test"
	LocalDiagnosticGatewayTask LocalDiagnosticTarget = "gateway_request"
	LocalDiagnosticGatewayTest LocalDiagnosticTarget = "gateway_test"

	LocalDiagnosticOK      LocalDiagnosticStatus = "ok"
	LocalDiagnosticError   LocalDiagnosticStatus = "error"
	LocalDiagnosticUnknown LocalDiagnosticStatus = "unknown"
)

type LocalDiagnosticResult struct {
	Status        LocalDiagnosticStatus `json:"status"`
	CheckedAt     *time.Time            `json:"checked_at,omitempty"`
	LastSuccessAt *time.Time            `json:"last_success_at,omitempty"`
	LatencyMS     int64                 `json:"latency_ms,omitempty"`
	HTTPStatus    int                   `json:"http_status,omitempty"`
	ErrorCode     string                `json:"error_code,omitempty"`
	FailureCount  int                   `json:"failure_count"`
	NextRetryAt   *time.Time            `json:"next_retry_at,omitempty"`
}

type LocalDiagnosticsSnapshot struct {
	CoreSync       LocalDiagnosticResult `json:"core_sync"`
	CoreTest       LocalDiagnosticResult `json:"core_test"`
	GatewayRequest LocalDiagnosticResult `json:"gateway_request"`
	GatewayTest    LocalDiagnosticResult `json:"gateway_test"`
}

type LocalDiagnostics struct {
	mu      sync.RWMutex
	results map[LocalDiagnosticTarget]LocalDiagnosticResult
}

func NewLocalDiagnostics() *LocalDiagnostics {
	return &LocalDiagnostics{results: make(map[LocalDiagnosticTarget]LocalDiagnosticResult)}
}

// Record accepts only bounded, non-sensitive fields. Callers map raw errors to
// stable codes before crossing this boundary.
func (d *LocalDiagnostics) Record(target LocalDiagnosticTarget, status LocalDiagnosticStatus, checkedAt time.Time, latency time.Duration, httpStatus int, errorCode string) {
	if d == nil || !validDiagnosticTarget(target) || (status != LocalDiagnosticOK && status != LocalDiagnosticError) {
		return
	}
	result := LocalDiagnosticResult{
		Status:     status,
		CheckedAt:  timePointer(checkedAt.UTC()),
		LatencyMS:  maxInt64(0, latency.Milliseconds()),
		HTTPStatus: sanitizeHTTPStatus(httpStatus),
		ErrorCode:  sanitizeDiagnosticCode(errorCode),
	}
	d.mu.Lock()
	previous := d.results[target]
	result.LastSuccessAt = previous.LastSuccessAt
	if status == LocalDiagnosticOK {
		result.LastSuccessAt = result.CheckedAt
		result.FailureCount = 0
		result.ErrorCode = ""
	} else {
		result.FailureCount = previous.FailureCount + 1
	}
	d.results[target] = result
	d.mu.Unlock()
}

func (d *LocalDiagnostics) RecordRetry(target LocalDiagnosticTarget, checkedAt time.Time, failureCount int, nextRetryAt time.Time, errorCode string) {
	if d == nil || !validDiagnosticTarget(target) {
		return
	}
	d.mu.Lock()
	result := d.results[target]
	result.Status = LocalDiagnosticError
	result.CheckedAt = timePointer(checkedAt.UTC())
	result.FailureCount = maxInt(0, failureCount)
	result.NextRetryAt = timePointer(nextRetryAt.UTC())
	result.ErrorCode = sanitizeDiagnosticCode(errorCode)
	d.results[target] = result
	d.mu.Unlock()
}

func (d *LocalDiagnostics) Clear(target LocalDiagnosticTarget) {
	if d == nil || !validDiagnosticTarget(target) {
		return
	}
	d.mu.Lock()
	delete(d.results, target)
	d.mu.Unlock()
}

func (d *LocalDiagnostics) Snapshot() LocalDiagnosticsSnapshot {
	unknown := func() LocalDiagnosticResult { return LocalDiagnosticResult{Status: LocalDiagnosticUnknown} }
	snapshot := LocalDiagnosticsSnapshot{
		CoreSync: unknown(), CoreTest: unknown(), GatewayRequest: unknown(), GatewayTest: unknown(),
	}
	if d == nil {
		return snapshot
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	copyResult := func(target LocalDiagnosticTarget) LocalDiagnosticResult {
		if result, ok := d.results[target]; ok {
			return result
		}
		return unknown()
	}
	snapshot.CoreSync = copyResult(LocalDiagnosticCoreSync)
	snapshot.CoreTest = copyResult(LocalDiagnosticCoreTest)
	snapshot.GatewayRequest = copyResult(LocalDiagnosticGatewayTask)
	snapshot.GatewayTest = copyResult(LocalDiagnosticGatewayTest)
	return snapshot
}

type LocalAPIConfig struct {
	Store             *LocalConfigStore
	Token             string
	Default           GatewayLocalConfig
	Client            *http.Client
	CoreURL           string
	Diagnostics       *LocalDiagnostics
	AllowPrivatePeers bool
}

type gatewayLocalConfigInput struct {
	GatewayKind      string                     `json:"gateway_kind"`
	GatewayURL       string                     `json:"gateway_url"`
	Auth             GatewayAuth                `json:"auth"`
	Credentials      GatewayLocalCredentials    `json:"credentials"`
	Runtime          *localRuntimeSettingsInput `json:"runtime,omitempty"`
	ClearCredentials gatewayCredentialClear     `json:"clear_credentials,omitempty"`
}

// localRuntimeSettingsInput is a patch DTO. The local UI intentionally omits
// runtime settings it does not expose, so omitted fields must retain their
// persisted values rather than being normalized from zero values.
type localRuntimeSettingsInput struct {
	GatewayRequestTimeoutSeconds *int           `json:"gateway_request_timeout_seconds,omitempty"`
	LogLevel                     *LocalLogLevel `json:"log_level,omitempty"`
}

type gatewayCredentialClear struct {
	XAPIKey      bool `json:"x_api_key,omitempty"`
	NewAPIUserID bool `json:"newapi_user_id,omitempty"`
	NewAPIToken  bool `json:"newapi_token,omitempty"`
	BearerToken  bool `json:"bearer_token,omitempty"`
}

func NewLocalAPIHandler(cfg LocalAPIConfig) http.Handler {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	if strings.TrimSpace(cfg.CoreURL) == "" {
		cfg.CoreURL = strings.TrimSpace(os.Getenv("E2M_CORE_URL"))
	}
	diagnostics := cfg.Diagnostics
	if diagnostics == nil && cfg.Store != nil {
		diagnostics = cfg.Store.Diagnostics()
	}
	if diagnostics == nil {
		diagnostics = NewLocalDiagnostics()
	}
	mux := http.NewServeMux()
	mux.Handle("/", NewLocalUIHandler(cfg.Token))
	mux.HandleFunc("/api/local/connector/config", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeLocalAPI(w, r, cfg.Token) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			local, ok := loadOrDefault(cfg.Store, cfg.Default)
			if !ok {
				public := cfg.Default.Public()
				public.ContainerMode = cfg.AllowPrivatePeers
				writeLocalJSON(w, http.StatusOK, public)
				return
			}
			public := local.Public()
			public.ContainerMode = cfg.AllowPrivatePeers
			writeLocalJSON(w, http.StatusOK, public)
		case http.MethodPost:
			if cfg.Store == nil {
				writeLocalError(w, http.StatusInternalServerError, "local config store is not configured")
				return
			}
			input, ok := decodeGatewayLocalConfigInput(w, r)
			if !ok {
				return
			}
			base, _ := loadOrDefault(cfg.Store, cfg.Default)
			candidate := mergeGatewayLocalConfig(input, base)
			if err := validateGatewayCredentialTransition(base, candidate); err != nil {
				writeLocalError(w, http.StatusBadRequest, err.Error())
				return
			}
			if err := saveGatewayLocalConfigInput(cfg.Store, candidate, input.ClearCredentials); err != nil {
				writeLocalError(w, http.StatusBadRequest, err.Error())
				return
			}
			saved, err := cfg.Store.Load()
			if err != nil {
				writeLocalError(w, http.StatusInternalServerError, "saved config could not be reloaded")
				return
			}
			public := saved.Public()
			public.ContainerMode = cfg.AllowPrivatePeers
			writeLocalJSON(w, http.StatusOK, map[string]any{
				"message": "Local connector configuration saved.",
				"config":  public,
			})
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			writeLocalError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
	mux.HandleFunc("/api/local/connector/test", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeLocalAPI(w, r, cfg.Token) {
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeLocalError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		input, ok := decodeGatewayLocalConfigInput(w, r)
		if !ok {
			return
		}
		base, _ := loadOrDefault(cfg.Store, cfg.Default)
		candidate := mergeGatewayLocalConfig(input, base)
		if err := ValidateGatewayLocalConfig(candidate); err != nil {
			writeLocalError(w, http.StatusBadRequest, err.Error())
			return
		}
		startedAt := time.Now()
		status, err := TestGatewayLocalConfig(r.Context(), client, candidate)
		if err != nil {
			errorCode := classifyGatewayTestError(status, err)
			diagnostics.Record(LocalDiagnosticGatewayTest, LocalDiagnosticError, time.Now(), time.Since(startedAt), status, errorCode)
			writeLocalCodedError(w, http.StatusBadGateway, "gateway connection test failed", errorCode)
			return
		}
		diagnostics.Record(LocalDiagnosticGatewayTest, LocalDiagnosticOK, time.Now(), time.Since(startedAt), status, "")
		writeLocalJSON(w, http.StatusOK, map[string]any{
			"message": "Gateway connection test passed.",
			"status":  status,
		})
	})
	mux.HandleFunc("/api/local/connector/core-test", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeLocalAPI(w, r, cfg.Token) {
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeLocalError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		startedAt := time.Now()
		status, err := testCoreHealth(r.Context(), client, cfg.CoreURL)
		if err != nil {
			diagnostics.Record(LocalDiagnosticCoreTest, LocalDiagnosticError, time.Now(), time.Since(startedAt), status, classifyConnectionError(err))
			writeLocalError(w, http.StatusBadGateway, "Core connection test failed")
			return
		}
		diagnostics.Record(LocalDiagnosticCoreTest, LocalDiagnosticOK, time.Now(), time.Since(startedAt), status, "")
		writeLocalJSON(w, http.StatusOK, map[string]any{
			"message": "Core connection test passed.",
			"status":  status,
		})
	})
	mux.HandleFunc("/api/local/connector/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if !authorizeLocalAPI(w, r, cfg.Token) {
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeLocalError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeLocalJSON(w, http.StatusOK, diagnostics.Snapshot())
	})
	return loopbackGuard(mux, cfg.AllowPrivatePeers)
}

func decodeGatewayLocalConfigInput(w http.ResponseWriter, r *http.Request) (gatewayLocalConfigInput, bool) {
	var input *gatewayLocalConfigInput
	if !decodeLocalJSON(w, r, &input) {
		return gatewayLocalConfigInput{}, false
	}
	if input == nil {
		writeLocalError(w, http.StatusBadRequest, "request body must be a JSON object")
		return gatewayLocalConfigInput{}, false
	}
	return *input, true
}

func decodeLocalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeLocalError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	if r.ContentLength > maxLocalJSONRequestBytes {
		writeLocalError(w, http.StatusRequestEntityTooLarge, "JSON request body exceeds 64 KiB")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxLocalJSONRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeLocalJSONDecodeError(w, err)
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			writeLocalJSONDecodeError(w, err)
		} else {
			writeLocalError(w, http.StatusBadRequest, "request body must contain a single JSON value")
		}
		return false
	}
	return true
}

func writeLocalJSONDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeLocalError(w, http.StatusRequestEntityTooLarge, "JSON request body exceeds 64 KiB")
		return
	}
	if errors.Is(err, io.EOF) {
		writeLocalError(w, http.StatusBadRequest, "request body must contain a JSON object")
		return
	}
	writeLocalError(w, http.StatusBadRequest, "invalid JSON request: "+err.Error())
}

func mergeGatewayLocalConfig(input gatewayLocalConfigInput, base GatewayLocalConfig) GatewayLocalConfig {
	base.Normalize()
	candidate := base
	if strings.TrimSpace(input.GatewayKind) != "" {
		candidate.GatewayKind = input.GatewayKind
	}
	if strings.TrimSpace(input.GatewayURL) != "" {
		candidate.GatewayURL = input.GatewayURL
	}
	if strings.TrimSpace(string(input.Auth)) != "" {
		candidate.Auth = input.Auth
	}
	if input.Runtime != nil {
		mergeLocalRuntimeSettings(&candidate.Runtime, *input.Runtime)
	}
	candidate.Normalize()
	if !sameGatewayCredentialScope(base, candidate) {
		candidate.Credentials = GatewayLocalCredentials{}
	}
	// Credentials are scoped to the selected authentication scheme. Hidden
	// fields from another auth panel must never be accepted or retained, even
	// when a legacy config already contains them.
	candidate.Credentials = credentialsForGatewayAuth(candidate.Auth, candidate.Credentials)
	mergeCredential := func(submitted string, current *string, clear bool) {
		if strings.TrimSpace(submitted) != "" {
			*current = submitted
		}
		if clear {
			*current = ""
		}
	}
	switch candidate.Auth {
	case GatewayAuthXAPIKey:
		mergeCredential(input.Credentials.XAPIKey, &candidate.Credentials.XAPIKey, input.ClearCredentials.XAPIKey)
	case GatewayAuthNewAPI:
		mergeCredential(input.Credentials.NewAPIUserID, &candidate.Credentials.NewAPIUserID, input.ClearCredentials.NewAPIUserID)
		mergeCredential(input.Credentials.NewAPIToken, &candidate.Credentials.NewAPIToken, input.ClearCredentials.NewAPIToken)
	case GatewayAuthBearer:
		mergeCredential(input.Credentials.BearerToken, &candidate.Credentials.BearerToken, input.ClearCredentials.BearerToken)
	}
	candidate.UpdatedAt = time.Time{}
	candidate.Normalize()
	return candidate
}

func mergeLocalRuntimeSettings(current *LocalRuntimeSettings, patch localRuntimeSettingsInput) {
	if current == nil {
		return
	}
	current.Normalize()
	if patch.GatewayRequestTimeoutSeconds != nil {
		current.GatewayRequestTimeoutSeconds = *patch.GatewayRequestTimeoutSeconds
	}
	if patch.LogLevel != nil {
		current.LogLevel = *patch.LogLevel
	}
}

func credentialsForGatewayAuth(auth GatewayAuth, credentials GatewayLocalCredentials) GatewayLocalCredentials {
	switch auth {
	case GatewayAuthXAPIKey:
		return GatewayLocalCredentials{XAPIKey: credentials.XAPIKey}
	case GatewayAuthNewAPI:
		return GatewayLocalCredentials{
			NewAPIUserID: credentials.NewAPIUserID,
			NewAPIToken:  credentials.NewAPIToken,
		}
	case GatewayAuthBearer:
		return GatewayLocalCredentials{BearerToken: credentials.BearerToken}
	default:
		return GatewayLocalCredentials{}
	}
}

func testCoreHealth(ctx context.Context, client *http.Client, coreURL string) (int, error) {
	coreURL = strings.TrimRight(strings.TrimSpace(coreURL), "/")
	parsed, err := url.Parse(coreURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return 0, errors.New("core_url_invalid")
	}
	if client == nil {
		client = http.DefaultClient
	}
	probeClient := *client
	probeClient.Timeout = DefaultGatewayRequestTimeoutSeconds * time.Second
	probeClient.Jar = nil
	probeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coreURL+"/healthz", nil)
	if err != nil {
		return 0, errors.New("core_request_invalid")
	}
	resp, err := probeClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return resp.StatusCode, errors.New("core_http_error")
	}
	return resp.StatusCode, nil
}

func classifyConnectionError(err error) string {
	if err == nil {
		return ""
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "invalid"), strings.Contains(message, "required"), strings.Contains(message, "unsupported"):
		return "configuration_invalid"
	case strings.Contains(message, "http"), strings.Contains(message, "http_error"):
		return "http_error"
	default:
		return "connection_failed"
	}
}

func classifyGatewayTestError(status int, err error) string {
	switch {
	case status >= http.StatusMultipleChoices && status < http.StatusBadRequest:
		return "gateway_redirect_rejected"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "gateway_auth_failed"
	case status == http.StatusNotFound:
		return "gateway_resource_not_found"
	case status == http.StatusTooManyRequests:
		return "gateway_rejected"
	case status >= http.StatusInternalServerError:
		return "gateway_unavailable"
	}
	var netErr net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return "gateway_timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case err != nil:
		return "gateway_unreachable"
	default:
		return "gateway_test_failed"
	}
}

func validDiagnosticTarget(target LocalDiagnosticTarget) bool {
	switch target {
	case LocalDiagnosticCoreSync, LocalDiagnosticCoreTest, LocalDiagnosticGatewayTask, LocalDiagnosticGatewayTest:
		return true
	default:
		return false
	}
}

func sanitizeDiagnosticCode(code string) string {
	normalized := strings.ToLower(strings.TrimSpace(code))
	switch normalized {
	case "timeout", "canceled", "configuration_invalid", "http_error", "connection_failed",
		"authentication_failed", "task_failed", "rate_limited", "core_sync_failed":
		return normalized
	default:
		if normalized == "" {
			return ""
		}
		if contracts.IsConnectorReportedErrorCode(normalized) {
			return normalized
		}
		return "operation_failed"
	}
}

func sanitizeHTTPStatus(status int) int {
	if status < 100 || status > 599 {
		return 0
	}
	return status
}

func timePointer(value time.Time) *time.Time { return &value }

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func validateGatewayCredentialTransition(base, candidate GatewayLocalConfig) error {
	if sameGatewayCredentialScope(base, candidate) {
		return nil
	}
	return ValidateGatewayLocalConfig(candidate)
}

func sameGatewayCredentialScope(left, right GatewayLocalConfig) bool {
	left.Normalize()
	right.Normalize()
	if left.GatewayKind != right.GatewayKind || left.Auth != right.Auth {
		return false
	}
	leftOrigin, leftOK := normalizedGatewayOrigin(left.GatewayURL)
	rightOrigin, rightOK := normalizedGatewayOrigin(right.GatewayURL)
	return leftOK && rightOK && leftOrigin == rightOrigin
}

func normalizedGatewayOrigin(rawURL string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Opaque != "" || parsed.Scheme == "" || parsed.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	port := parsed.Port()
	if port == "" {
		port = defaultPort(scheme)
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", false
		}
		port = strconv.Itoa(n)
	}
	return scheme + "://" + net.JoinHostPort(host, port), true
}

func saveGatewayLocalConfigInput(store *LocalConfigStore, candidate GatewayLocalConfig, clears gatewayCredentialClear) error {
	missing := missingGatewayCredentials(candidate)
	for _, name := range missing {
		if !clears.includes(name) {
			return ValidateGatewayLocalConfig(candidate)
		}
	}
	if len(missing) != 0 {
		return store.saveAllowingClearedCredentials(candidate)
	}
	return store.Save(candidate)
}

func (clear gatewayCredentialClear) includes(name string) bool {
	switch name {
	case "x_api_key":
		return clear.XAPIKey
	case "newapi_user_id":
		return clear.NewAPIUserID
	case "newapi_token":
		return clear.NewAPIToken
	case "bearer_token":
		return clear.BearerToken
	default:
		return false
	}
}

func LocalRuntimeState(store *LocalConfigStore, defaultCfg GatewayLocalConfig) contracts.ConnectorRuntimeState {
	cfg, ok := loadOrDefault(store, defaultCfg)
	state := contracts.ConnectorRuntimeState{
		ProtocolVersion: contracts.ConnectorProtocolVersion,
		GatewayKind:     cfg.GatewayKind,
		Capabilities: []contracts.ConnectorTaskType{
			contracts.ConnectorTaskGatewayHealth,
			contracts.ConnectorTaskGatewayAccountsList,
			contracts.ConnectorTaskGatewaySchedulableSet,
			contracts.ConnectorTaskGatewaySwitch,
			contracts.ConnectorTaskGatewaySchedulingBarrier,
		},
	}
	if !ok {
		state.GatewayStatus = "missing"
		return state
	}
	state.GatewayConfigured = cfg.Configured()
	if state.GatewayConfigured {
		state.GatewayStatus = "configured"
	} else {
		state.GatewayStatus = "missing"
		state.ErrorCode = "gateway_not_configured"
	}
	return state
}

func loadOrDefault(store *LocalConfigStore, fallback GatewayLocalConfig) (GatewayLocalConfig, bool) {
	if store != nil {
		if cfg, err := store.Load(); err == nil {
			return cfg, true
		}
	}
	fallback.Normalize()
	return fallback, false
}

func authorizeLocalAPI(w http.ResponseWriter, r *http.Request, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		writeLocalError(w, http.StatusUnauthorized, "local UI token is not configured")
		return false
	}
	values := r.Header.Values("X-E2M-Local-Token")
	if len(values) != 0 {
		if len(values) == 1 && subtle.ConstantTimeCompare([]byte(values[0]), []byte(token)) == 1 {
			return true
		}
		writeLocalError(w, http.StatusUnauthorized, "local configuration API requires a valid local session")
		return false
	}
	cookieName, cookieValue, ok := localUISessionCredentials(r, token)
	if !ok {
		writeLocalError(w, http.StatusUnauthorized, "local configuration API requires a valid local session")
		return false
	}
	var matching []*http.Cookie
	for _, cookie := range r.Cookies() {
		if cookie.Name == cookieName {
			matching = append(matching, cookie)
		}
	}
	if len(matching) == 1 && subtle.ConstantTimeCompare([]byte(matching[0].Value), []byte(cookieValue)) == 1 {
		return true
	}
	writeLocalError(w, http.StatusUnauthorized, "local configuration API requires a valid local session")
	return false
}

func setLocalUISessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodGet {
		return
	}
	name, value, ok := localUISessionCredentials(r, token)
	if !ok {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/api/local/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	})
}

func localUISessionCredentials(r *http.Request, token string) (string, string, bool) {
	if r == nil {
		return "", "", false
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	authority, ok := parseLocalAuthority(r.Host, defaultPort(scheme))
	if !ok {
		return "", "", false
	}
	origin := scheme + "://" + authority
	nameDigest := sha256.Sum256([]byte(origin))
	name := localUISessionCookiePrefix + hex.EncodeToString(nameDigest[:6])
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("e2m-local-ui-cookie-v1:" + origin))
	return name, hex.EncodeToString(mac.Sum(nil)), true
}

func loopbackGuard(next http.Handler, allowPrivatePeers bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		withSecurityHeaders(w)
		if !allowedLocalPeer(r.RemoteAddr, allowPrivatePeers) {
			writeLocalError(w, http.StatusForbidden, "local UI only accepts loopback clients")
			return
		}
		if !allowedLocalHost(r.Host) {
			writeLocalError(w, http.StatusForbidden, "local UI only accepts loopback hosts")
			return
		}
		isAPI := strings.HasPrefix(r.URL.Path, "/api/")
		if isAPI {
			switch strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site"))) {
			case "", "same-origin", "none":
			default:
				writeLocalError(w, http.StatusForbidden, "cross-site local API requests are not allowed")
				return
			}
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameLocalOrigin(r, origin) {
			writeLocalError(w, http.StatusForbidden, "local UI origin is not allowed")
			return
		}
		if isAPI && isUnsafeHTTPMethod(r.Method) && r.Header.Get("Origin") == "" {
			writeLocalError(w, http.StatusForbidden, "local API writes require a same-origin Origin header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedLocalPeer(remoteAddr string, allowPrivate bool) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || allowPrivate && ip.IsPrivate())
}

func isUnsafeHTTPMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func sameLocalOrigin(r *http.Request, origin string) bool {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	expectedScheme := "http"
	if r.TLS != nil {
		expectedScheme = "https"
	}
	if parsed.Scheme != expectedScheme {
		return false
	}
	originAuthority, ok := parseLocalAuthority(parsed.Host, defaultPort(parsed.Scheme))
	if !ok {
		return false
	}
	requestAuthority, ok := parseLocalAuthority(r.Host, defaultPort(expectedScheme))
	return ok && originAuthority == requestAuthority
}

func allowedLocalOrigin(origin string) bool {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	_, ok := parseLocalAuthority(parsed.Host, defaultPort(parsed.Scheme))
	return ok
}

func allowedLocalHost(host string) bool {
	_, ok := parseLocalAuthority(host, "")
	return ok
}

func parseLocalAuthority(authority, fallbackPort string) (string, bool) {
	authority = strings.TrimSpace(authority)
	if authority == "" || strings.ContainsAny(authority, "/\\?#@") {
		return "", false
	}
	host := authority
	port := ""
	if strings.HasPrefix(authority, "[") {
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return "", false
		}
		host = authority[1:end]
		rest := authority[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, ":") || len(rest) == 1 {
				return "", false
			}
			port = rest[1:]
		}
	} else if strings.Count(authority, ":") == 1 {
		host, port, _ = strings.Cut(authority, ":")
		if port == "" {
			return "", false
		}
	} else if strings.Count(authority, ":") > 1 {
		host = authority
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		host = "localhost"
	} else {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", false
		}
		host = ip.String()
	}
	if port == "" {
		port = fallbackPort
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", false
		}
	}
	return net.JoinHostPort(host, port), true
}

func defaultPort(scheme string) string {
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func withSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self'")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-Permitted-Cross-Domain-Policies", "none")
}

func writeLocalJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeLocalError(w http.ResponseWriter, status int, message string) {
	writeLocalJSON(w, status, map[string]string{"error": message})
}

func writeLocalCodedError(w http.ResponseWriter, status int, message, errorCode string) {
	writeLocalJSON(w, status, map[string]string{
		"error":      message,
		"error_code": sanitizeDiagnosticCode(errorCode),
	})
}

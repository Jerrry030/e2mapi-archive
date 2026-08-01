package connector

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type GatewayAuth string

type LocalLogLevel string

const (
	DefaultGatewayRequestTimeoutSeconds = 15
	MinGatewayRequestTimeoutSeconds     = 5
	MaxGatewayRequestTimeoutSeconds     = 20
	DefaultQualityProbeMaxPerHour       = 12
	DefaultQualityProbeMinInterval      = 60
	MinQualityProbeMaxPerHour           = 1
	MaxQualityProbeMaxPerHour           = 360
	MinQualityProbeIntervalSeconds      = 10
	MaxQualityProbeIntervalSeconds      = 3600
)

const (
	GatewayAuthXAPIKey GatewayAuth = "x-api-key"
	GatewayAuthNewAPI  GatewayAuth = "newapi"
	GatewayAuthBearer  GatewayAuth = "bearer"

	LocalLogLevelError LocalLogLevel = "error"
	LocalLogLevelInfo  LocalLogLevel = "info"
	LocalLogLevelDebug LocalLogLevel = "debug"
)

type LocalRuntimeSettings struct {
	GatewayRequestTimeoutSeconds int                       `json:"gateway_request_timeout_seconds"`
	LogLevel                     LocalLogLevel             `json:"log_level"`
	QualityProbe                 LocalQualityProbeSettings `json:"quality_probe"`
	// CPAUsageStatisticsEnabled is an explicit local opt-in to CPA's
	// destructive usage-queue reader. It must stay false unless the operator has
	// enabled usage-statistics in CPA itself and accepts the queue semantics.
	CPAUsageStatisticsEnabled bool `json:"cpa_usage_statistics_enabled,omitempty"`
}

type LocalQualityProbeSettings struct {
	Enabled            bool `json:"enabled"`
	MaxRequestsPerHour int  `json:"max_requests_per_hour"`
	MinIntervalSeconds int  `json:"min_interval_seconds"`
}

type GatewayLocalConfig struct {
	GatewayKind string                  `json:"gateway_kind"`
	GatewayURL  string                  `json:"gateway_url"`
	Auth        GatewayAuth             `json:"auth"`
	Credentials GatewayLocalCredentials `json:"credentials,omitempty"`
	Runtime     LocalRuntimeSettings    `json:"runtime"`
	UpdatedAt   time.Time               `json:"updated_at,omitempty"`
}

type GatewayLocalCredentials struct {
	XAPIKey      string `json:"x_api_key,omitempty"`
	NewAPIUserID string `json:"newapi_user_id,omitempty"`
	NewAPIToken  string `json:"newapi_token,omitempty"`
	BearerToken  string `json:"bearer_token,omitempty"`
}

type GatewayLocalConfigPublic struct {
	GatewayKind          string                     `json:"gateway_kind"`
	GatewayURL           string                     `json:"gateway_url"`
	Auth                 GatewayAuth                `json:"auth"`
	ContainerMode        bool                       `json:"container_mode,omitempty"`
	CredentialConfigured map[string]bool            `json:"credential_configured"`
	GatewayConfigured    bool                       `json:"gateway_configured"`
	Runtime              LocalRuntimeSettingsPublic `json:"runtime"`
	UpdatedAt            time.Time                  `json:"updated_at,omitempty"`
}

// LocalRuntimeSettingsPublic is the active configuration surface. Retired
// observation fields remain readable in LocalRuntimeSettings solely so an
// existing on-disk config can be loaded without a destructive migration.
type LocalRuntimeSettingsPublic struct {
	GatewayRequestTimeoutSeconds int           `json:"gateway_request_timeout_seconds"`
	LogLevel                     LocalLogLevel `json:"log_level"`
}

type LocalConfigStore struct {
	mu                sync.RWMutex
	path              string
	diagnostics       *LocalDiagnostics
	gatewayGeneration uint64
	onRuntime         func(LocalRuntimeSettings)
}

func (s *LocalConfigStore) BindingResolver() *LocalBindingStore {
	if s == nil {
		return nil
	}
	path := filepath.Join(filepath.Dir(s.path), "gateway-bindings.json")
	return &LocalBindingStore{path: path, mu: localBindingStoreLock(path)}
}

func (s *LocalConfigStore) DataDir() string {
	if s == nil {
		return ""
	}
	return filepath.Dir(s.path)
}

// SetRuntimeApply installs a process-local callback for settings that can take
// effect without restarting Connector. It is never persisted or serialized.
func (s *LocalConfigStore) SetRuntimeApply(apply func(LocalRuntimeSettings)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onRuntime = apply
	s.mu.Unlock()
}

func NewLocalConfigStore(dataDir string) *LocalConfigStore {
	return &LocalConfigStore{
		path:        filepath.Join(dataDir, "gateway-config.json"),
		diagnostics: NewLocalDiagnostics(),
	}
}

// Diagnostics exposes a process-local, typed recorder. It deliberately
// accepts no raw errors, request data, or response data.
func (s *LocalConfigStore) Diagnostics() *LocalDiagnostics {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.diagnostics == nil {
		s.diagnostics = NewLocalDiagnostics()
	}
	return s.diagnostics
}

func (s *LocalConfigStore) Load() (GatewayLocalConfig, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadLocked()
}

func (s *LocalConfigStore) loadWithGatewayGeneration() (GatewayLocalConfig, uint64, error) {
	if s == nil {
		return GatewayLocalConfig{}, 0, os.ErrNotExist
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg, err := s.loadLocked()
	return cfg, s.gatewayGeneration, err
}

func (s *LocalConfigStore) loadLocked() (GatewayLocalConfig, error) {
	raw, err := readRegularFileNoSymlink(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return GatewayLocalConfig{}, os.ErrNotExist
		}
		return GatewayLocalConfig{}, err
	}
	var cfg GatewayLocalConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return GatewayLocalConfig{}, err
	}
	cfg.Normalize()
	return cfg, nil
}

func (s *LocalConfigStore) Ensure(defaultCfg GatewayLocalConfig) (GatewayLocalConfig, error) {
	if cfg, err := s.Load(); err == nil {
		return cfg, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return GatewayLocalConfig{}, err
	}
	defaultCfg.Normalize()
	if !defaultCfg.Configured() {
		return defaultCfg, nil
	}
	if err := s.Save(defaultCfg); err != nil {
		return GatewayLocalConfig{}, err
	}
	return defaultCfg, nil
}

func (s *LocalConfigStore) Save(cfg GatewayLocalConfig) error {
	cfg.Normalize()
	if err := ValidateGatewayLocalConfig(cfg); err != nil {
		return err
	}
	return s.save(cfg)
}

// saveAllowingClearedCredentials is used only for an explicit credential-clear
// request. It still validates every non-secret part of the configuration.
func (s *LocalConfigStore) saveAllowingClearedCredentials(cfg GatewayLocalConfig) error {
	cfg.Normalize()
	if err := validateGatewayLocalConfigBase(cfg); err != nil {
		return err
	}
	return s.save(cfg)
}

func (s *LocalConfigStore) save(cfg GatewayLocalConfig) error {
	cfg.UpdatedAt = time.Now().UTC()
	s.mu.Lock()
	previous, previousErr := s.loadLocked()
	err := s.writeLocked(cfg)
	if err == nil && (previousErr != nil || !sameGatewayRuntimeSemantics(previous, cfg)) {
		s.gatewayGeneration++
		if s.diagnostics != nil {
			s.diagnostics.Clear(LocalDiagnosticGatewayTask)
		}
	}
	apply := s.onRuntime
	s.mu.Unlock()
	if err == nil && apply != nil {
		apply(cfg.Runtime)
	}
	return err
}

func (s *LocalConfigStore) gatewayGenerationSnapshot() uint64 {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gatewayGeneration
}

func (s *LocalConfigStore) recordGatewayRequest(generation uint64, status LocalDiagnosticStatus, checkedAt time.Time, latency time.Duration, errorCode string) {
	if s == nil {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if generation != s.gatewayGeneration || s.diagnostics == nil {
		return
	}
	s.diagnostics.Record(LocalDiagnosticGatewayTask, status, checkedAt, latency, 0, errorCode)
}

func sameGatewayRuntimeSemantics(left, right GatewayLocalConfig) bool {
	left.Normalize()
	right.Normalize()
	return left.GatewayKind == right.GatewayKind &&
		left.GatewayURL == right.GatewayURL &&
		left.Auth == right.Auth &&
		left.Credentials == right.Credentials &&
		left.Runtime.GatewayRequestTimeoutSeconds == right.Runtime.GatewayRequestTimeoutSeconds
}

func (s *LocalConfigStore) writeLocked(cfg GatewayLocalConfig) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWritePrivateFile(s.path, append(raw, '\n'))
}

func (cfg *GatewayLocalConfig) Normalize() {
	cfg.GatewayKind = strings.ToLower(strings.TrimSpace(cfg.GatewayKind))
	if cfg.GatewayKind == "new-api" {
		cfg.GatewayKind = "newapi"
	}
	cfg.GatewayURL = strings.TrimRight(strings.TrimSpace(cfg.GatewayURL), "/")
	cfg.Auth = GatewayAuth(strings.ToLower(strings.TrimSpace(string(cfg.Auth))))
	if cfg.Auth == "" {
		cfg.Auth = defaultGatewayAuth(cfg.GatewayKind)
	}
	cfg.Credentials.XAPIKey = strings.TrimSpace(cfg.Credentials.XAPIKey)
	cfg.Credentials.NewAPIUserID = strings.TrimSpace(cfg.Credentials.NewAPIUserID)
	cfg.Credentials.NewAPIToken = strings.TrimSpace(cfg.Credentials.NewAPIToken)
	cfg.Credentials.BearerToken = strings.TrimSpace(cfg.Credentials.BearerToken)
	if cfg.GatewayKind == "" {
		cfg.GatewayKind = "sub2api"
	}
	cfg.Runtime.Normalize()
}

func (settings *LocalRuntimeSettings) Normalize() {
	if settings.GatewayRequestTimeoutSeconds == 0 {
		settings.GatewayRequestTimeoutSeconds = DefaultGatewayRequestTimeoutSeconds
	}
	settings.LogLevel = LocalLogLevel(strings.ToLower(strings.TrimSpace(string(settings.LogLevel))))
	if settings.LogLevel == "" {
		settings.LogLevel = LocalLogLevelInfo
	}
	if settings.QualityProbe.MaxRequestsPerHour == 0 {
		settings.QualityProbe.MaxRequestsPerHour = DefaultQualityProbeMaxPerHour
	}
	if settings.QualityProbe.MinIntervalSeconds == 0 {
		settings.QualityProbe.MinIntervalSeconds = DefaultQualityProbeMinInterval
	}
}

func ValidateLocalRuntimeSettings(settings LocalRuntimeSettings) error {
	settings.Normalize()
	if settings.GatewayRequestTimeoutSeconds < MinGatewayRequestTimeoutSeconds ||
		settings.GatewayRequestTimeoutSeconds > MaxGatewayRequestTimeoutSeconds {
		return fmt.Errorf("gateway request timeout must be between %d and %d seconds", MinGatewayRequestTimeoutSeconds, MaxGatewayRequestTimeoutSeconds)
	}
	switch settings.LogLevel {
	case LocalLogLevelError, LocalLogLevelInfo, LocalLogLevelDebug:
		if settings.QualityProbe.MaxRequestsPerHour < MinQualityProbeMaxPerHour ||
			settings.QualityProbe.MaxRequestsPerHour > MaxQualityProbeMaxPerHour {
			return fmt.Errorf("quality probe hourly budget must be between %d and %d", MinQualityProbeMaxPerHour, MaxQualityProbeMaxPerHour)
		}
		if settings.QualityProbe.MinIntervalSeconds < MinQualityProbeIntervalSeconds ||
			settings.QualityProbe.MinIntervalSeconds > MaxQualityProbeIntervalSeconds {
			return fmt.Errorf("quality probe minimum interval must be between %d and %d seconds", MinQualityProbeIntervalSeconds, MaxQualityProbeIntervalSeconds)
		}
		return nil
	default:
		return errors.New("log level must be error, info, or debug")
	}
}

func (cfg GatewayLocalConfig) Public() GatewayLocalConfigPublic {
	cfg.Normalize()
	publicURL := cfg.GatewayURL
	if err := validateGatewayLocalConfigBase(cfg); err != nil {
		publicURL = ""
	}
	return GatewayLocalConfigPublic{
		GatewayKind:       cfg.GatewayKind,
		GatewayURL:        publicURL,
		Auth:              cfg.Auth,
		GatewayConfigured: cfg.Configured(),
		Runtime: LocalRuntimeSettingsPublic{
			GatewayRequestTimeoutSeconds: cfg.Runtime.GatewayRequestTimeoutSeconds,
			LogLevel:                     cfg.Runtime.LogLevel,
		},
		CredentialConfigured: map[string]bool{
			"x_api_key":      cfg.Credentials.XAPIKey != "",
			"newapi_user_id": cfg.Credentials.NewAPIUserID != "",
			"newapi_token":   cfg.Credentials.NewAPIToken != "",
			"bearer_token":   cfg.Credentials.BearerToken != "",
		},
		UpdatedAt: cfg.UpdatedAt,
	}
}

func (cfg GatewayLocalConfig) Configured() bool {
	return ValidateGatewayLocalConfig(cfg) == nil
}

func ValidateGatewayLocalConfig(cfg GatewayLocalConfig) error {
	cfg.Normalize()
	if err := ValidateLocalRuntimeSettings(cfg.Runtime); err != nil {
		return err
	}
	if err := validateGatewayLocalConfigBase(cfg); err != nil {
		return err
	}
	missing := missingGatewayCredentials(cfg)
	if len(missing) == 0 {
		return nil
	}
	switch cfg.Auth {
	case GatewayAuthXAPIKey:
		return errors.New("x-api-key credential is required")
	case GatewayAuthNewAPI:
		return errors.New("new-api user id and token are required")
	case GatewayAuthBearer:
		return errors.New("bearer token is required")
	default:
		return errors.New("gateway credential is required")
	}
}

func validateGatewayLocalConfigBase(cfg GatewayLocalConfig) error {
	cfg.Normalize()
	if err := ValidateLocalRuntimeSettings(cfg.Runtime); err != nil {
		return err
	}
	if cfg.GatewayURL == "" {
		return errors.New("gateway url is required")
	}
	parsed, err := url.Parse(cfg.GatewayURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Opaque != "" {
		return errors.New("gateway url must be a valid http or https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("gateway url must use http or https")
	}
	if parsed.User != nil {
		return errors.New("gateway url must not contain user info")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("gateway url must not contain a query")
	}
	if parsed.Fragment != "" || strings.Contains(cfg.GatewayURL, "#") {
		return errors.New("gateway url must not contain a fragment")
	}
	expectedAuth := defaultGatewayAuth(cfg.GatewayKind)
	switch cfg.GatewayKind {
	case "sub2api", "newapi", "cpa":
	default:
		return errors.New("unsupported gateway kind")
	}
	if cfg.Auth != expectedAuth {
		return errors.New("gateway authentication does not match gateway kind")
	}
	return nil
}

func missingGatewayCredentials(cfg GatewayLocalConfig) []string {
	cfg.Normalize()
	switch cfg.Auth {
	case GatewayAuthXAPIKey:
		if cfg.Credentials.XAPIKey == "" {
			return []string{"x_api_key"}
		}
	case GatewayAuthNewAPI:
		var missing []string
		if cfg.Credentials.NewAPIUserID == "" {
			missing = append(missing, "newapi_user_id")
		}
		if cfg.Credentials.NewAPIToken == "" {
			missing = append(missing, "newapi_token")
		}
		return missing
	case GatewayAuthBearer:
		if cfg.Credentials.BearerToken == "" {
			return []string{"bearer_token"}
		}
	}
	return nil
}

func TestGatewayLocalConfig(ctx context.Context, client *http.Client, cfg GatewayLocalConfig) (int, error) {
	cfg.Normalize()
	if err := ValidateGatewayLocalConfig(cfg); err != nil {
		return 0, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	noRedirectClient := *client
	noRedirectClient.Timeout = time.Duration(cfg.Runtime.GatewayRequestTimeoutSeconds) * time.Second
	noRedirectClient.Jar = nil
	noRedirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.GatewayURL+gatewayTestPath(cfg), nil)
	if err != nil {
		return 0, err
	}
	if err := attachGatewayAuth(req, cfg, cfg.Auth); err != nil {
		return 0, err
	}
	resp, err := noRedirectClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("gateway test returned HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

func EnsureLocalUIToken(path string) (string, error) {
	raw, err := readRegularFileNoSymlink(path)
	if err == nil {
		token := strings.TrimSpace(string(raw))
		if token != "" {
			if err := atomicWritePrivateFile(path, []byte(token+"\n")); err != nil {
				return "", err
			}
			return token, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := "e2m_local_" + hex.EncodeToString(tokenBytes)
	if err := atomicWritePrivateFile(path, []byte(token+"\n")); err != nil {
		return "", err
	}
	return token, nil
}

func readRegularFileNoSymlink(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("refusing to follow symbolic link %q", path)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("local config path %q is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) {
		return nil, fmt.Errorf("local config path %q changed while opening", path)
	}
	return io.ReadAll(f)
}

func atomicWritePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	if err := rejectSymlinkTarget(path); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	renamed := false
	defer func() {
		_ = temp.Close()
		if !renamed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0600); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := rejectSymlinkTarget(path); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	renamed = true
	return syncDirectory(dir)
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("local config directory %q must be a real directory", dir)
	}
	return os.Chmod(dir, 0700)
}

func rejectSymlinkTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to replace symbolic link %q", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("local config path %q is not a regular file", path)
	}
	return nil
}

func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func defaultGatewayAuth(kind string) GatewayAuth {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "newapi":
		return GatewayAuthNewAPI
	case "cpa":
		return GatewayAuthBearer
	default:
		return GatewayAuthXAPIKey
	}
}

func gatewayTestPath(cfg GatewayLocalConfig) string {
	switch strings.ToLower(strings.TrimSpace(cfg.GatewayKind)) {
	case "newapi":
		return "/api/channel/?p=0&page_size=1"
	case "cpa":
		return "/v0/management/auth-files"
	case "sub2api", "":
		return "/api/v1/admin/accounts"
	default:
		return "/"
	}
}

func attachGatewayAuth(req *http.Request, cfg GatewayLocalConfig, style GatewayAuth) error {
	if style == "" {
		style = cfg.Auth
	}
	switch style {
	case "", GatewayAuthXAPIKey:
		if cfg.Credentials.XAPIKey == "" {
			return errors.New("x-api-key credential is not configured")
		}
		req.Header.Set("x-api-key", cfg.Credentials.XAPIKey)
	case GatewayAuthNewAPI:
		if cfg.Credentials.NewAPIUserID == "" || cfg.Credentials.NewAPIToken == "" {
			return errors.New("new-api credential is not configured")
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Credentials.NewAPIToken)
		req.Header.Set("New-Api-User", cfg.Credentials.NewAPIUserID)
	case GatewayAuthBearer:
		if cfg.Credentials.BearerToken == "" {
			return errors.New("bearer credential is not configured")
		}
		req.Header.Set("Authorization", "Bearer "+cfg.Credentials.BearerToken)
	default:
		return fmt.Errorf("unsupported auth style %q", style)
	}
	return nil
}

package gateways

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"e2m.local/contracts"
)

const maxResponseBytes int64 = 2 << 20

type Config struct {
	BaseURL      string
	XAPIKey      string
	NewAPIUserID string
	NewAPIToken  string
	BearerToken  string
	HTTPClient   *http.Client
	// BindingResolver resolves opaque Connector-local IDs. It must never return
	// values to Core; lifecycle adapters use it only while constructing the
	// local gateway request.
	BindingResolver   BindingResolver
	QualityProbeGuard func() error
	// CPAUsageStatisticsEnabled is a local-only acknowledgement that the CPA
	// instance has usage-statistics enabled and its destructive queue may be
	// consumed by this Connector.
	CPAUsageStatisticsEnabled bool
}

type BindingResolver interface {
	ResolveBinding(context.Context, string) (string, error)
}

type Gateway interface {
	Health(context.Context) (contracts.ConnectorGatewayHealthResult, error)
	ListAccounts(context.Context) ([]contracts.GatewayAccount, error)
	ProbeQuality(context.Context, contracts.ConnectorGatewayQualityProbeInput) (contracts.ConnectorGatewayQualityProbeResult, error)
	SetSchedulable(context.Context, string, bool) error
}

// WeightedTrafficGateway is optional. Only adapters with a verified native
// integer traffic-share write implement it; boolean scheduling is deliberately
// kept separate so staged 10/25/50/100 rollouts cannot collapse to on/off.
type WeightedTrafficGateway interface {
	SetTrafficShare(context.Context, string, int) error
}

// AccountLifecycleGateway is optional so diagnostic/test gateways and older
// adapter builds fail closed without pretending to execute lifecycle work.
type AccountLifecycleGateway interface {
	ProvisionAccount(context.Context, contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error)
	UpdateAccount(context.Context, contracts.GatewayAccountSpec) (contracts.GatewayProvisionResult, error)
	DeleteAccount(context.Context, string) error
}

func ResolveBinding(ctx context.Context, resolver BindingResolver, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || resolver == nil {
		return "", &Error{Code: "binding_not_found", Message: "connector-local binding is not configured"}
	}
	value, err := resolver.ResolveBinding(ctx, id)
	if err != nil || strings.TrimSpace(value) == "" {
		return "", &Error{Code: "binding_not_found", Message: "connector-local binding could not be resolved"}
	}
	return value, nil
}

// PassiveObservationPage is an adapter-neutral page of real downstream
// requests. Cursor is opaque to Connector and must advance only across returned
// (or deliberately ineligible) gateway rows so a restart can resume safely.
type PassiveObservationPage struct {
	Observations []contracts.ConnectorChannelObservation
	NextCursor   string
}

// PassiveObservationReader is optional. Gateways without a request-fact API do
// not implement it; Connector reports that absence explicitly in runtime state.
type PassiveObservationReader interface {
	ObservationCapabilities() contracts.ConnectorObservationCapabilities
	ReadPassiveObservations(context.Context, string, int) (PassiveObservationPage, error)
}

type Error struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *Error) Error() string { return e.Message }

func TaskError(err error) contracts.ConnectorTaskError {
	var gatewayErr *Error
	if errors.As(err, &gatewayErr) {
		code := gatewayErr.Code
		if !contracts.IsConnectorReportedErrorCode(code) {
			code = "connector_task_failed"
		}
		return contracts.ConnectorTaskError{
			Code:      code,
			Retryable: gatewayErr.Retryable,
		}
	}
	return contracts.ConnectorTaskError{
		Code:      "gateway_request_failed",
		Retryable: true,
	}
}

type AuthStyle int

const (
	AuthXAPIKey AuthStyle = iota
	AuthNewAPI
	AuthBearer
)

type Transport struct {
	cfg    Config
	auth   AuthStyle
	client *http.Client
}

// DoStream sends an authenticated request while leaving the response body to
// the caller. It is intentionally separate from Do: quality probes must measure
// the first real SSE event, which cannot be recovered after buffering the body.
func (t *Transport) DoStream(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, &Error{Code: "invalid_gateway_request", Message: "gateway request could not be encoded"}
		}
		requestBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.cfg.BaseURL+path, requestBody)
	if err != nil {
		return nil, &Error{Code: "invalid_gateway_request", Message: "gateway request could not be created"}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch t.auth {
	case AuthXAPIKey:
		req.Header.Set("x-api-key", t.cfg.XAPIKey)
	case AuthNewAPI:
		req.Header.Set("Authorization", "Bearer "+t.cfg.NewAPIToken)
		req.Header.Set("New-Api-User", t.cfg.NewAPIUserID)
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+t.cfg.BearerToken)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &Error{Code: "gateway_timeout", Message: "gateway request timed out", Retryable: true}
		}
		return nil, &Error{Code: "gateway_unreachable", Message: "gateway could not be reached", Retryable: true}
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		_ = resp.Body.Close()
		return nil, &Error{Code: "gateway_redirect_rejected", Message: "gateway redirect was rejected"}
	}
	return resp, nil
}

func NewTransport(cfg Config, auth AuthStyle) *Transport {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	} else {
		clone := *client
		client = &clone
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	return &Transport{cfg: cfg, auth: auth, client: client}
}

func (t *Transport) Do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, &Error{Code: "invalid_gateway_request", Message: "gateway request could not be encoded"}
		}
		requestBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.cfg.BaseURL+path, requestBody)
	if err != nil {
		return 0, nil, &Error{Code: "invalid_gateway_request", Message: "gateway request could not be created"}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch t.auth {
	case AuthXAPIKey:
		req.Header.Set("x-api-key", t.cfg.XAPIKey)
	case AuthNewAPI:
		req.Header.Set("Authorization", "Bearer "+t.cfg.NewAPIToken)
		req.Header.Set("New-Api-User", t.cfg.NewAPIUserID)
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+t.cfg.BearerToken)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, nil, &Error{Code: "gateway_unreachable", Message: "gateway could not be reached", Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return 0, nil, &Error{Code: "gateway_redirect_rejected", Message: "gateway redirect was rejected"}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, nil, &Error{Code: "gateway_response_invalid", Message: "gateway response could not be read", Retryable: true}
	}
	if int64(len(raw)) > maxResponseBytes {
		return 0, nil, &Error{Code: "gateway_response_too_large", Message: "gateway response exceeded the size limit"}
	}
	return resp.StatusCode, raw, nil
}

func HTTPStatusError(status int) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Code: "gateway_auth_failed", Message: "gateway authentication failed"}
	case status == http.StatusNotFound:
		return &Error{Code: "gateway_resource_not_found", Message: "gateway resource was not found"}
	case status >= 500:
		return &Error{Code: "gateway_unavailable", Message: "gateway is temporarily unavailable", Retryable: true}
	default:
		return &Error{Code: "gateway_rejected", Message: "gateway rejected the request"}
	}
}

func InvalidResponse() error {
	return &Error{Code: "gateway_response_invalid", Message: "gateway returned an invalid response"}
}

func QualityProbeUnsupported() error {
	return &Error{Code: "task_type_unsupported", Message: "gateway quality probing is not supported by this adapter"}
}

func HealthOK() contracts.ConnectorGatewayHealthResult {
	return contracts.ConnectorGatewayHealthResult{Status: "ok", CheckedAt: time.Now().UTC()}
}

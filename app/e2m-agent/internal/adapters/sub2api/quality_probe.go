package sub2api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

const maxProbeEventBytes = 64 << 10

type probeEvent struct {
	Type    string `json:"type"`
	Success bool   `json:"success,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (g *Gateway) ProbeQuality(ctx context.Context, input contracts.ConnectorGatewayQualityProbeInput) (contracts.ConnectorGatewayQualityProbeResult, error) {
	if g.probeGuard == nil {
		return contracts.ConnectorGatewayQualityProbeResult{}, &gateways.Error{Code: "quality_probe_disabled", Message: "quality probes require explicit local opt-in"}
	}
	if input.Capability != contracts.QualityProbeTextStream || !contracts.IsQualityProbeEndpointPath(input.EndpointPath) {
		return contracts.ConnectorGatewayQualityProbeResult{}, &gateways.Error{Code: "invalid_gateway_request", Message: "quality probe capability or endpoint path is unsupported"}
	}
	accountID := strings.TrimSpace(input.AccountID)
	if _, err := url.PathUnescape(accountID); err != nil || accountID == "" {
		return contracts.ConnectorGatewayQualityProbeResult{}, &gateways.Error{Code: "invalid_account_id", Message: "account_id is invalid"}
	}
	account, err := g.getAccount(ctx, accountID)
	if err != nil {
		return contracts.ConnectorGatewayQualityProbeResult{}, err
	}
	actualPath, ok := sub2APIProbeEndpoint(account)
	if !ok || actualPath != input.EndpointPath {
		return contracts.ConnectorGatewayQualityProbeResult{}, &gateways.Error{
			Code: "quality_probe_scope_unsupported", Message: "account does not use the requested upstream endpoint",
		}
	}
	if err := g.probeGuard(); err != nil {
		return contracts.ConnectorGatewayQualityProbeResult{}, err
	}
	startedAt := time.Now()
	resp, err := g.http.DoStream(ctx, http.MethodPost,
		typedAdminBase+"/accounts/"+url.PathEscape(accountID)+"/test",
		map[string]string{"model_id": strings.TrimSpace(input.Model)})
	if err != nil {
		return contracts.ConnectorGatewayQualityProbeResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result := failedProbeResult(resp.StatusCode, classifyProbeHTTPStatus(resp.StatusCode), startedAt)
		result.Capability, result.EndpointPath = input.Capability, input.EndpointPath
		return result, nil
	}

	result := contracts.ConnectorGatewayQualityProbeResult{
		Status: http.StatusOK, Capability: input.Capability,
		EndpointPath: input.EndpointPath, ObservedAt: startedAt.UTC(),
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxProbeEventBytes+1))
	buf := make([]byte, 0, 4096)
	scanner.Buffer(buf, maxProbeEventBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event probeEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
			return contracts.ConnectorGatewayQualityProbeResult{}, gateways.InvalidResponse()
		}
		now := time.Now()
		if result.FirstTokenMS == 0 && event.Type == "content" {
			result.FirstTokenMS = elapsedMilliseconds(startedAt, now)
		}
		switch event.Type {
		case "error":
			result.Success = false
			result.ErrorType = classifyProbeError(event.Error)
			result.TotalMS = elapsedMilliseconds(startedAt, now)
			return result, nil
		case "test_complete":
			result.Success = event.Success
			result.TotalMS = elapsedMilliseconds(startedAt, now)
			if !result.Success {
				result.ErrorType = contracts.ErrorUnknown
				return result, nil
			}
			if result.FirstTokenMS <= 0 {
				return contracts.ConnectorGatewayQualityProbeResult{}, gateways.InvalidResponse()
			}
			return result, nil
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result := failedProbeResult(0, contracts.ErrorTimeout, startedAt)
			result.Capability, result.EndpointPath = input.Capability, input.EndpointPath
			return result, nil
		}
		return contracts.ConnectorGatewayQualityProbeResult{}, gateways.InvalidResponse()
	}
	return contracts.ConnectorGatewayQualityProbeResult{}, gateways.InvalidResponse()
}

func sub2APIProbeEndpoint(account typedAccount) (string, bool) {
	platform := strings.ToLower(strings.TrimSpace(account.Platform))
	accountType := strings.ToLower(strings.TrimSpace(account.Type))
	switch platform {
	case "anthropic":
		return contracts.QualityProbeEndpointMessages, true
	case "grok":
		return contracts.QualityProbeEndpointResponses, true
	case "openai":
		switch accountType {
		case "oauth", "setup-token", "setup_token":
			return contracts.QualityProbeEndpointResponses, true
		case "apikey", "api-key", "api_key":
			if openAIAPIKeyUsesChatCompletions(account.Extra) {
				return contracts.QualityProbeEndpointChatCompletions, true
			}
			return contracts.QualityProbeEndpointResponses, true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

func openAIAPIKeyUsesChatCompletions(extra map[string]any) bool {
	mode, _ := extra["openai_responses_mode"].(string)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "force_chat_completions":
		return true
	case "force_responses":
		return false
	}
	supported, exists := extra["openai_responses_supported"]
	value, valid := supported.(bool)
	return exists && valid && !value
}

func failedProbeResult(status int, errorType contracts.ObservationErrorType, startedAt time.Time) contracts.ConnectorGatewayQualityProbeResult {
	return contracts.ConnectorGatewayQualityProbeResult{Success: false, Status: status, ErrorType: errorType,
		TotalMS: elapsedMilliseconds(startedAt, time.Now()), ObservedAt: startedAt.UTC()}
}

func elapsedMilliseconds(started, finished time.Time) float64 {
	value := float64(finished.Sub(started).Microseconds()) / 1000
	if value <= 0 {
		return 0.001
	}
	return value
}

func classifyProbeHTTPStatus(status int) contracts.ObservationErrorType {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return contracts.ErrorAuth
	case status == http.StatusPaymentRequired:
		return contracts.ErrorInsufficientBalance
	case status == http.StatusTooManyRequests:
		return contracts.ErrorRateLimit
	case status >= 500:
		return contracts.ErrorServer
	case status >= 400:
		return contracts.ErrorClient
	default:
		return contracts.ErrorUnknown
	}
}

func classifyProbeError(message string) contracts.ObservationErrorType {
	value := strings.ToLower(message)
	switch {
	case strings.Contains(value, "401"), strings.Contains(value, "403"), strings.Contains(value, "unauthorized"), strings.Contains(value, "authentication"):
		return contracts.ErrorAuth
	case strings.Contains(value, "insufficient"), strings.Contains(value, "balance"), strings.Contains(value, "quota exhausted"):
		return contracts.ErrorInsufficientBalance
	case strings.Contains(value, "429"), strings.Contains(value, "rate limit"), strings.Contains(value, "too many"):
		return contracts.ErrorRateLimit
	case strings.Contains(value, "timeout"), strings.Contains(value, "deadline"):
		return contracts.ErrorTimeout
	case strings.Contains(value, "network"), strings.Contains(value, "connection"), strings.Contains(value, "dns"):
		return contracts.ErrorNetwork
	case strings.Contains(value, "500"), strings.Contains(value, "502"), strings.Contains(value, "503"), strings.Contains(value, "504"):
		return contracts.ErrorServer
	default:
		return contracts.ErrorUnknown
	}
}

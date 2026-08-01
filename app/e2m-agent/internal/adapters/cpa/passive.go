package cpa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"e2m.local/agent/internal/adapters/gateways"
	"e2m.local/contracts"
)

const cpaUsageBatchLimit = 500

// cpaUsageRecord mirrors CLIProxyAPI's management usage-queue wire shape. The
// endpoint destructively pops records, so Connector's generic passive pending
// outbox is the durable owner immediately after this method returns.
type cpaUsageRecord struct {
	Timestamp time.Time     `json:"timestamp"`
	LatencyMS int64         `json:"latency_ms"`
	TTFTMS    int64         `json:"ttft_ms"`
	AuthIndex cpaWireScalar `json:"auth_index"`
	Model     string        `json:"model"`
	RequestID string        `json:"request_id"`
	Failed    bool          `json:"failed"`
	Generate  bool          `json:"generate"`
	Endpoint  string        `json:"endpoint"`
	Tokens    struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"tokens"`
	Fail struct {
		StatusCode int    `json:"status_code"`
		Body       string `json:"body"`
	} `json:"fail"`
}

func (g *Gateway) ObservationCapabilities() contracts.ConnectorObservationCapabilities {
	if !g.usageStatisticsEnabled {
		return contracts.ConnectorObservationCapabilities{}
	}
	return contracts.ConnectorObservationCapabilities{
		PassiveCollection: true, SuccessEvents: true, FailureEvents: true,
		ErrorClassification: true, FirstTokenMS: true, TotalMS: true, TokenCounts: true,
	}
}

// ReadPassiveObservations intentionally ignores the supplied cursor: the
// upstream queue itself is the cursor. Connector persists every returned page
// before upload and will not call this method again until Core acknowledges the
// pending page, giving the destructive API at-least-once delivery from the
// Connector boundary onward.
func (g *Gateway) ReadPassiveObservations(ctx context.Context, _ string, limit int) (gateways.PassiveObservationPage, error) {
	if !g.usageStatisticsEnabled {
		return gateways.PassiveObservationPage{}, &gateways.Error{Code: "task_type_unsupported", Message: "CPA usage statistics require explicit local opt-in"}
	}
	if limit <= 0 {
		return gateways.PassiveObservationPage{}, &gateways.Error{Code: "invalid_gateway_request", Message: "observation limit must be positive"}
	}
	if limit > cpaUsageBatchLimit {
		limit = cpaUsageBatchLimit
	}
	// Refresh the stable auth-file identity map before destructively popping the
	// queue. Usage rows refer to auth_index on some CPA versions while Core
	// bindings refer to the auth-file name. Reuse a prior account read so the
	// normal health loop need not make a duplicate management request.
	g.authIndexMu.RLock()
	hasIndex := len(g.authIndexToName) > 0
	g.authIndexMu.RUnlock()
	if !hasIndex {
		if _, err := g.ListAccounts(ctx); err != nil {
			return gateways.PassiveObservationPage{}, err
		}
	}
	status, raw, err := g.http.Do(ctx, http.MethodGet, managementBase+"/usage-queue?count="+strconvItoa(limit), nil)
	if err != nil {
		return gateways.PassiveObservationPage{}, err
	}
	if status < 200 || status >= 300 {
		return gateways.PassiveObservationPage{}, gateways.HTTPStatusError(status)
	}
	var records []cpaUsageRecord
	if err := json.Unmarshal(raw, &records); err != nil || records == nil || len(records) > limit {
		return gateways.PassiveObservationPage{}, gateways.InvalidResponse()
	}
	out := make([]contracts.ConnectorChannelObservation, 0, len(records))
	for _, record := range records {
		observation, ok := cpaUsageObservation(record)
		if ok {
			if name, mapped := g.resolveAuthIndex(observation.RemoteID); mapped {
				observation.RemoteID = name
			}
			out = append(out, observation)
		}
	}
	// A non-empty changing cursor is required by the Connector protocol even
	// though CPA has already advanced its destructive source queue.
	now := time.Now().UTC()
	return gateways.PassiveObservationPage{Observations: out, NextCursor: "queue.v1." + strconvFormatInt(now.UnixNano())}, nil
}

func cpaUsageObservation(record cpaUsageRecord) (contracts.ConnectorChannelObservation, bool) {
	remoteID := strings.TrimSpace(string(record.AuthIndex))
	model := strings.TrimSpace(record.Model)
	if remoteID == "" || model == "" || record.Timestamp.IsZero() {
		return contracts.ConnectorChannelObservation{}, false
	}
	identity := strings.TrimSpace(record.RequestID)
	if identity == "" {
		identity = record.Timestamp.UTC().Format(time.RFC3339Nano) + "\x00" + remoteID + "\x00" + model
	}
	sum := sha256.Sum256([]byte(identity))
	observation := contracts.ConnectorChannelObservation{
		ObservationID: "cpa.usage.v1." + hex.EncodeToString(sum[:12]),
		RemoteID:      remoteID, Model: model, Success: !record.Failed,
		StatusCode: record.Fail.StatusCode, FirstTokenMS: float64(max(record.TTFTMS, 0)), TotalMS: float64(max(record.LatencyMS, 0)),
		InputTokens: max(record.Tokens.InputTokens, 0), OutputTokens: max(record.Tokens.OutputTokens, 0),
		Source: contracts.ObservationPassive, ObservedAt: record.Timestamp.UTC(),
	}
	if observation.Success {
		if observation.StatusCode == 0 {
			observation.StatusCode = http.StatusOK
		}
		return observation, true
	}
	observation.ErrorType = classifyCPAUsageError(record.Fail.StatusCode, record.Fail.Body)
	return observation, true
}

func classifyCPAUsageError(status int, body string) contracts.ObservationErrorType {
	value := strings.ToLower(body)
	switch {
	case status == 499 || strings.Contains(value, "cancel") || strings.Contains(value, "client disconnected"):
		return contracts.ErrorCanceled
	case status == http.StatusUnauthorized || status == http.StatusForbidden || strings.Contains(value, "unauthorized") || strings.Contains(value, "invalid api key"):
		return contracts.ErrorAuth
	case status == http.StatusPaymentRequired || strings.Contains(value, "insufficient") || strings.Contains(value, "quota exhausted"):
		return contracts.ErrorInsufficientBalance
	case status == http.StatusTooManyRequests || strings.Contains(value, "rate limit"):
		return contracts.ErrorRateLimit
	case status == http.StatusGatewayTimeout || strings.Contains(value, "timeout") || strings.Contains(value, "deadline"):
		return contracts.ErrorTimeout
	case status >= 500:
		return contracts.ErrorServer
	case status >= 400 && status < 500:
		return contracts.ErrorClient
	case strings.Contains(value, "network") || strings.Contains(value, "connection") || strings.Contains(value, "dns"):
		return contracts.ErrorNetwork
	default:
		return contracts.ErrorUnknown
	}
}

// Small local integer helpers keep this adapter's queue path dependency-free.
func strconvItoa(value int) string { return strconvFormatInt(int64(value)) }

func strconvFormatInt(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	var magnitude uint64
	if negative {
		magnitude = uint64(-(value + 1)) + 1
	} else {
		magnitude = uint64(value)
	}
	var buffer [20]byte
	index := len(buffer)
	for magnitude > 0 {
		index--
		buffer[index] = byte('0' + magnitude%10)
		magnitude /= 10
	}
	if negative {
		index--
		buffer[index] = '-'
	}
	return string(buffer[index:])
}

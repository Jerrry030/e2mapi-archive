package contracts

import "time"

// ConnectorCostUsage is the only Connector observation shape that may feed
// the upstream financial ledger. Every quantity is nullable so an omitted
// upstream field cannot become an observed zero. GroupKey is nullable for the
// same reason: Core must not infer a billing group from channel configuration.
//
// The legacy top-level input_tokens/output_tokens fields below remain for
// quality aggregation and wire compatibility. They are intentionally not a
// fallback for this structure.
type ConnectorCostUsage struct {
	InputTokens       *int64  `json:"input_tokens,omitempty"`
	OutputTokens      *int64  `json:"output_tokens,omitempty"`
	CachedInputTokens *int64  `json:"cached_input_tokens,omitempty"`
	RequestCount      *int64  `json:"request_count,omitempty"`
	GroupKey          *string `json:"group_key,omitempty"`
}

// ConnectorChannelObservation is the connector wire shape for one measured
// upstream request or active probe. Instance and pool identity are deliberately
// omitted: Core derives both from the authenticated connector and its published
// binding so one downstream cannot submit observations for another.
type ConnectorChannelObservation struct {
	ObservationID string                 `json:"observation_id"`
	ChannelID     string                 `json:"channel_id,omitempty"`
	RemoteID      string                 `json:"remote_id,omitempty"`
	Model         string                 `json:"model"`
	Capability    QualityProbeCapability `json:"capability,omitempty"`
	EndpointPath  string                 `json:"endpoint_path,omitempty"`
	Success       bool                   `json:"success"`
	StatusCode    int                    `json:"status_code,omitempty"`
	ErrorType     ObservationErrorType   `json:"error_type,omitempty"`
	FirstTokenMS  float64                `json:"first_token_ms,omitempty"`
	TotalMS       float64                `json:"total_ms,omitempty"`
	// Deprecated for financial attribution: these scalar fields predate
	// presence-aware usage and cannot distinguish omitted from observed zero.
	// They remain quality-only compatibility inputs.
	InputTokens  int64               `json:"input_tokens,omitempty"`
	OutputTokens int64               `json:"output_tokens,omitempty"`
	CostUsage    *ConnectorCostUsage `json:"cost_usage,omitempty"`
	Source       ObservationSource   `json:"source,omitempty"`
	ObservedAt   time.Time           `json:"observed_at,omitempty"`
}

// ConnectorObservationBatchRequest batches telemetry to keep request-path
// reporting cheap. ConnectorID is checked against the bearer token identity.
type ConnectorObservationBatchRequest struct {
	ConnectorID  string                        `json:"connector_id"`
	Observations []ConnectorChannelObservation `json:"observations"`
}

type ConnectorObservationBatchResponse struct {
	Accepted int `json:"accepted"`
}

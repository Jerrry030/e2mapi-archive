package contracts

import "time"

// This file models the health-metrics layer that feeds health-driven automatic
// upstream switching. It is deliberately split from health.go (the account-level
// health-checker verdict): this layer is about per-channel *service quality*
// aggregated over time windows, not a single checker snapshot.
//
// The flow is: real requests (and, later, active probes) produce
// ChannelObservation rows; an aggregator rolls them up per window into a
// ChannelHealthSnapshot carrying success rate, TTFT/duration percentiles, and a
// set of explainable sub-scores. The strategy engine (Phase 3) and automatic
// switch (Phase 4) consume snapshots, never the raw observations, so the
// decision inputs stay small and auditable.

// ObservationSource records where an observation came from. Passive is a real
// downstream request; probe is a platform-issued synthetic check. Both are kept
// because passive data alone has cold-start/sample-bias gaps and probe data
// alone does not represent real user experience.
type ObservationSource string

const (
	ObservationPassive ObservationSource = "passive"
	ObservationProbe   ObservationSource = "probe"
)

// ObservationErrorType is a coarse classification of a failed observation, used
// to compute timeout/rate-limit rates and to drive hard-gate risk (auth /
// insufficient balance are treated as immediate disqualifiers upstream).
type ObservationErrorType string

const (
	ErrorNone                ObservationErrorType = ""
	ErrorTimeout             ObservationErrorType = "timeout"
	ErrorRateLimit           ObservationErrorType = "rate_limit"
	ErrorAuth                ObservationErrorType = "auth"
	ErrorInsufficientBalance ObservationErrorType = "insufficient_balance"
	ErrorServer              ObservationErrorType = "server_error"
	ErrorClient              ObservationErrorType = "client_error"
	ErrorNetwork             ObservationErrorType = "network"
	ErrorCanceled            ObservationErrorType = "canceled"
	// ErrorPlatform is a downstream-visible failure caused by the local gateway
	// or platform. It remains in factual SLA but must not penalize an upstream.
	ErrorPlatform ObservationErrorType = "platform_error"
	ErrorUnknown  ObservationErrorType = "unknown"
)

// HealthWindow is an aggregation window. The first version lands 1m and 5m; 30m
// and 24h are defined for the stability-trend and cost/quality-review rollups
// that Phase 6 uses.
type HealthWindow string

const (
	Window1m  HealthWindow = "1m"
	Window5m  HealthWindow = "5m"
	Window30m HealthWindow = "30m"
	Window24h HealthWindow = "24h"
)

// Duration maps a window to its time span. Unknown windows return 0 so callers
// can treat them as "no window".
func (w HealthWindow) Duration() time.Duration {
	switch w {
	case Window1m:
		return time.Minute
	case Window5m:
		return 5 * time.Minute
	case Window30m:
		return 30 * time.Minute
	case Window24h:
		return 24 * time.Hour
	default:
		return 0
	}
}

// HealthState is the per-channel runtime state. The metrics aggregator only ever
// emits unknown/healthy/degraded/unhealthy (pure data verdicts); quarantined and
// recovering are lifecycle states owned by the automatic-switch orchestrator
// (Phase 4), which sets them when it drains or re-observes a channel.
type HealthState string

const (
	// HealthUnknown: not enough samples in the window to judge. Distinct from
	// unhealthy so an idle channel is never mistaken for a failing one.
	HealthUnknown     HealthState = "unknown"
	HealthHealthy     HealthState = "healthy"
	HealthDegraded    HealthState = "degraded"
	HealthUnhealthy   HealthState = "unhealthy"
	HealthQuarantined HealthState = "quarantined"
	HealthRecovering  HealthState = "recovering"
)

// ChannelObservation is one measured outcome for a managed channel: either a
// real downstream request (passive) or a synthetic probe. Cost/token fields are
// optional; latency fields are milliseconds. Observations are append-only facts,
// never mutated.
type ChannelObservation struct {
	ID           string                 `json:"id"`
	ChannelID    string                 `json:"channel_id"`
	InstanceID   string                 `json:"instance_id,omitempty"`
	PoolID       string                 `json:"pool_id,omitempty"`
	Model        string                 `json:"model,omitempty"`
	Capability   QualityProbeCapability `json:"capability,omitempty"`
	EndpointPath string                 `json:"endpoint_path,omitempty"`
	// Success is the single source of truth for the success rate; ErrorType only
	// classifies *why* a non-success failed.
	Success       bool                 `json:"success"`
	StatusCode    int                  `json:"status_code,omitempty"`
	ErrorType     ObservationErrorType `json:"error_type,omitempty"`
	FirstTokenMS  float64              `json:"first_token_ms,omitempty"`
	TotalMS       float64              `json:"total_ms,omitempty"`
	InputTokens   int64                `json:"input_tokens,omitempty"`
	OutputTokens  int64                `json:"output_tokens,omitempty"`
	EstimatedCost float64              `json:"estimated_cost,omitempty"`
	Source        ObservationSource    `json:"source,omitempty"`
	ObservedAt    time.Time            `json:"observed_at"`
}

// ChannelHealthSnapshot is the aggregate of a channel's observations over one
// window. Scores are 0..100. Sub-scores are exposed (not just a single number)
// so a switch decision and its notification can explain which dimension moved.
type ChannelHealthSnapshot struct {
	ID           string                 `json:"id"`
	ChannelID    string                 `json:"channel_id"`
	PoolID       string                 `json:"pool_id,omitempty"`
	InstanceID   string                 `json:"instance_id,omitempty"`
	Model        string                 `json:"model,omitempty"`
	Capability   QualityProbeCapability `json:"capability,omitempty"`
	EndpointPath string                 `json:"endpoint_path,omitempty"`
	Window       HealthWindow           `json:"window"`
	// BucketStart identifies the recompute bucket. CreatedAt is the exact time
	// this immutable rolling-window revision was computed inside that bucket;
	// changed recomputations receive a new ID rather than replacing this fact.
	BucketStart time.Time `json:"bucket_start"`
	// SampleCount/SuccessRate/ErrorRate are factual traffic metrics. They include
	// every observation, including downstream client errors and cancellations,
	// so user-facing SLA reports match what the downstream actually experienced.
	SampleCount int `json:"sample_count"`

	SuccessRate float64 `json:"success_rate"`
	TTFTP50     float64 `json:"ttft_p50"`
	TTFTP95     float64 `json:"ttft_p95"`
	DurationP50 float64 `json:"duration_p50"`
	DurationP95 float64 `json:"duration_p95"`
	ErrorRate   float64 `json:"error_rate"`
	// Quality* excludes failures attributable to the downstream (client errors
	// and cancellations). Scheduling confidence, scores and health verdicts use
	// this attribution-aware population rather than the factual SLA population.
	QualitySampleCount int     `json:"quality_sample_count"`
	QualitySuccessRate float64 `json:"quality_success_rate"`
	QualityErrorRate   float64 `json:"quality_error_rate"`
	TimeoutRate        float64 `json:"timeout_rate"`
	RateLimitRate      float64 `json:"rate_limit_rate"`
	// UpstreamErrorRate excludes downstream/client responsibility (client
	// errors and cancellations). Credential failures are counted separately so
	// callers can apply their hard-failure policy without treating them as a
	// provider-wide quality regression.
	UpstreamErrorRate        float64 `json:"upstream_error_rate"`
	UpstreamFailureCount     int     `json:"upstream_failure_count"`
	AuthFailureCount         int     `json:"auth_failure_count"`
	InsufficientBalanceCount int     `json:"insufficient_balance_count"`

	EstimatedCostPer1KTokens float64 `json:"estimated_cost_per_1k_tokens"`

	HealthScore    float64 `json:"health_score"`
	QualityScore   float64 `json:"quality_score"`
	SuccessScore   float64 `json:"success_score"`
	TTFTScore      float64 `json:"ttft_score"`
	DurationScore  float64 `json:"duration_score"`
	StabilityScore float64 `json:"stability_score"`
	CostScore      float64 `json:"cost_score"`
	RiskScore      float64 `json:"risk_score"`

	HealthState HealthState `json:"health_state"`
	CreatedAt   time.Time   `json:"created_at"`
}

// ChannelObservationFilter narrows an observation query. Zero-value fields are
// ignored; Since and Until are inclusive bounds and Limit caps the result.
type ChannelObservationFilter struct {
	ChannelID    string
	InstanceID   string
	PoolID       string
	Model        string
	Capability   QualityProbeCapability
	EndpointPath string
	Source       ObservationSource
	Since        time.Time
	Until        time.Time
	Limit        int
	// ExactScope makes channel/instance/model comparisons exact, including empty
	// values. PoolID remains optional metadata rather than part of identity. It
	// is used by the aggregator after it discovers a scope;
	// without it zero-value fields retain the public wildcard semantics.
	ExactScope bool
}

// ChannelHealthScope is the isolation boundary for one quality signal. A
// channel can be deployed to many downstream instances and can serve models
// with very different latency/error profiles, so neither dimension may be
// omitted when observations are aggregated for a concrete downstream.
type ChannelHealthScope struct {
	ChannelID    string                 `json:"channel_id"`
	InstanceID   string                 `json:"instance_id,omitempty"`
	PoolID       string                 `json:"pool_id,omitempty"`
	Model        string                 `json:"model,omitempty"`
	Capability   QualityProbeCapability `json:"capability,omitempty"`
	EndpointPath string                 `json:"endpoint_path,omitempty"`
}

// ChannelHealthSnapshotFilter narrows a snapshot query. Zero-value fields are
// ignored, so a caller can fetch by channel, by instance, by pool, and/or by
// window.
type ChannelHealthSnapshotFilter struct {
	ChannelID    string
	InstanceID   string
	PoolID       string
	Model        string
	Capability   QualityProbeCapability
	EndpointPath string
	Window       HealthWindow
	BucketStart  time.Time
	Since        time.Time
	Limit        int
	ExactScope   bool
	// IncludeHistory returns every matching bucket. By default stores collapse
	// buckets to the latest row per instance/channel/model/window, preserving
	// the original "current snapshots" behaviour for existing consumers.
	IncludeHistory bool
}

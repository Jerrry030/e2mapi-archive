package contracts

import "time"

// This file models the route-strategy config that drives health-driven upstream
// selection. It is deliberately split from channel_health.go: that file is the
// measured *data* (observations/snapshots), this one is the *policy* (how to
// rank healthy candidates and how cautiously to act on them).
//
// The strategy engine (internal/strategy, Phase 3) reads a RouteStrategy plus a
// set of ChannelHealthSnapshots and produces an explainable ranking. It never
// touches gateways: the automatic-switch orchestrator (Phase 4) turns a ranking
// into a RoutePlan change and runs it through reconcile dry-run/apply/rollback.

// RouteStrategyType selects the weighting profile used to rank candidate
// channels. The first version implements stability_first and cost_first;
// latency_first and balanced are reserved so users/plans can adopt them
// without a contract change (Phase 6 fills in their product wiring).
type RouteStrategyType string

const (
	// StrategyStabilityFirst favours success rate and low volatility: for
	// high-value/production sites that would rather pay more than flap.
	StrategyStabilityFirst RouteStrategyType = "stability_first"
	// StrategyCostFirst prefers the cheapest channel that still clears a quality
	// floor: for cost-sensitive/low-tier sites. "Cheap" never means "unusable".
	StrategyCostFirst RouteStrategyType = "cost_first"
	// StrategyLatencyFirst (reserved) weights first-token latency highest: for
	// chat/realtime/copilot workloads.
	StrategyLatencyFirst RouteStrategyType = "latency_first"
	// StrategyBalanced (reserved) is the general-purpose default for new sites.
	StrategyBalanced RouteStrategyType = "balanced"
)

// Normalize maps free-form/legacy config onto a known type, defaulting to
// stability_first (the safe managed-hosting default).
func (t RouteStrategyType) Normalize() RouteStrategyType {
	switch t {
	case StrategyCostFirst, StrategyLatencyFirst, StrategyBalanced, StrategyStabilityFirst:
		return t
	default:
		return StrategyStabilityFirst
	}
}

// StrategyWeights weights the 0..100 sub-scores of a ChannelHealthSnapshot into
// one strategy score. The weights of a well-formed strategy sum to 1.0 so the
// resulting score stays on the same 0..100 scale as its inputs; the engine
// fills defaults per type when a weight set is left zero.
type StrategyWeights struct {
	Success   float64 `json:"success"`
	TTFT      float64 `json:"ttft"`
	Duration  float64 `json:"duration"`
	Stability float64 `json:"stability"`
	Cost      float64 `json:"cost"`
}

// StrategyThresholds are the hard-gate and quality-floor knobs. Latencies are
// milliseconds. These mirror the aggregator's DefaultThresholds so a channel the
// metrics layer calls unhealthy is also gated out here; the engine fills any
// unset field with the first-version defaults.
type StrategyThresholds struct {
	// MinSamples is the fewest observations a window needs before its snapshot is
	// trusted for numeric gating and for meeting a quality floor. Below it a
	// candidate is treated as low-confidence, never as a confirmed pass.
	MinSamples int `json:"min_samples,omitempty"`
	// TargetSuccessRate is the quality-floor success rate (cost_first must clear
	// it); FloorSuccessRate is the hard disqualifier below which any candidate is
	// gated out.
	TargetSuccessRate float64 `json:"target_success_rate,omitempty"`
	FloorSuccessRate  float64 `json:"floor_success_rate,omitempty"`
	// MaxTTFTP95MS / MaxDurationP95MS are the p95 latency ceilings used both as
	// the hard gate and as the cost_first quality floor.
	MaxTTFTP95MS     float64 `json:"max_ttft_p95_ms,omitempty"`
	MaxDurationP95MS float64 `json:"max_duration_p95_ms,omitempty"`
	// ConsecutiveFailureLimit hard-gates a channel whose live consecutive-failure
	// streak reaches it, even if its window still reads acceptable (flapping).
	ConsecutiveFailureLimit int `json:"consecutive_failure_limit,omitempty"`
	// EjectScore is the lower inclusive bound of the 100-point penalty score.
	// A candidate at or below it is removed from scheduling until recovery.
	// Zero uses the engine default.
	EjectScore float64 `json:"eject_score,omitempty"`
}

// RouteStrategy is the persisted policy a plan/pool/user runs under. Threshold
// and weight overrides are optional; the engine applies type defaults for any
// unset field so a minimally-specified strategy still behaves sensibly.
type RouteStrategy struct {
	ID   string            `json:"id"`
	Name string            `json:"name,omitempty"`
	Type RouteStrategyType `json:"type"`

	Thresholds StrategyThresholds `json:"thresholds"`
	Weights    StrategyWeights    `json:"weights"`

	// AutoApply lets a low-risk decision reconcile automatically; when false the
	// engine's decision is advisory only. ApprovalRequired forces a human gate
	// regardless of risk. Both default conservative (no silent auto-apply).
	AutoApply        bool `json:"auto_apply"`
	ApprovalRequired bool `json:"approval_required"`

	// Anti-flapping guards (seconds): CooldownSeconds is the minimum interval
	// between switches on one instance; RecoveryObservationSeconds is how long a
	// recovered channel must be observed before traffic is switched *back* to it
	// ("switch out fast, switch back slow"); MaxAutoSwitchesPerHour caps
	// automatic switches per instance per hour.
	CooldownSeconds            int `json:"cooldown_seconds,omitempty"`
	RecoveryObservationSeconds int `json:"recovery_observation_seconds,omitempty"`
	MaxAutoSwitchesPerHour     int `json:"max_auto_switches_per_hour,omitempty"`

	// Scope + scope ownership let one strategy be persisted and resolved at a
	// specific level (a plan, a pool, or a user). The auto-switch orchestrator
	// resolves the effective strategy for a plan by precedence
	// plan > pool > user > built-in default (see StrategyScope). These are
	// empty for an in-memory/ad-hoc strategy that is never persisted.
	Scope     StrategyScope `json:"scope,omitempty"`
	PlanID    string        `json:"plan_id,omitempty"`
	PoolID    string        `json:"pool_id,omitempty"`
	UserID    int64         `json:"user_id,omitempty"`
	CreatedAt time.Time     `json:"created_at,omitempty"`
	UpdatedAt time.Time     `json:"updated_at,omitempty"`
}

// StrategyScope is the level a persisted RouteStrategy binds to. The orchestrator
// resolves a plan's effective strategy by precedence: a plan-scoped strategy wins
// over a pool-scoped one, which wins over a user-scoped one, which wins over the
// built-in stability-first default. This supports account-wide defaults while
// still letting a single high-value plan run a stricter policy.
type StrategyScope string

const (
	// StrategyScopePlan binds a strategy to one RoutePlan (highest precedence).
	StrategyScopePlan StrategyScope = "plan"
	// StrategyScopePool binds a strategy to every plan publishing an upstream pool.
	StrategyScopePool StrategyScope = "pool"
	// StrategyScopeUser binds a strategy to every plan owned by a user (the
	// account-wide default, lowest persisted precedence).
	StrategyScopeUser StrategyScope = "user"
)

// Normalize maps free-form scope onto a known value, defaulting to user (the
// broadest, safest scope for a newly-authored strategy).
func (s StrategyScope) Normalize() StrategyScope {
	switch s {
	case StrategyScopePlan, StrategyScopePool, StrategyScopeUser:
		return s
	default:
		return StrategyScopeUser
	}
}

// RouteStrategyFilter narrows a strategy query. Zero-value fields are ignored, so
// a caller can list every strategy for a user, or only the pool/plan-scoped
// ones. It mirrors the other store filters in this package.
type RouteStrategyFilter struct {
	Scope  StrategyScope
	PlanID string
	PoolID string
	UserID int64
}

// Matches reports whether a strategy satisfies the filter (shared by the memory
// store and any caller that filters an in-memory slice).
func (f RouteStrategyFilter) Matches(s RouteStrategy) bool {
	if f.Scope != "" && s.Scope != f.Scope {
		return false
	}
	if f.PlanID != "" && s.PlanID != f.PlanID {
		return false
	}
	if f.PoolID != "" && s.PoolID != f.PoolID {
		return false
	}
	if f.UserID != 0 && s.UserID != f.UserID {
		return false
	}
	return true
}

package contracts

import "time"

// RecommendationExecutionScope is the smallest owner-controlled boundary
// allowed to opt into intelligence-driven mutations. A broad implicit default
// is intentionally not part of the vocabulary.
type RecommendationExecutionScope string

const (
	RecommendationExecutionScopePlan RecommendationExecutionScope = "plan"
	RecommendationExecutionScopePool RecommendationExecutionScope = "pool"
)

func IsRecommendationExecutionScope(value RecommendationExecutionScope) bool {
	return value == RecommendationExecutionScopePlan || value == RecommendationExecutionScopePool
}

// RecommendationExecutionPolicy is independent from health auto-switch policy.
// Enabled defaults false, so a zero value can never authorize execution.
type RecommendationExecutionPolicy struct {
	ID         string                       `json:"id"`
	UserID     int64                        `json:"user_id"`
	Scope      RecommendationExecutionScope `json:"scope"`
	PlanID     string                       `json:"plan_id,omitempty"`
	PoolID     string                       `json:"pool_id,omitempty"`
	Enabled    bool                         `json:"enabled"`
	KillSwitch bool                         `json:"kill_switch"`

	DailyExecutionCap int              `json:"daily_execution_cap"`
	CooldownSeconds   int              `json:"cooldown_seconds"`
	MinimumSavings    CanonicalDecimal `json:"minimum_savings"`
	// Version is the optimistic-concurrency token for the persisted policy.
	// Zero is never a persisted value and is used only for create-only writes.
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RecommendationExecutionContext contains only current, owner-scoped inputs.
// DailyExecutionCount must use the policy owner's current UTC accounting day;
// LastExecutedAt is the latest successful intelligence optimization in scope.
type RecommendationExecutionContext struct {
	UserID              int64            `json:"user_id"`
	PlanID              string           `json:"plan_id"`
	PoolID              string           `json:"pool_id"`
	ExpectedSavings     CanonicalDecimal `json:"expected_savings"`
	DailyExecutionCount int              `json:"daily_execution_count"`
	LastExecutedAt      *time.Time       `json:"last_executed_at,omitempty"`
	Now                 time.Time        `json:"now"`
}

type RecommendationExecutionBlockReason string

const (
	RecommendationExecutionBlockedInvalidPolicy       RecommendationExecutionBlockReason = "invalid_policy"
	RecommendationExecutionBlockedDisabled            RecommendationExecutionBlockReason = "disabled"
	RecommendationExecutionBlockedKillSwitch          RecommendationExecutionBlockReason = "kill_switch"
	RecommendationExecutionBlockedOwnerMismatch       RecommendationExecutionBlockReason = "owner_mismatch"
	RecommendationExecutionBlockedScopeMismatch       RecommendationExecutionBlockReason = "scope_mismatch"
	RecommendationExecutionBlockedDailyCap            RecommendationExecutionBlockReason = "daily_cap_reached"
	RecommendationExecutionBlockedCooldown            RecommendationExecutionBlockReason = "cooldown_active"
	RecommendationExecutionBlockedSavingsUnavailable  RecommendationExecutionBlockReason = "savings_unavailable"
	RecommendationExecutionBlockedSavingsBelowMinimum RecommendationExecutionBlockReason = "savings_below_minimum"
	RecommendationExecutionBlockedInvalidContext      RecommendationExecutionBlockReason = "invalid_context"
)

func IsRecommendationExecutionBlockReason(value RecommendationExecutionBlockReason) bool {
	switch value {
	case RecommendationExecutionBlockedInvalidPolicy, RecommendationExecutionBlockedDisabled,
		RecommendationExecutionBlockedKillSwitch, RecommendationExecutionBlockedOwnerMismatch,
		RecommendationExecutionBlockedScopeMismatch, RecommendationExecutionBlockedDailyCap,
		RecommendationExecutionBlockedCooldown, RecommendationExecutionBlockedSavingsUnavailable,
		RecommendationExecutionBlockedSavingsBelowMinimum, RecommendationExecutionBlockedInvalidContext:
		return true
	default:
		return false
	}
}

type RecommendationExecutionAuthorization struct {
	Allowed bool                                 `json:"allowed"`
	Reasons []RecommendationExecutionBlockReason `json:"reasons"`
}

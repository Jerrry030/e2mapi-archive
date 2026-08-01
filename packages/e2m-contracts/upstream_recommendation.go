package contracts

import "time"

const (
	UpstreamRecommendationFormulaVersionV1  = "upstream-recommendation-savings-v1"
	UpstreamRecommendationStrategyVersionV1 = "upstream-recommendation-policy-v1"
)

type UpstreamRecommendationStatus string

const (
	UpstreamRecommendationOpen           UpstreamRecommendationStatus = "open"
	UpstreamRecommendationShadowing      UpstreamRecommendationStatus = "shadowing"
	UpstreamRecommendationReadyForDryRun UpstreamRecommendationStatus = "ready_for_dry_run"
	UpstreamRecommendationDryRunning     UpstreamRecommendationStatus = "dry_running"
	UpstreamRecommendationDryRunPassed   UpstreamRecommendationStatus = "dry_run_passed"
	UpstreamRecommendationDryRunBlocked  UpstreamRecommendationStatus = "dry_run_blocked"
	UpstreamRecommendationDismissed      UpstreamRecommendationStatus = "dismissed"
	UpstreamRecommendationExpired        UpstreamRecommendationStatus = "expired"
)

func IsUpstreamRecommendationStatus(value UpstreamRecommendationStatus) bool {
	switch value {
	case UpstreamRecommendationOpen, UpstreamRecommendationShadowing,
		UpstreamRecommendationReadyForDryRun, UpstreamRecommendationDryRunning,
		UpstreamRecommendationDryRunPassed, UpstreamRecommendationDryRunBlocked,
		UpstreamRecommendationDismissed, UpstreamRecommendationExpired:
		return true
	default:
		return false
	}
}

type UpstreamRecommendationConstraintKind string

const (
	UpstreamRecommendationConstraintQuality  UpstreamRecommendationConstraintKind = "quality"
	UpstreamRecommendationConstraintCapacity UpstreamRecommendationConstraintKind = "capacity"
	UpstreamRecommendationConstraintBalance  UpstreamRecommendationConstraintKind = "balance"
)

func UpstreamRecommendationRequiredConstraints() []UpstreamRecommendationConstraintKind {
	return []UpstreamRecommendationConstraintKind{
		UpstreamRecommendationConstraintQuality,
		UpstreamRecommendationConstraintCapacity,
		UpstreamRecommendationConstraintBalance,
	}
}

func IsUpstreamRecommendationConstraintKind(value UpstreamRecommendationConstraintKind) bool {
	return value == UpstreamRecommendationConstraintQuality || value == UpstreamRecommendationConstraintCapacity || value == UpstreamRecommendationConstraintBalance
}

type UpstreamRecommendationConstraintStatus string

const (
	UpstreamRecommendationConstraintPassed  UpstreamRecommendationConstraintStatus = "passed"
	UpstreamRecommendationConstraintBlocked UpstreamRecommendationConstraintStatus = "blocked"
	UpstreamRecommendationConstraintUnknown UpstreamRecommendationConstraintStatus = "unknown"
)

func IsUpstreamRecommendationConstraintStatus(value UpstreamRecommendationConstraintStatus) bool {
	return value == UpstreamRecommendationConstraintPassed || value == UpstreamRecommendationConstraintBlocked || value == UpstreamRecommendationConstraintUnknown
}

type UpstreamRecommendationConstraint struct {
	Kind        UpstreamRecommendationConstraintKind   `json:"kind"`
	Status      UpstreamRecommendationConstraintStatus `json:"status"`
	ReasonCode  string                                 `json:"reason_code,omitempty"`
	EvidenceIDs []string                               `json:"evidence_ids"`
	// Explanation is presentation-only and excluded from the fingerprint.
	Explanation string `json:"explanation,omitempty"`
}

type UpstreamRecommendationCostRange struct {
	Lower    CanonicalDecimal `json:"lower"`
	Expected CanonicalDecimal `json:"expected"`
	Upper    CanonicalDecimal `json:"upper"`
}

// UpstreamRecommendationSavingsRange is an interval over the same currency,
// price dimension and unit as the recommendation. Percent values are ratios.
type UpstreamRecommendationSavingsRange struct {
	AmountLower     CanonicalDecimal `json:"amount_lower"`
	AmountExpected  CanonicalDecimal `json:"amount_expected"`
	AmountUpper     CanonicalDecimal `json:"amount_upper"`
	PercentLower    CanonicalDecimal `json:"percent_lower"`
	PercentExpected CanonicalDecimal `json:"percent_expected"`
	PercentUpper    CanonicalDecimal `json:"percent_upper"`
}

// UpstreamRecommendation pins every fact and mapping needed to reproduce an
// advisory result. A changed intelligence/cost version, plan generation,
// mapping identity, or evidence set makes the old object stale.
type UpstreamRecommendation struct {
	ID     string                       `json:"id"`
	UserID int64                        `json:"user_id"`
	Status UpstreamRecommendationStatus `json:"status"`

	IntelligenceFactVersion int64 `json:"intelligence_fact_version"`
	CostLedgerFactVersion   int64 `json:"cost_ledger_fact_version"`
	LinkFactVersion         int64 `json:"link_fact_version"`
	PlanGeneration          int64 `json:"plan_generation"`

	FromSourceID  string `json:"from_source_id"`
	FromChannelID string `json:"from_channel_id"`
	FromGroupKey  string `json:"from_group_key"`
	ToSourceID    string `json:"to_source_id"`
	ToChannelID   string `json:"to_channel_id"`
	ToGroupKey    string `json:"to_group_key"`
	ModelKey      string `json:"model_key"`

	PriceDimension      UpstreamPriceDimension `json:"price_dimension"`
	SettlementCurrency  string                 `json:"settlement_currency"`
	PerTokens           int64                  `json:"per_tokens"`
	AffectedPlanIDs     []string               `json:"affected_plan_ids"`
	AffectedDownstreams []string               `json:"affected_downstreams"`

	EvidenceIDs     []string                           `json:"evidence_ids"`
	Constraints     []UpstreamRecommendationConstraint `json:"constraints"`
	FromCost        UpstreamRecommendationCostRange    `json:"from_cost"`
	ToCost          UpstreamRecommendationCostRange    `json:"to_cost"`
	Savings         UpstreamRecommendationSavingsRange `json:"savings"`
	FormulaVersion  string                             `json:"formula_version"`
	StrategyVersion string                             `json:"strategy_version"`
	Fingerprint     string                             `json:"fingerprint"`

	DryRunID  string    `json:"dry_run_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// UpstreamRecommendationFilter is an owner-scoped persistence query. An empty
// Status selects every lifecycle state; Limit is normalized by the store.
type UpstreamRecommendationFilter struct {
	UserID int64
	Status UpstreamRecommendationStatus
	Limit  int
}

// UpstreamRecommendationGenerationReason is intentionally allowlisted. It is
// safe to persist, audit and return to the console; raw upstream/database
// errors, URLs and credentials must never be copied into it.
type UpstreamRecommendationGenerationReason string

const (
	UpstreamRecommendationGenerationNoCurrentFacts      UpstreamRecommendationGenerationReason = "no_current_facts"
	UpstreamRecommendationGenerationNoPublishedPlan     UpstreamRecommendationGenerationReason = "no_published_plan"
	UpstreamRecommendationGenerationNoCallablePair      UpstreamRecommendationGenerationReason = "no_callable_pair"
	UpstreamRecommendationGenerationMissingLink         UpstreamRecommendationGenerationReason = "missing_verified_link"
	UpstreamRecommendationGenerationMissingCost         UpstreamRecommendationGenerationReason = "missing_exact_cost"
	UpstreamRecommendationGenerationIncomparableCost    UpstreamRecommendationGenerationReason = "incomparable_cost"
	UpstreamRecommendationGenerationStalePrice          UpstreamRecommendationGenerationReason = "stale_price"
	UpstreamRecommendationGenerationInsufficientQuality UpstreamRecommendationGenerationReason = "insufficient_quality"
	UpstreamRecommendationGenerationInsufficientBalance UpstreamRecommendationGenerationReason = "insufficient_balance"
	UpstreamRecommendationGenerationNoProvenSavings     UpstreamRecommendationGenerationReason = "no_proven_savings"
)

func IsUpstreamRecommendationGenerationReason(value UpstreamRecommendationGenerationReason) bool {
	switch value {
	case UpstreamRecommendationGenerationNoCurrentFacts,
		UpstreamRecommendationGenerationNoPublishedPlan,
		UpstreamRecommendationGenerationNoCallablePair,
		UpstreamRecommendationGenerationMissingLink,
		UpstreamRecommendationGenerationMissingCost,
		UpstreamRecommendationGenerationIncomparableCost,
		UpstreamRecommendationGenerationStalePrice,
		UpstreamRecommendationGenerationInsufficientQuality,
		UpstreamRecommendationGenerationInsufficientBalance,
		UpstreamRecommendationGenerationNoProvenSavings:
		return true
	default:
		return false
	}
}

type UpstreamRecommendationGenerationDiagnostic struct {
	Reason UpstreamRecommendationGenerationReason `json:"reason"`
	Count  int                                    `json:"count"`
}

type UpstreamRecommendationGenerationResult struct {
	Recommendations []UpstreamRecommendation                     `json:"recommendations"`
	Blocked         []UpstreamRecommendationGenerationDiagnostic `json:"blocked"`
}

type UpstreamRecommendationCandidate struct {
	UserID int64 `json:"user_id"`

	IntelligenceFactVersion int64 `json:"intelligence_fact_version"`
	CostLedgerFactVersion   int64 `json:"cost_ledger_fact_version"`
	LinkFactVersion         int64 `json:"link_fact_version"`
	PlanGeneration          int64 `json:"plan_generation"`

	FromSourceID  string `json:"from_source_id"`
	FromChannelID string `json:"from_channel_id"`
	FromGroupKey  string `json:"from_group_key"`
	ToSourceID    string `json:"to_source_id"`
	ToChannelID   string `json:"to_channel_id"`
	ToGroupKey    string `json:"to_group_key"`
	ModelKey      string `json:"model_key"`

	PriceDimension      UpstreamPriceDimension `json:"price_dimension"`
	SettlementCurrency  string                 `json:"settlement_currency"`
	PerTokens           int64                  `json:"per_tokens"`
	AffectedPlanIDs     []string               `json:"affected_plan_ids"`
	AffectedDownstreams []string               `json:"affected_downstreams"`

	EvidenceIDs     []string                           `json:"evidence_ids"`
	Constraints     []UpstreamRecommendationConstraint `json:"constraints"`
	FromCost        UpstreamRecommendationCostRange    `json:"from_cost"`
	ToCost          UpstreamRecommendationCostRange    `json:"to_cost"`
	FormulaVersion  string                             `json:"formula_version"`
	StrategyVersion string                             `json:"strategy_version"`
	CreatedAt       time.Time                          `json:"created_at"`
	ExpiresAt       time.Time                          `json:"expires_at"`
}

type UpstreamRecommendationCurrentFacts struct {
	UserID                  int64                  `json:"user_id"`
	IntelligenceFactVersion int64                  `json:"intelligence_fact_version"`
	CostLedgerFactVersion   int64                  `json:"cost_ledger_fact_version"`
	LinkFactVersion         int64                  `json:"link_fact_version"`
	PlanGeneration          int64                  `json:"plan_generation"`
	FromSourceID            string                 `json:"from_source_id"`
	FromChannelID           string                 `json:"from_channel_id"`
	FromGroupKey            string                 `json:"from_group_key"`
	ToSourceID              string                 `json:"to_source_id"`
	ToChannelID             string                 `json:"to_channel_id"`
	ToGroupKey              string                 `json:"to_group_key"`
	ModelKey                string                 `json:"model_key"`
	PriceDimension          UpstreamPriceDimension `json:"price_dimension"`
	SettlementCurrency      string                 `json:"settlement_currency"`
	PerTokens               int64                  `json:"per_tokens"`
	AffectedPlanIDs         []string               `json:"affected_plan_ids"`
	AffectedDownstreams     []string               `json:"affected_downstreams"`
	EvidenceIDs             []string               `json:"evidence_ids"`
	FormulaVersion          string                 `json:"formula_version"`
	StrategyVersion         string                 `json:"strategy_version"`
	Now                     time.Time              `json:"now"`
}

type UpstreamRecommendationStaleReason string

const (
	UpstreamRecommendationStaleExpired             UpstreamRecommendationStaleReason = "expired"
	UpstreamRecommendationStaleOwner               UpstreamRecommendationStaleReason = "owner_changed"
	UpstreamRecommendationStaleIntelligenceVersion UpstreamRecommendationStaleReason = "intelligence_fact_version_changed"
	UpstreamRecommendationStaleCostVersion         UpstreamRecommendationStaleReason = "cost_ledger_fact_version_changed"
	UpstreamRecommendationStaleLinkVersion         UpstreamRecommendationStaleReason = "link_fact_version_changed"
	UpstreamRecommendationStalePlanGeneration      UpstreamRecommendationStaleReason = "plan_generation_changed"
	UpstreamRecommendationStaleMapping             UpstreamRecommendationStaleReason = "mapping_changed"
	UpstreamRecommendationStaleDimension           UpstreamRecommendationStaleReason = "dimension_changed"
	UpstreamRecommendationStaleEvidence            UpstreamRecommendationStaleReason = "evidence_changed"
	UpstreamRecommendationStaleFormula             UpstreamRecommendationStaleReason = "formula_changed"
	UpstreamRecommendationStaleStrategy            UpstreamRecommendationStaleReason = "strategy_changed"
	UpstreamRecommendationStaleInvalidCurrentFacts UpstreamRecommendationStaleReason = "invalid_current_facts"
)

type UpstreamRecommendationValidity struct {
	Current bool                                `json:"current"`
	Reasons []UpstreamRecommendationStaleReason `json:"reasons"`
}

type UpstreamRecommendationEventType string

const (
	UpstreamRecommendationEventStartShadow   UpstreamRecommendationEventType = "start_shadow"
	UpstreamRecommendationEventShadowPassed  UpstreamRecommendationEventType = "shadow_passed"
	UpstreamRecommendationEventShadowBlocked UpstreamRecommendationEventType = "shadow_blocked"
	UpstreamRecommendationEventStartDryRun   UpstreamRecommendationEventType = "start_dry_run"
	UpstreamRecommendationEventDryRunPassed  UpstreamRecommendationEventType = "dry_run_passed"
	UpstreamRecommendationEventDryRunBlocked UpstreamRecommendationEventType = "dry_run_blocked"
	UpstreamRecommendationEventDismiss       UpstreamRecommendationEventType = "dismiss"
	UpstreamRecommendationEventExpire        UpstreamRecommendationEventType = "expire"
)

type UpstreamRecommendationEvent struct {
	Type     UpstreamRecommendationEventType `json:"type"`
	UserID   int64                           `json:"user_id"`
	Now      time.Time                       `json:"now"`
	DryRunID string                          `json:"dry_run_id,omitempty"`
}

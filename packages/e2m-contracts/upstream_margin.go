package contracts

import "time"

const UpstreamMarginDefaultMinimumAttributableCoverage CanonicalDecimal = "0.9"

// UpstreamRevenueFact is an immutable, owner-scoped revenue fact. Amount is a
// sealed accounting amount, not a catalog price or a revenue estimate. A zero
// amount is valid; absence is represented by supplying no revenue facts.
type UpstreamRevenueFact struct {
	ID                   string           `json:"id"`
	IdempotencyKey       string           `json:"idempotency_key"`
	UserID               int64            `json:"user_id"`
	RevenueObservationID string           `json:"revenue_observation_id"`
	Amount               CanonicalDecimal `json:"amount"`
	Currency             string           `json:"currency"`
	CalculationVersion   string           `json:"calculation_version"`
	OccurredAt           time.Time        `json:"occurred_at"`
	CreatedAt            time.Time        `json:"created_at"`
}

// UpstreamMarginCostBucket is the five-column cost presentation. Derived cost
// belongs to exact because it is a deterministic calculation from complete,
// versioned evidence; its raw count remains visible in the exact column.
type UpstreamMarginCostBucket string

const (
	UpstreamMarginCostExact        UpstreamMarginCostBucket = "exact"
	UpstreamMarginCostEstimated    UpstreamMarginCostBucket = "estimated"
	UpstreamMarginCostUnknown      UpstreamMarginCostBucket = "unknown"
	UpstreamMarginCostUnattributed UpstreamMarginCostBucket = "unattributed"
	UpstreamMarginCostExpired      UpstreamMarginCostBucket = "expired"
)

func IsUpstreamMarginCostBucket(value UpstreamMarginCostBucket) bool {
	switch value {
	case UpstreamMarginCostExact, UpstreamMarginCostEstimated, UpstreamMarginCostUnknown, UpstreamMarginCostUnattributed, UpstreamMarginCostExpired:
		return true
	default:
		return false
	}
}

// UpstreamMarginMoney is one currency-local total. Aggregators must emit
// separate entries per currency unless explicit, versioned FX evidence exists.
type UpstreamMarginMoney struct {
	Currency string           `json:"currency"`
	Amount   CanonicalDecimal `json:"amount"`
}

type UpstreamMarginCostColumn struct {
	FactCount int                   `json:"fact_count"`
	Amounts   []UpstreamMarginMoney `json:"amounts"`
	Reasons   map[string]int        `json:"reasons,omitempty"`
}

type UpstreamMarginCostBreakdown struct {
	Exact        UpstreamMarginCostColumn `json:"exact"`
	Estimated    UpstreamMarginCostColumn `json:"estimated"`
	Unknown      UpstreamMarginCostColumn `json:"unknown"`
	Unattributed UpstreamMarginCostColumn `json:"unattributed"`
	Expired      UpstreamMarginCostColumn `json:"expired"`

	// ExactFactCount and DerivedFactCount explain the composition of Exact.
	ExactFactCount   int `json:"exact_fact_count"`
	DerivedFactCount int `json:"derived_fact_count"`
}

// UpstreamMarginCostReadResponse is the browser-safe purchase-cost projection.
// It deliberately excludes owner identity, revenue, margin amounts/rates and
// the full accounting claim. Those require a separate financial authorization
// boundary; this view only explains whether a margin claim would be safe.
type UpstreamMarginCostReadResponse struct {
	Window                      UpstreamIntelligenceReadWindow `json:"window"`
	WindowStart                 time.Time                      `json:"window_start"`
	WindowEnd                   time.Time                      `json:"window_end"`
	GeneratedAt                 time.Time                      `json:"generated_at"`
	Costs                       UpstreamMarginCostBreakdown    `json:"costs"`
	TotalCostFactCount          int                            `json:"total_cost_fact_count"`
	AttributableCostFactCount   int                            `json:"attributable_cost_fact_count"`
	UncoveredCostFactCount      int                            `json:"uncovered_cost_fact_count"`
	AttributableCoverage        CanonicalDecimal               `json:"attributable_coverage"`
	MinimumAttributableCoverage CanonicalDecimal               `json:"minimum_attributable_coverage"`
	CoverageGatePassed          bool                           `json:"coverage_gate_passed"`
	BlockedReasons              []UpstreamMarginBlockedReason  `json:"blocked_reasons"`
}

type UpstreamMarginClaimStatus string

const (
	// Exact means every cost fact is attributable with exact/derived evidence.
	UpstreamMarginClaimExact UpstreamMarginClaimStatus = "exact"
	// Estimated is explicitly not a true-margin claim. It may contain estimated
	// cost or a bounded (<=10%) fact-coverage gap. Its money fields are an
	// explicitly estimated point result, never a true-margin assertion.
	UpstreamMarginClaimEstimated UpstreamMarginClaimStatus = "estimated"
	UpstreamMarginClaimBlocked   UpstreamMarginClaimStatus = "blocked"
)

type UpstreamMarginBlockedReason string

const (
	UpstreamMarginBlockedNoCostFacts        UpstreamMarginBlockedReason = "no_cost_facts"
	UpstreamMarginBlockedCoverageBelowGate  UpstreamMarginBlockedReason = "coverage_below_gate"
	UpstreamMarginBlockedRevenueUnavailable UpstreamMarginBlockedReason = "revenue_unavailable"
	UpstreamMarginBlockedCrossCurrency      UpstreamMarginBlockedReason = "cross_currency_without_fx"
)

func IsUpstreamMarginClaimStatus(value UpstreamMarginClaimStatus) bool {
	return value == UpstreamMarginClaimExact || value == UpstreamMarginClaimEstimated || value == UpstreamMarginClaimBlocked
}

func IsUpstreamMarginBlockedReason(value UpstreamMarginBlockedReason) bool {
	switch value {
	case UpstreamMarginBlockedNoCostFacts,
		UpstreamMarginBlockedCoverageBelowGate,
		UpstreamMarginBlockedRevenueUnavailable,
		UpstreamMarginBlockedCrossCurrency:
		return true
	default:
		return false
	}
}

// UpstreamMarginClaim keeps exact claims distinct from estimates. MarginAmount
// and MarginRate are nil while blocked, so missing revenue can never become a
// zero-revenue or zero-margin assertion.
type UpstreamMarginClaim struct {
	Status         UpstreamMarginClaimStatus     `json:"status"`
	BlockedReasons []UpstreamMarginBlockedReason `json:"blocked_reasons"`
	Currency       string                        `json:"currency,omitempty"`
	Revenue        *CanonicalDecimal             `json:"revenue"`
	PurchaseCost   *CanonicalDecimal             `json:"purchase_cost"`
	MarginAmount   *CanonicalDecimal             `json:"margin_amount"`
	MarginRate     *CanonicalDecimal             `json:"margin_rate"`
}

// UpstreamMarginReadModel is deliberately count-based for coverage: each
// immutable cost fact is one expected accounting line, including observed
// zero-quantity lines. Unknown monetary amounts cannot safely weight their own
// denominator. The exact count and uncovered count make that rule auditable.
type UpstreamMarginReadModel struct {
	UserID                      int64                       `json:"user_id"`
	Costs                       UpstreamMarginCostBreakdown `json:"costs"`
	Revenue                     []UpstreamMarginMoney       `json:"revenue"`
	TotalCostFactCount          int                         `json:"total_cost_fact_count"`
	AttributableCostFactCount   int                         `json:"attributable_cost_fact_count"`
	UncoveredCostFactCount      int                         `json:"uncovered_cost_fact_count"`
	AttributableCoverage        CanonicalDecimal            `json:"attributable_coverage"`
	MinimumAttributableCoverage CanonicalDecimal            `json:"minimum_attributable_coverage"`
	CoverageGatePassed          bool                        `json:"coverage_gate_passed"`
	Claim                       UpstreamMarginClaim         `json:"claim"`
}

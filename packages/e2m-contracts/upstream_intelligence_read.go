package contracts

import (
	"math/big"
	"time"
)

// UpstreamIntelligenceReadMetadata is embedded by every read-model response.
// FactVersion is an owner-scoped monotonic consistency token, not a timestamp.
// Embedding keeps both fields at the top level of the JSON response.
type UpstreamIntelligenceReadMetadata struct {
	FactVersion int64     `json:"fact_version"`
	GeneratedAt time.Time `json:"generated_at"`
}

type UpstreamIntelligenceFreshness string
type UpstreamIntelligenceReadWindow string
type UpstreamIntelligenceEvidenceKind string
type UpstreamIntelligenceComparabilityReason string
type UpstreamIntelligenceFrontierLinkState string
type UpstreamIntelligenceFrontierPointStatus string

const (
	UpstreamFreshnessCurrent UpstreamIntelligenceFreshness = "current"
	UpstreamFreshnessStale   UpstreamIntelligenceFreshness = "stale"
	UpstreamFreshnessExpired UpstreamIntelligenceFreshness = "expired"

	UpstreamIntelligenceWindow24h UpstreamIntelligenceReadWindow = "24h"
	UpstreamIntelligenceWindow7d  UpstreamIntelligenceReadWindow = "7d"

	UpstreamIntelligenceEvidenceWallet UpstreamIntelligenceEvidenceKind = "wallet"
	UpstreamIntelligenceEvidenceOffer  UpstreamIntelligenceEvidenceKind = "offer"
	UpstreamIntelligenceEvidenceChange UpstreamIntelligenceEvidenceKind = "change"

	UpstreamIntelligenceNotComparableMissingCurrency      UpstreamIntelligenceComparabilityReason = "missing_currency"
	UpstreamIntelligenceNotComparableCurrencyMismatch     UpstreamIntelligenceComparabilityReason = "currency_mismatch"
	UpstreamIntelligenceNotComparableMissingUnit          UpstreamIntelligenceComparabilityReason = "missing_unit"
	UpstreamIntelligenceNotComparableUnitMismatch         UpstreamIntelligenceComparabilityReason = "unit_mismatch"
	UpstreamIntelligenceNotComparableMissingPrice         UpstreamIntelligenceComparabilityReason = "missing_price"
	UpstreamIntelligenceNotComparableMissingMultiplier    UpstreamIntelligenceComparabilityReason = "missing_multiplier"
	UpstreamIntelligenceNotComparableMissingRechargeYield UpstreamIntelligenceComparabilityReason = "missing_recharge_yield"
	UpstreamIntelligenceNotComparableInvalidRechargeYield UpstreamIntelligenceComparabilityReason = "invalid_recharge_yield"
	UpstreamIntelligenceNotComparableUnknownEvidence      UpstreamIntelligenceComparabilityReason = "unknown_evidence"
	UpstreamIntelligenceNotComparableUnattributedEvidence UpstreamIntelligenceComparabilityReason = "unattributed_evidence"
	UpstreamIntelligenceNotComparableIncompleteCoverage   UpstreamIntelligenceComparabilityReason = "incomplete_coverage"
	UpstreamIntelligenceNotComparableStaleEvidence        UpstreamIntelligenceComparabilityReason = "stale_evidence"
	UpstreamIntelligenceNotComparableExpiredEvidence      UpstreamIntelligenceComparabilityReason = "expired_evidence"
	UpstreamIntelligenceNotComparableUnlinkedQuality      UpstreamIntelligenceComparabilityReason = "unlinked_quality"
	UpstreamIntelligenceNotComparableQualityUnavailable   UpstreamIntelligenceComparabilityReason = "quality_unavailable"
	UpstreamIntelligenceNotComparableQualityInsufficient  UpstreamIntelligenceComparabilityReason = "quality_insufficient"
	UpstreamIntelligenceNotComparableQualityStale         UpstreamIntelligenceComparabilityReason = "quality_stale"

	UpstreamIntelligenceFrontierLinked   UpstreamIntelligenceFrontierLinkState = "linked"
	UpstreamIntelligenceFrontierUnlinked UpstreamIntelligenceFrontierLinkState = "unlinked"

	UpstreamIntelligenceFrontierEligible UpstreamIntelligenceFrontierPointStatus = "eligible"
	UpstreamIntelligenceFrontierBlocked  UpstreamIntelligenceFrontierPointStatus = "blocked"
)

func IsUpstreamIntelligenceFreshness(value UpstreamIntelligenceFreshness) bool {
	switch value {
	case UpstreamFreshnessCurrent, UpstreamFreshnessStale, UpstreamFreshnessExpired:
		return true
	default:
		return false
	}
}

func IsUpstreamIntelligenceComparabilityReason(value UpstreamIntelligenceComparabilityReason) bool {
	switch value {
	case UpstreamIntelligenceNotComparableMissingCurrency,
		UpstreamIntelligenceNotComparableCurrencyMismatch,
		UpstreamIntelligenceNotComparableMissingUnit,
		UpstreamIntelligenceNotComparableUnitMismatch,
		UpstreamIntelligenceNotComparableMissingPrice,
		UpstreamIntelligenceNotComparableMissingMultiplier,
		UpstreamIntelligenceNotComparableMissingRechargeYield,
		UpstreamIntelligenceNotComparableInvalidRechargeYield,
		UpstreamIntelligenceNotComparableUnknownEvidence,
		UpstreamIntelligenceNotComparableUnattributedEvidence,
		UpstreamIntelligenceNotComparableIncompleteCoverage,
		UpstreamIntelligenceNotComparableStaleEvidence,
		UpstreamIntelligenceNotComparableExpiredEvidence,
		UpstreamIntelligenceNotComparableUnlinkedQuality,
		UpstreamIntelligenceNotComparableQualityUnavailable,
		UpstreamIntelligenceNotComparableQualityInsufficient,
		UpstreamIntelligenceNotComparableQualityStale:
		return true
	default:
		return false
	}
}

// UpstreamIntelligenceComparability makes an honest non-comparable result a
// first-class value. A comparable row has an empty reason; a non-comparable row
// must carry one stable allowlisted blocker.
type UpstreamIntelligenceComparability struct {
	Comparable          bool                                    `json:"comparable"`
	ComparabilityReason UpstreamIntelligenceComparabilityReason `json:"comparability_reason,omitempty"`
}

func (value UpstreamIntelligenceComparability) Valid() bool {
	if value.Comparable {
		return value.ComparabilityReason == ""
	}
	return IsUpstreamIntelligenceComparabilityReason(value.ComparabilityReason)
}

// UpstreamIntelligenceReadEvidence keeps accuracy, coverage and freshness
// orthogonal. Confidence is a canonical decimal in [0,1] and is only meaningful
// for derived/estimated facts. Unknown and unattributed facts retain their
// explicit accuracy instead of being represented as zero.
type UpstreamIntelligenceReadEvidence struct {
	Accuracy      UpstreamEvidenceAccuracy      `json:"accuracy"`
	Coverage      UpstreamEvidenceCoverage      `json:"coverage"`
	Freshness     UpstreamIntelligenceFreshness `json:"freshness"`
	Confidence    *CanonicalDecimal             `json:"confidence"`
	ObservedAt    time.Time                     `json:"observed_at"`
	EffectiveAt   *time.Time                    `json:"effective_at,omitempty"`
	ReceivedAt    time.Time                     `json:"received_at"`
	FreshUntil    time.Time                     `json:"fresh_until"`
	MissingFields []string                      `json:"missing_fields"`
	ReasonCode    string                        `json:"reason_code,omitempty"`
}

// Valid checks the cross-field evidence invariants that must survive read
// projection. In particular, confidence cannot be used to make exact/unknown
// facts look more certain, and unknown/unattributed facts must remain
// explainable rather than silently becoming zeroes. A nil confidence for a
// derived/estimated fact means the upstream did not provide a defensible
// confidence value; it remains unknown instead of being invented by Core.
func (value UpstreamIntelligenceReadEvidence) Valid() bool {
	if !isUpstreamIntelligenceReadAccuracy(value.Accuracy) ||
		!isUpstreamIntelligenceReadCoverage(value.Coverage) ||
		!IsUpstreamIntelligenceFreshness(value.Freshness) {
		return false
	}
	if value.Confidence != nil {
		if value.Accuracy != UpstreamEvidenceDerived && value.Accuracy != UpstreamEvidenceEstimated {
			return false
		}
		confidence, err := value.Confidence.Rat()
		if err != nil || confidence.Sign() < 0 || confidence.Cmp(new(big.Rat).SetInt64(1)) > 0 {
			return false
		}
	}
	if (value.Accuracy == UpstreamEvidenceUnknown || value.Accuracy == UpstreamEvidenceUnattributed) &&
		len(value.MissingFields) == 0 && value.ReasonCode == "" {
		return false
	}
	return true
}

func isUpstreamIntelligenceReadAccuracy(value UpstreamEvidenceAccuracy) bool {
	switch value {
	case UpstreamEvidenceExact, UpstreamEvidenceDerived, UpstreamEvidenceEstimated,
		UpstreamEvidenceUnknown, UpstreamEvidenceUnattributed:
		return true
	default:
		return false
	}
}

func isUpstreamIntelligenceReadCoverage(value UpstreamEvidenceCoverage) bool {
	switch value {
	case UpstreamCoverageComplete, UpstreamCoveragePartial, UpstreamCoverageUnavailable:
		return true
	default:
		return false
	}
}

// UpstreamIntelligenceReadSourceSummary is safe for Core/browser responses. It
// deliberately excludes Connector/local identity, credentials, URLs and hashes.
type UpstreamIntelligenceReadSourceSummary struct {
	ID            string                           `json:"id"`
	Mode          UpstreamIntelligenceSourceMode   `json:"mode"`
	Provider      string                           `json:"provider"`
	DisplayName   string                           `json:"display_name"`
	Currency      string                           `json:"currency,omitempty"`
	Status        UpstreamIntelligenceSourceStatus `json:"status"`
	Capabilities  UpstreamIntelligenceCapabilities `json:"capabilities"`
	Freshness     *UpstreamIntelligenceFreshness   `json:"freshness"`
	LastRunAt     *time.Time                       `json:"last_run_at"`
	LastSuccessAt *time.Time                       `json:"last_success_at"`
	NextPollAt    *time.Time                       `json:"next_poll_at"`
	LastCoverage  UpstreamEvidenceCoverage         `json:"last_coverage,omitempty"`
	LastErrorCode string                           `json:"last_error_code,omitempty"`
}

type UpstreamIntelligenceWalletReadModel struct {
	ObservationID string                                `json:"observation_id"`
	Source        UpstreamIntelligenceReadSourceSummary `json:"source"`
	BalanceAmount *CanonicalDecimal                     `json:"balance_amount"`
	UnitKind      UpstreamWalletUnitKind                `json:"unit_kind"`
	Currency      string                                `json:"currency,omitempty"`
	Evidence      UpstreamIntelligenceReadEvidence      `json:"evidence"`
}

type UpstreamIntelligenceRateReadModel struct {
	ObservationID       string                                `json:"observation_id"`
	Source              UpstreamIntelligenceReadSourceSummary `json:"source"`
	GroupKey            string                                `json:"group_key"`
	ModelKey            string                                `json:"model_key"`
	PriceDimension      UpstreamPriceDimension                `json:"price_dimension"`
	SettlementCurrency  string                                `json:"settlement_currency,omitempty"`
	PerTokens           int64                                 `json:"per_tokens"`
	GroupMultiplier     *CanonicalDecimal                     `json:"group_multiplier"`
	RechargeYield       *CanonicalDecimal                     `json:"recharge_yield"`
	PublishedUnitPrice  *CanonicalDecimal                     `json:"published_unit_price"`
	EffectiveMultiplier *CanonicalDecimal                     `json:"effective_multiplier"`
	EffectiveUnitCost   *CanonicalDecimal                     `json:"effective_unit_cost"`
	FormulaVersion      string                                `json:"formula_version,omitempty"`
	Evidence            UpstreamIntelligenceReadEvidence      `json:"evidence"`
	UpstreamIntelligenceComparability
}

type UpstreamIntelligenceChangeReadModel struct {
	ID                  string                                `json:"id"`
	Source              UpstreamIntelligenceReadSourceSummary `json:"source"`
	Type                UpstreamChangeEventType               `json:"event_type"`
	BeforeObservationID string                                `json:"before_observation_id,omitempty"`
	AfterObservationID  string                                `json:"after_observation_id,omitempty"`
	AbsoluteChange      *CanonicalDecimal                     `json:"absolute_change"`
	PercentageChange    *CanonicalDecimal                     `json:"percentage_change"`
	FirstDetectedAt     time.Time                             `json:"first_detected_at"`
	ConfirmedAt         time.Time                             `json:"confirmed_at"`
	Severity            UpstreamChangeSeverity                `json:"severity"`
	ImpactScope         map[string]string                     `json:"impact_scope,omitempty"`
	GroupKey            string                                `json:"group_key,omitempty"`
	ModelKey            string                                `json:"model_key,omitempty"`
	PriceDimension      UpstreamPriceDimension                `json:"price_dimension,omitempty"`
}

// UpstreamIntelligenceReadRunSummary supplies drill-down context without
// exposing snapshot/manifest hashes or any Connector identity.
type UpstreamIntelligenceReadRunSummary struct {
	ID          string                    `json:"id"`
	Trigger     UpstreamCollectionTrigger `json:"trigger"`
	Status      UpstreamCollectionStatus  `json:"status"`
	Coverage    UpstreamEvidenceCoverage  `json:"coverage"`
	StartedAt   time.Time                 `json:"started_at"`
	ObservedAt  time.Time                 `json:"observed_at"`
	ReceivedAt  time.Time                 `json:"received_at"`
	CompletedAt *time.Time                `json:"completed_at"`
	FactCount   int                       `json:"fact_count"`
	PageCount   int                       `json:"page_count"`
	ErrorCode   string                    `json:"error_code,omitempty"`
	Retryable   bool                      `json:"retryable"`
}

// UpstreamIntelligenceFrontierQualityEvidence is the browser-safe proof behind
// one cost-quality point. QualityScore remains on the point because it is an
// axis value; this structure explains whether the score has enough fresh
// observations to be used. Scores are canonical decimals in [0,100], with a
// higher score meaning better quality. Raw observations and deployment-local
// identities deliberately do not enter this DTO.
type UpstreamIntelligenceFrontierQualityEvidence struct {
	SnapshotID              string                        `json:"snapshot_id"`
	Window                  HealthWindow                  `json:"window"`
	QualitySampleCount      int                           `json:"quality_sample_count"`
	MinimumSampleCount      int                           `json:"minimum_sample_count"`
	SuccessRate             *CanonicalDecimal             `json:"success_rate"`
	TTFTP95Milliseconds     *CanonicalDecimal             `json:"ttft_p95_ms"`
	DurationP95Milliseconds *CanonicalDecimal             `json:"duration_p95_ms"`
	HealthState             HealthState                   `json:"health_state"`
	ObservedAt              time.Time                     `json:"observed_at"`
	FreshUntil              time.Time                     `json:"fresh_until"`
	Freshness               UpstreamIntelligenceFreshness `json:"freshness"`
}

type UpstreamIntelligenceFrontierPoint struct {
	Rate            UpstreamIntelligenceRateReadModel            `json:"rate"`
	LinkState       UpstreamIntelligenceFrontierLinkState        `json:"link_state"`
	ChannelID       string                                       `json:"channel_id,omitempty"`
	QualityScore    *CanonicalDecimal                            `json:"quality_score"`
	QualityEvidence *UpstreamIntelligenceFrontierQualityEvidence `json:"quality_evidence"`
	Status          UpstreamIntelligenceFrontierPointStatus      `json:"status"`
	BlockedReasons  []UpstreamIntelligenceComparabilityReason    `json:"blocked_reasons"`
	OnFrontier      bool                                         `json:"on_frontier"`
}

// UpstreamIntelligenceLinkReadModel is the browser-safe form of an explicit
// cost-to-quality mapping. Source-identity targets stay opaque: operators can
// see the resolved allocated channel, but Core never returns the deployment-
// local source identity used to prove that resolution.
type UpstreamIntelligenceLinkReadModel struct {
	ID                   string                         `json:"id"`
	IntelligenceSourceID string                         `json:"intelligence_source_id"`
	Scope                UpstreamIntelligenceLinkScope  `json:"link_scope"`
	ChannelID            string                         `json:"channel_id"`
	PriceDimension       UpstreamPriceDimension         `json:"price_dimension"`
	Status               UpstreamIntelligenceLinkStatus `json:"status"`
	VerifiedAt           *time.Time                     `json:"verified_at"`
	CreatedAt            time.Time                      `json:"created_at"`
	UpdatedAt            time.Time                      `json:"updated_at"`
}

type UpstreamIntelligenceLinksResponse struct {
	UpstreamIntelligenceReadMetadata
	Items []UpstreamIntelligenceLinkReadModel `json:"items"`
}

// UpstreamIntelligenceLinkWriteRequest accepts exactly one explicit target.
// UpstreamSourceIdentity is input-only and must never be copied into a read
// DTO, audit detail, log, or error message.
type UpstreamIntelligenceLinkWriteRequest struct {
	ID                     string                         `json:"id,omitempty"`
	UserID                 int64                          `json:"user_id"`
	IntelligenceSourceID   string                         `json:"intelligence_source_id"`
	Scope                  UpstreamIntelligenceLinkScope  `json:"link_scope"`
	UpstreamSourceIdentity string                         `json:"upstream_source_identity,omitempty"`
	ChannelID              string                         `json:"channel_id,omitempty"`
	PriceDimension         UpstreamPriceDimension         `json:"price_dimension"`
	Status                 UpstreamIntelligenceLinkStatus `json:"status"`
}

type UpstreamIntelligenceOverviewMetrics struct {
	SourceCount             int               `json:"source_count"`
	ActiveSourceCount       int               `json:"active_source_count"`
	StaleSourceCount        int               `json:"stale_source_count"`
	ExpiredSourceCount      int               `json:"expired_source_count"`
	FailedSourceCount       int               `json:"failed_source_count"`
	CurrentRateCount        int               `json:"current_rate_count"`
	ComparableRateCount     int               `json:"comparable_rate_count"`
	FreshComparableCoverage *CanonicalDecimal `json:"fresh_comparable_coverage"`
	BalanceRiskSourceCount  int               `json:"balance_risk_source_count"`
	Changes24h              int               `json:"changes_24h"`
	Changes7d               int               `json:"changes_7d"`
	NextPollAt              *time.Time        `json:"next_poll_at"`
}

type UpstreamIntelligenceOverviewResponse struct {
	UpstreamIntelligenceReadMetadata
	Metrics       UpstreamIntelligenceOverviewMetrics   `json:"metrics"`
	Wallets       []UpstreamIntelligenceWalletReadModel `json:"wallets"`
	TopRates      []UpstreamIntelligenceRateReadModel   `json:"top_rates"`
	RecentChanges []UpstreamIntelligenceChangeReadModel `json:"recent_changes"`
	Frontier      []UpstreamIntelligenceFrontierPoint   `json:"frontier"`
}

type UpstreamIntelligenceSourcesResponse struct {
	UpstreamIntelligenceReadMetadata
	Items      []UpstreamIntelligenceReadSourceSummary `json:"items"`
	NextCursor string                                  `json:"next_cursor,omitempty"`
}

type UpstreamIntelligenceSourceDetailResponse struct {
	UpstreamIntelligenceReadMetadata
	Source            UpstreamIntelligenceReadSourceSummary `json:"source"`
	Wallet            *UpstreamIntelligenceWalletReadModel  `json:"wallet"`
	CurrentRates      []UpstreamIntelligenceRateReadModel   `json:"current_rates"`
	RecentChanges     []UpstreamIntelligenceChangeReadModel `json:"recent_changes"`
	RatesNextCursor   string                                `json:"rates_next_cursor,omitempty"`
	ChangesNextCursor string                                `json:"changes_next_cursor,omitempty"`
}

type UpstreamIntelligenceRatesResponse struct {
	UpstreamIntelligenceReadMetadata
	Items      []UpstreamIntelligenceRateReadModel `json:"items"`
	NextCursor string                              `json:"next_cursor,omitempty"`
}

type UpstreamIntelligenceChangesResponse struct {
	UpstreamIntelligenceReadMetadata
	Items      []UpstreamIntelligenceChangeReadModel `json:"items"`
	NextCursor string                                `json:"next_cursor,omitempty"`
}

type UpstreamIntelligenceEvidenceResponse struct {
	UpstreamIntelligenceReadMetadata
	ID     string                                `json:"id"`
	Kind   UpstreamIntelligenceEvidenceKind      `json:"kind"`
	Source UpstreamIntelligenceReadSourceSummary `json:"source"`
	Run    *UpstreamIntelligenceReadRunSummary   `json:"run"`
	Wallet *UpstreamIntelligenceWalletReadModel  `json:"wallet"`
	Offer  *UpstreamIntelligenceRateReadModel    `json:"offer"`
	Change *UpstreamIntelligenceChangeReadModel  `json:"change"`
}

type UpstreamIntelligenceFrontierResponse struct {
	UpstreamIntelligenceReadMetadata
	Items      []UpstreamIntelligenceFrontierPoint `json:"items"`
	NextCursor string                              `json:"next_cursor,omitempty"`
}

// Read filters are owner-agnostic wire/service inputs; the authenticated owner
// is supplied separately by Core. Normalize caps every list before store use.
type UpstreamIntelligenceOverviewFilter struct {
	SourceID string
	ModelKey string
	GroupKey string
	Provider string
	Currency string
	Window   UpstreamIntelligenceReadWindow
	Accuracy UpstreamEvidenceAccuracy
}

type UpstreamIntelligenceSourcesFilter struct {
	Status    UpstreamIntelligenceSourceStatus
	Provider  string
	Currency  string
	Accuracy  UpstreamEvidenceAccuracy
	Coverage  UpstreamEvidenceCoverage
	Freshness UpstreamIntelligenceFreshness
	Limit     int
	Cursor    string
}

// UpstreamIntelligenceSourceDetailFilter independently bounds the two lists
// returned alongside one source. Their cursors cannot be shared because rate
// and change ordering use different keys.
type UpstreamIntelligenceSourceDetailFilter struct {
	RatesLimit    int
	RatesCursor   string
	ChangesLimit  int
	ChangesCursor string
}

func (filter UpstreamIntelligenceSourceDetailFilter) Normalize() UpstreamIntelligenceSourceDetailFilter {
	filter.RatesLimit = NormalizeUpstreamIntelligenceListLimit(filter.RatesLimit)
	filter.ChangesLimit = NormalizeUpstreamIntelligenceListLimit(filter.ChangesLimit)
	return filter
}

func (filter UpstreamIntelligenceSourcesFilter) Normalize() UpstreamIntelligenceSourcesFilter {
	filter.Limit = NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	return filter
}

type UpstreamIntelligenceRatesFilter struct {
	SourceID       string
	ModelKey       string
	GroupKey       string
	Provider       string
	Currency       string
	PriceDimension UpstreamPriceDimension
	Accuracy       UpstreamEvidenceAccuracy
	Coverage       UpstreamEvidenceCoverage
	Freshness      UpstreamIntelligenceFreshness
	Comparable     *bool
	Limit          int
	Cursor         string
}

func (filter UpstreamIntelligenceRatesFilter) Normalize() UpstreamIntelligenceRatesFilter {
	filter.Limit = NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	return filter
}

type UpstreamIntelligenceChangesFilter struct {
	SourceID string
	ModelKey string
	GroupKey string
	Type     UpstreamChangeEventType
	Severity UpstreamChangeSeverity
	Window   UpstreamIntelligenceReadWindow
	Limit    int
	Cursor   string
}

func (filter UpstreamIntelligenceChangesFilter) Normalize() UpstreamIntelligenceChangesFilter {
	filter.Limit = NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	return filter
}

type UpstreamIntelligenceFrontierFilter struct {
	SourceID       string
	ModelKey       string
	GroupKey       string
	Provider       string
	Currency       string
	PriceDimension UpstreamPriceDimension
	Freshness      UpstreamIntelligenceFreshness
	Limit          int
	Cursor         string
}

func (filter UpstreamIntelligenceFrontierFilter) Normalize() UpstreamIntelligenceFrontierFilter {
	filter.Limit = NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	return filter
}

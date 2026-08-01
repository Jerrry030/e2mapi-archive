package contracts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	UpstreamCostCalculationVersionV1 = "upstream-cost-v1"
	upstreamCostIdempotencyDomain    = "e2m.upstream-cost.v1"
)

type UpstreamCostAttribution string

const (
	UpstreamCostExact        UpstreamCostAttribution = "exact"
	UpstreamCostDerived      UpstreamCostAttribution = "derived"
	UpstreamCostEstimated    UpstreamCostAttribution = "estimated"
	UpstreamCostUnknown      UpstreamCostAttribution = "unknown"
	UpstreamCostUnattributed UpstreamCostAttribution = "unattributed"
)

type UpstreamCostPriceStatus string

const (
	UpstreamCostPriceValid       UpstreamCostPriceStatus = "valid"
	UpstreamCostPriceExpired     UpstreamCostPriceStatus = "expired"
	UpstreamCostPriceUnavailable UpstreamCostPriceStatus = "unavailable"
)

const (
	UpstreamCostReasonMissingOwner       = "missing_owner"
	UpstreamCostReasonMissingUsage       = "missing_usage"
	UpstreamCostReasonMissingChannel     = "missing_channel"
	UpstreamCostReasonMissingModel       = "missing_model"
	UpstreamCostReasonMissingGroup       = "missing_group"
	UpstreamCostReasonMissingQuantity    = "missing_quantity"
	UpstreamCostReasonUnlinkedSource     = "unlinked_source"
	UpstreamCostReasonPriceUnavailable   = "price_unavailable"
	UpstreamCostReasonPriceExpired       = "price_expired"
	UpstreamCostReasonAmbiguousPrice     = "ambiguous_price"
	UpstreamCostReasonIncompleteEvidence = "incomplete_evidence"
)

// Pointer quantities preserve observed zero versus unavailable.
type UpstreamCostUsage struct {
	ObservationID     string    `json:"observation_id"`
	UserID            int64     `json:"user_id"`
	ChannelID         string    `json:"channel_id"`
	InstanceID        string    `json:"instance_id,omitempty"`
	ModelKey          string    `json:"model_key"`
	GroupKey          string    `json:"group_key"`
	InputTokens       *int64    `json:"input_tokens"`
	OutputTokens      *int64    `json:"output_tokens"`
	CachedInputTokens *int64    `json:"cached_input_tokens"`
	RequestCount      *int64    `json:"request_count"`
	OccurredAt        time.Time `json:"occurred_at"`
}

// UpstreamCostFact is immutable. Nil amount means unknown, never zero.
type UpstreamCostFact struct {
	ID                   string                  `json:"id"`
	IdempotencyKey       string                  `json:"idempotency_key"`
	UserID               int64                   `json:"user_id"`
	FactVersion          int64                   `json:"fact_version"`
	UsageObservationID   string                  `json:"usage_observation_id"`
	ChannelID            string                  `json:"channel_id"`
	InstanceID           string                  `json:"instance_id,omitempty"`
	IntelligenceSourceID string                  `json:"intelligence_source_id,omitempty"`
	ModelKey             string                  `json:"model_key"`
	GroupKey             string                  `json:"group_key,omitempty"`
	PriceDimension       UpstreamPriceDimension  `json:"price_dimension"`
	Quantity             *int64                  `json:"quantity"`
	PerTokens            int64                   `json:"per_tokens,omitempty"`
	PriceObservationID   string                  `json:"price_observation_id,omitempty"`
	PriceEffectiveAt     *time.Time              `json:"price_effective_at,omitempty"`
	PriceValidUntil      *time.Time              `json:"price_valid_until,omitempty"`
	UnitCost             *CanonicalDecimal       `json:"unit_cost"`
	Amount               *CanonicalDecimal       `json:"amount"`
	Currency             string                  `json:"currency,omitempty"`
	Attribution          UpstreamCostAttribution `json:"attribution"`
	PriceStatus          UpstreamCostPriceStatus `json:"price_status"`
	CalculationVersion   string                  `json:"calculation_version"`
	ReasonCode           string                  `json:"reason_code,omitempty"`
	MissingFields        []string                `json:"missing_fields,omitempty"`
	OccurredAt           time.Time               `json:"occurred_at"`
	CreatedAt            time.Time               `json:"created_at"`
}

// UpstreamCostFactVersion is an owner-scoped monotonic consistency token.
// All dimensions from one atomic usage batch share the same version.
type UpstreamCostFactVersion struct {
	UserID      int64     `json:"user_id"`
	FactVersion int64     `json:"fact_version"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type UpstreamCostFactFilter struct {
	UserID         int64
	ChannelID      string
	SourceID       string
	ModelKey       string
	PriceDimension UpstreamPriceDimension
	Attribution    UpstreamCostAttribution
	Since          time.Time
	Until          time.Time
	Limit          int
}

func IsUpstreamCostAttribution(value UpstreamCostAttribution) bool {
	switch value {
	case UpstreamCostExact, UpstreamCostDerived, UpstreamCostEstimated, UpstreamCostUnknown, UpstreamCostUnattributed:
		return true
	default:
		return false
	}
}

// UpstreamCostAttributionIsKnown reports whether evidence may enter the known
// cost column. Derived is deterministic complete evidence, not an estimate;
// callers must still gate expired price status separately.
func UpstreamCostAttributionIsKnown(value UpstreamCostAttribution) bool {
	return value == UpstreamCostExact || value == UpstreamCostDerived
}

func IsUpstreamCostPriceStatus(value UpstreamCostPriceStatus) bool {
	return value == UpstreamCostPriceValid || value == UpstreamCostPriceExpired || value == UpstreamCostPriceUnavailable
}

// A different result under the same key is a conflict, not a second row.
func UpstreamCostIdempotencyKey(userID int64, usageObservationID string, dimension UpstreamPriceDimension, calculationVersion string) (string, error) {
	usageObservationID = strings.TrimSpace(usageObservationID)
	calculationVersion = strings.TrimSpace(calculationVersion)
	if userID <= 0 || usageObservationID == "" || calculationVersion == "" || !isUpstreamCostPriceDimension(dimension) {
		return "", errors.New("invalid upstream cost idempotency identity")
	}
	payload := strings.Join([]string{upstreamCostIdempotencyDomain, strconv.FormatInt(userID, 10), usageObservationID, string(dimension), calculationVersion}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:]), nil
}

func isUpstreamCostPriceDimension(value UpstreamPriceDimension) bool {
	switch value {
	case UpstreamPriceInput, UpstreamPriceOutput, UpstreamPriceCachedInput, UpstreamPriceRequest:
		return true
	default:
		return false
	}
}

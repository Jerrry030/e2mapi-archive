// Package upstreamcost builds immutable cost facts from explicitly scoped
// usage and historical price evidence. It never reads current price state.
package upstreamcost

import (
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

var ErrInvalidInput = errors.New("upstream cost: invalid input")

// PriceEvidence is a sanitized owner-scoped candidate. Target resolution is
// supplied by the store boundary; the domain still verifies every identity.
type PriceEvidence struct {
	OwnerID              int64
	ChannelID            string
	IntelligenceSourceID string
	TargetVerified       bool
	Offer                contracts.UpstreamOfferObservation
}

// BuildFacts returns one deterministic line for every supported dimension.
// Missing quantities and evidence are persisted as explicit unknown facts.
// The caller must append all returned lines as one atomic ledger batch.
func BuildFacts(usage contracts.UpstreamCostUsage, prices []PriceEvidence, calculationVersion string) ([]contracts.UpstreamCostFact, error) {
	calculationVersion = strings.TrimSpace(calculationVersion)
	if usage.UserID <= 0 || strings.TrimSpace(usage.ObservationID) == "" || usage.OccurredAt.IsZero() || calculationVersion == "" {
		return nil, ErrInvalidInput
	}
	if negativeQuantity(usage.InputTokens) || negativeQuantity(usage.OutputTokens) || negativeQuantity(usage.CachedInputTokens) || negativeQuantity(usage.RequestCount) {
		return nil, ErrInvalidInput
	}
	dimensions := []struct {
		dimension contracts.UpstreamPriceDimension
		quantity  *int64
		missing   string
	}{
		{contracts.UpstreamPriceInput, usage.InputTokens, "input_tokens"},
		{contracts.UpstreamPriceOutput, usage.OutputTokens, "output_tokens"},
		{contracts.UpstreamPriceCachedInput, usage.CachedInputTokens, "cached_input_tokens"},
		{contracts.UpstreamPriceRequest, usage.RequestCount, "request_count"},
	}
	facts := make([]contracts.UpstreamCostFact, 0, len(dimensions))
	for _, item := range dimensions {
		fact, err := buildFact(usage, prices, calculationVersion, item.dimension, item.quantity, item.missing)
		if err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, nil
}

func buildFact(usage contracts.UpstreamCostUsage, prices []PriceEvidence, version string, dimension contracts.UpstreamPriceDimension, quantity *int64, quantityField string) (contracts.UpstreamCostFact, error) {
	key, err := contracts.UpstreamCostIdempotencyKey(usage.UserID, usage.ObservationID, dimension, version)
	if err != nil {
		return contracts.UpstreamCostFact{}, err
	}
	fact := contracts.UpstreamCostFact{
		IdempotencyKey: key, UserID: usage.UserID, UsageObservationID: usage.ObservationID,
		ChannelID: strings.TrimSpace(usage.ChannelID), InstanceID: strings.TrimSpace(usage.InstanceID),
		ModelKey: strings.TrimSpace(usage.ModelKey), GroupKey: strings.TrimSpace(usage.GroupKey),
		PriceDimension: dimension, Quantity: cloneQuantity(quantity), CalculationVersion: version,
		OccurredAt:  usage.OccurredAt.UTC().Truncate(time.Microsecond),
		Attribution: contracts.UpstreamCostUnknown, PriceStatus: contracts.UpstreamCostPriceUnavailable,
	}
	if fact.ChannelID == "" {
		return blocked(fact, contracts.UpstreamCostUnattributed, contracts.UpstreamCostReasonMissingChannel, "channel_id"), nil
	}
	if fact.ModelKey == "" {
		return blocked(fact, contracts.UpstreamCostUnattributed, contracts.UpstreamCostReasonMissingModel, "model_key"), nil
	}
	if fact.GroupKey == "" {
		return blocked(fact, contracts.UpstreamCostUnattributed, contracts.UpstreamCostReasonMissingGroup, "group_key"), nil
	}
	if quantity == nil {
		return blocked(fact, contracts.UpstreamCostUnknown, contracts.UpstreamCostReasonMissingQuantity, quantityField), nil
	}

	current, expired := temporalMatches(usage, prices, dimension)
	if len(current) == 0 {
		if expired {
			fact.PriceStatus = contracts.UpstreamCostPriceExpired
			return blocked(fact, contracts.UpstreamCostUnknown, contracts.UpstreamCostReasonPriceExpired, "price_interval"), nil
		}
		return blocked(fact, contracts.UpstreamCostUnknown, contracts.UpstreamCostReasonPriceUnavailable, "price_observation_id"), nil
	}
	if len(current) != 1 {
		return blocked(fact, contracts.UpstreamCostUnknown, contracts.UpstreamCostReasonAmbiguousPrice, "price_observation_id"), nil
	}
	evidence := current[0]
	offer := evidence.Offer
	if !evidence.TargetVerified || evidence.OwnerID != usage.UserID || evidence.ChannelID != fact.ChannelID || offer.UserID != usage.UserID {
		return blocked(fact, contracts.UpstreamCostUnattributed, contracts.UpstreamCostReasonUnlinkedSource, "intelligence_source_id"), nil
	}
	if offer.Coverage != contracts.UpstreamCoverageComplete || offer.EffectiveUnitCost == nil || offer.PerTokens <= 0 || offer.SettlementCurrency == "" ||
		(offer.Accuracy != contracts.UpstreamEvidenceExact && offer.Accuracy != contracts.UpstreamEvidenceDerived && offer.Accuracy != contracts.UpstreamEvidenceEstimated) {
		return blocked(fact, contracts.UpstreamCostUnknown, contracts.UpstreamCostReasonIncompleteEvidence, "effective_unit_cost"), nil
	}
	amount, err := calculateAmount(*quantity, offer.PerTokens, *offer.EffectiveUnitCost)
	if err != nil {
		return contracts.UpstreamCostFact{}, err
	}
	fact.IntelligenceSourceID = evidence.IntelligenceSourceID
	fact.PriceObservationID, fact.PerTokens = offer.ID, offer.PerTokens
	fact.PriceEffectiveAt = timePtr(offer.EffectiveAt)
	fact.PriceValidUntil = effectiveEndForCandidate(evidence, prices)
	fact.UnitCost, fact.Amount = cloneDecimal(offer.EffectiveUnitCost), &amount
	fact.Currency, fact.PriceStatus = offer.SettlementCurrency, contracts.UpstreamCostPriceValid
	switch offer.Accuracy {
	case contracts.UpstreamEvidenceExact:
		fact.Attribution = contracts.UpstreamCostExact
	case contracts.UpstreamEvidenceDerived:
		fact.Attribution = contracts.UpstreamCostDerived
	default:
		fact.Attribution = contracts.UpstreamCostEstimated
	}
	return fact, nil
}

func temporalMatches(usage contracts.UpstreamCostUsage, prices []PriceEvidence, dimension contracts.UpstreamPriceDimension) ([]PriceEvidence, bool) {
	matches := make([]PriceEvidence, 0, 1)
	expired := false
	for _, candidate := range prices {
		offer := candidate.Offer
		if candidate.OwnerID != usage.UserID || offer.UserID != usage.UserID || candidate.ChannelID != usage.ChannelID ||
			offer.ModelKey != usage.ModelKey || offer.GroupKey != usage.GroupKey || offer.PriceDimension != dimension {
			continue
		}
		end := effectiveEndForCandidate(candidate, prices)
		if !usage.OccurredAt.Before(offer.EffectiveAt) && (end == nil || usage.OccurredAt.Before(*end)) {
			matches = append(matches, candidate)
		} else if !usage.OccurredAt.Before(offer.EffectiveAt) && end != nil && !usage.OccurredAt.Before(*end) {
			expired = true
		}
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Offer.ID < matches[j].Offer.ID })
	return matches, expired
}

func calculateAmount(quantity, perTokens int64, unit contracts.CanonicalDecimal) (contracts.CanonicalDecimal, error) {
	if quantity < 0 || perTokens <= 0 {
		return "", ErrInvalidInput
	}
	price, err := unit.Rat()
	if err != nil || price.Sign() < 0 {
		return "", ErrInvalidInput
	}
	amount := new(big.Rat).Mul(price, new(big.Rat).SetInt64(quantity))
	amount.Quo(amount, new(big.Rat).SetInt64(perTokens))
	return contracts.QuantizeCanonicalDecimal(amount, contracts.UpstreamDecimalMaxScale)
}

func effectiveEndForCandidate(candidate PriceEvidence, prices []PriceEvidence) *time.Time {
	offer := candidate.Offer
	var end *time.Time
	if offer.ValidUntil != nil {
		end = timePtr(*offer.ValidUntil)
	}
	for _, next := range prices {
		other := next.Offer
		if next.OwnerID != candidate.OwnerID || next.ChannelID != candidate.ChannelID ||
			other.UserID != offer.UserID || other.SourceID != offer.SourceID ||
			other.GroupKey != offer.GroupKey || other.ModelKey != offer.ModelKey ||
			other.PriceDimension != offer.PriceDimension || !other.EffectiveAt.After(offer.EffectiveAt) {
			continue
		}
		if end == nil || other.EffectiveAt.Before(*end) {
			end = timePtr(other.EffectiveAt)
		}
	}
	return end
}

func blocked(fact contracts.UpstreamCostFact, attribution contracts.UpstreamCostAttribution, reason string, fields ...string) contracts.UpstreamCostFact {
	fact.Attribution, fact.ReasonCode = attribution, reason
	fact.MissingFields = append([]string(nil), fields...)
	sort.Strings(fact.MissingFields)
	fact.Amount, fact.UnitCost = nil, nil
	return fact
}

func negativeQuantity(value *int64) bool { return value != nil && *value < 0 }
func cloneQuantity(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneDecimal(value *contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func timePtr(value time.Time) *time.Time {
	normalized := value.UTC().Truncate(time.Microsecond)
	return &normalized
}

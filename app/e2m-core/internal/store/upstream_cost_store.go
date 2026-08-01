package store

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"time"

	"e2m.local/contracts"
)

const upstreamCostDimensionsPerUsage = 4

// UpstreamCostStore is the narrow persistence boundary for immutable cost
// facts. The four dimensions produced for one usage observation are appended
// atomically and receive one owner-scoped FactVersion.
type UpstreamCostStore interface {
	AppendUpstreamCostFacts(context.Context, []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error)
	ListUpstreamCostFacts(context.Context, contracts.UpstreamCostFactFilter) ([]contracts.UpstreamCostFact, error)
}

var (
	_ UpstreamCostStore = (*MemoryStore)(nil)
	_ UpstreamCostStore = (*PostgresStore)(nil)
)

func validUpstreamCostBatch(facts []contracts.UpstreamCostFact) bool {
	if len(facts) != upstreamCostDimensionsPerUsage {
		return false
	}
	owner, usage, calculation := facts[0].UserID, strings.TrimSpace(facts[0].UsageObservationID), strings.TrimSpace(facts[0].CalculationVersion)
	if owner <= 0 || usage == "" || calculation == "" {
		return false
	}
	seen := make(map[contracts.UpstreamPriceDimension]struct{}, upstreamCostDimensionsPerUsage)
	for _, fact := range facts {
		if fact.ID != "" || fact.FactVersion != 0 || !fact.CreatedAt.IsZero() || fact.UserID != owner ||
			strings.TrimSpace(fact.UsageObservationID) != usage || strings.TrimSpace(fact.CalculationVersion) != calculation ||
			strings.TrimSpace(fact.IdempotencyKey) == "" || fact.OccurredAt.IsZero() || fact.Quantity != nil && *fact.Quantity < 0 ||
			!contracts.IsUpstreamCostAttribution(fact.Attribution) || !contracts.IsUpstreamCostPriceStatus(fact.PriceStatus) {
			return false
		}
		key, err := contracts.UpstreamCostIdempotencyKey(owner, usage, fact.PriceDimension, calculation)
		if err != nil || key != fact.IdempotencyKey {
			return false
		}
		if _, duplicate := seen[fact.PriceDimension]; duplicate {
			return false
		}
		seen[fact.PriceDimension] = struct{}{}
		if fact.PriceValidUntil != nil && (fact.PriceEffectiveAt == nil || !fact.PriceValidUntil.After(*fact.PriceEffectiveAt)) ||
			len(fact.ReasonCode) > 64 || !validUpstreamCostShape(fact) {
			return false
		}
	}
	return len(seen) == upstreamCostDimensionsPerUsage
}

func validUpstreamCostShape(fact contracts.UpstreamCostFact) bool {
	known := fact.Attribution == contracts.UpstreamCostExact || fact.Attribution == contracts.UpstreamCostDerived || fact.Attribution == contracts.UpstreamCostEstimated
	if known {
		return fact.PriceStatus == contracts.UpstreamCostPriceValid && fact.Quantity != nil && fact.PerTokens > 0 &&
			strings.TrimSpace(fact.ChannelID) != "" && strings.TrimSpace(fact.ModelKey) != "" && strings.TrimSpace(fact.GroupKey) != "" &&
			strings.TrimSpace(fact.IntelligenceSourceID) != "" && strings.TrimSpace(fact.PriceObservationID) != "" && fact.PriceEffectiveAt != nil &&
			validUpstreamDecimal(fact.UnitCost, 0) && fact.UnitCost != nil && validUpstreamDecimal(fact.Amount, 0) && fact.Amount != nil &&
			validUpstreamCurrency(fact.Currency) && strings.TrimSpace(fact.ReasonCode) == "" && len(fact.MissingFields) == 0
	}
	return fact.Amount == nil && fact.UnitCost == nil && strings.TrimSpace(fact.ReasonCode) != "" && len(fact.MissingFields) > 0
}

func normalizeUpstreamCostFact(fact contracts.UpstreamCostFact) contracts.UpstreamCostFact {
	fact.OccurredAt = normalizeUpstreamTime(fact.OccurredAt)
	fact.PriceEffectiveAt = normalizeUpstreamTimePtr(fact.PriceEffectiveAt)
	fact.PriceValidUntil = normalizeUpstreamTimePtr(fact.PriceValidUntil)
	fact.MissingFields = normalizeUpstreamMissingFields(fact.MissingFields)
	fact.Quantity = cloneInt64(fact.Quantity)
	fact.UnitCost = cloneCanonicalDecimal(fact.UnitCost)
	fact.Amount = cloneCanonicalDecimal(fact.Amount)
	return fact
}

func cloneUpstreamCostFact(fact contracts.UpstreamCostFact) contracts.UpstreamCostFact {
	fact.Quantity = cloneInt64(fact.Quantity)
	fact.PriceEffectiveAt = normalizeUpstreamTimePtr(fact.PriceEffectiveAt)
	fact.PriceValidUntil = normalizeUpstreamTimePtr(fact.PriceValidUntil)
	fact.UnitCost = cloneCanonicalDecimal(fact.UnitCost)
	fact.Amount = cloneCanonicalDecimal(fact.Amount)
	if fact.MissingFields != nil {
		fact.MissingFields = append([]string{}, fact.MissingFields...)
	}
	return fact
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneCanonicalDecimal(value *contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func prepareUpstreamCostBatch(facts []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, error) {
	prepared := make([]contracts.UpstreamCostFact, len(facts))
	for i, fact := range facts {
		prepared[i] = normalizeUpstreamCostFact(fact)
	}
	if !validUpstreamCostBatch(prepared) {
		return nil, ErrInvalid
	}
	sort.Slice(prepared, func(i, j int) bool {
		return upstreamCostDimensionRank(prepared[i].PriceDimension) < upstreamCostDimensionRank(prepared[j].PriceDimension)
	})
	return prepared, nil
}

func upstreamCostDimensionRank(dimension contracts.UpstreamPriceDimension) int {
	switch dimension {
	case contracts.UpstreamPriceInput:
		return 0
	case contracts.UpstreamPriceOutput:
		return 1
	case contracts.UpstreamPriceCachedInput:
		return 2
	default:
		return 3
	}
}

func costFactsEqualIgnoringServerFields(a, b contracts.UpstreamCostFact) bool {
	a.ID, b.ID, a.FactVersion, b.FactVersion, a.CreatedAt, b.CreatedAt = "", "", 0, 0, time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func (s *MemoryStore) AppendUpstreamCostFacts(ctx context.Context, facts []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	prepared, err := prepareUpstreamCostBatch(facts)
	if err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendUpstreamCostFactsLocked(prepared, normalizeUpstreamTime(s.now()))
}

func (s *MemoryStore) appendUpstreamCostFactsLocked(prepared []contracts.UpstreamCostFact, now time.Time) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	existing := make(map[string]contracts.UpstreamCostFact, len(prepared))
	for _, stored := range s.upstreamCostFacts {
		if stored.UserID == prepared[0].UserID {
			existing[stored.IdempotencyKey] = stored
		}
	}
	matched := make([]contracts.UpstreamCostFact, 0, len(prepared))
	for _, fact := range prepared {
		stored, found := existing[fact.IdempotencyKey]
		if !found {
			continue
		}
		if !costFactsEqualIgnoringServerFields(stored, fact) {
			return nil, contracts.UpstreamCostFactVersion{}, ErrConflict
		}
		matched = append(matched, stored)
	}
	if len(matched) != 0 {
		if len(matched) != len(prepared) {
			return nil, contracts.UpstreamCostFactVersion{}, ErrConflict
		}
		version := contracts.UpstreamCostFactVersion{UserID: matched[0].UserID, FactVersion: matched[0].FactVersion, UpdatedAt: matched[0].CreatedAt}
		return cloneUpstreamCostFacts(matched), version, nil
	}

	var version int64
	for _, stored := range s.upstreamCostFacts {
		if stored.UserID == prepared[0].UserID && stored.FactVersion > version {
			version = stored.FactVersion
		}
	}
	version++
	for i := range prepared {
		prepared[i].ID = s.nextID("ucost")
		prepared[i].FactVersion = version
		prepared[i].CreatedAt = now
		s.upstreamCostFacts = append(s.upstreamCostFacts, cloneUpstreamCostFact(prepared[i]))
	}
	return cloneUpstreamCostFacts(prepared), contracts.UpstreamCostFactVersion{UserID: prepared[0].UserID, FactVersion: version, UpdatedAt: now}, nil
}

func (s *MemoryStore) ListUpstreamCostFacts(ctx context.Context, filter contracts.UpstreamCostFactFilter) ([]contracts.UpstreamCostFact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := requireUpstreamOwner(filter.UserID); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]contracts.UpstreamCostFact, 0)
	for _, fact := range s.upstreamCostFacts {
		if fact.UserID != filter.UserID || filter.ChannelID != "" && fact.ChannelID != filter.ChannelID ||
			filter.SourceID != "" && fact.IntelligenceSourceID != filter.SourceID || filter.ModelKey != "" && fact.ModelKey != filter.ModelKey ||
			filter.PriceDimension != "" && fact.PriceDimension != filter.PriceDimension || filter.Attribution != "" && fact.Attribution != filter.Attribution ||
			!filter.Since.IsZero() && fact.OccurredAt.Before(filter.Since) || !filter.Until.IsZero() && !fact.OccurredAt.Before(filter.Until) {
			continue
		}
		out = append(out, cloneUpstreamCostFact(fact))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	limit := contracts.NormalizeUpstreamIntelligenceListLimit(filter.Limit)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func cloneUpstreamCostFacts(facts []contracts.UpstreamCostFact) []contracts.UpstreamCostFact {
	out := make([]contracts.UpstreamCostFact, len(facts))
	for i := range facts {
		out[i] = cloneUpstreamCostFact(facts[i])
	}
	return out
}

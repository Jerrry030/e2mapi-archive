package httpapi

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

const (
	ownerModelMarketDefaultLimit = 200
	ownerModelMarketMaximumLimit = 500
)

type ownerModelMarketStatus string

const (
	ownerModelMarketReady                ownerModelMarketStatus = "ready"
	ownerModelMarketPriceOnly            ownerModelMarketStatus = "price_only"
	ownerModelMarketInsufficientEvidence ownerModelMarketStatus = "insufficient_evidence"
)

// ownerModelMarketResponse is deliberately model-scoped and anonymous. It
// exposes the commercial and quality facts an owner needs to make a routing
// choice, but never returns source/channel/Connector identities or local
// deployment details.
type ownerModelMarketResponse struct {
	FactVersion int64                   `json:"fact_version"`
	GeneratedAt time.Time               `json:"generated_at"`
	Metrics     ownerModelMarketMetrics `json:"metrics"`
	Models      []ownerModelMarketModel `json:"models"`
	Returned    int                     `json:"returned_count"`
	Truncated   bool                    `json:"truncated"`
}

type ownerModelMarketMetrics struct {
	ModelCount                     int `json:"model_count"`
	ReadyModelCount                int `json:"ready_model_count"`
	PriceOnlyModelCount            int `json:"price_only_model_count"`
	InsufficientEvidenceModelCount int `json:"insufficient_evidence_model_count"`
	ComparableOfferCount           int `json:"comparable_offer_count"`
	QualityCoveredModelCount       int `json:"quality_covered_model_count"`
}

type ownerModelMarketModel struct {
	ModelKey             string                                  `json:"model_key"`
	Status               ownerModelMarketStatus                  `json:"status"`
	Prices               []ownerModelMarketPrice                 `json:"prices"`
	ObservedOfferCount   int                                     `json:"observed_offer_count"`
	ComparableOfferCount int                                     `json:"comparable_offer_count"`
	QualityOptionCount   int                                     `json:"quality_option_count"`
	FrontierOptionCount  int                                     `json:"frontier_option_count"`
	FreshestEvidence     contracts.UpstreamIntelligenceFreshness `json:"freshest_evidence"`
	BestQuality          *ownerModelMarketQuality                `json:"best_quality"`
}

type ownerModelMarketPrice struct {
	Dimension          contracts.UpstreamPriceDimension `json:"dimension"`
	Currency           string                           `json:"currency"`
	PerTokens          int64                            `json:"per_tokens"`
	MinimumCost        *contracts.CanonicalDecimal      `json:"minimum_cost"`
	MaximumCost        *contracts.CanonicalDecimal      `json:"maximum_cost"`
	TrustedOptionCount int                              `json:"trusted_option_count"`
}

// Every field in BestQuality comes from the same eligible cost-quality point;
// the API never combines the cheapest price from one route with the latency or
// success rate of another route.
type ownerModelMarketQuality struct {
	QualityScore  *contracts.CanonicalDecimal             `json:"quality_score"`
	SuccessRate   *contracts.CanonicalDecimal             `json:"success_rate"`
	TTFTP95MS     *contracts.CanonicalDecimal             `json:"ttft_p95_ms"`
	DurationP95MS *contracts.CanonicalDecimal             `json:"duration_p95_ms"`
	SampleCount   int                                     `json:"sample_count"`
	HealthState   contracts.HealthState                   `json:"health_state"`
	Freshness     contracts.UpstreamIntelligenceFreshness `json:"freshness"`
	OnFrontier    bool                                    `json:"on_frontier"`
	EffectiveCost *contracts.CanonicalDecimal             `json:"effective_cost"`
	Currency      string                                  `json:"currency"`
	PerTokens     int64                                   `json:"per_tokens"`
	Dimension     contracts.UpstreamPriceDimension        `json:"dimension"`
}

type ownerModelMarketFilter struct {
	query     string
	dimension contracts.UpstreamPriceDimension
	limit     int
}

type ownerModelMarketPriceKey struct {
	dimension contracts.UpstreamPriceDimension
	currency  string
	perTokens int64
}

type ownerModelMarketAccumulator struct {
	model  ownerModelMarketModel
	prices map[ownerModelMarketPriceKey]*ownerModelMarketPrice
}

func (s *Server) handleOwnerModelMarket(w http.ResponseWriter, r *http.Request) {
	setNoStore(w)
	owner, ok := requireCurrentOwner(w, r)
	if !ok {
		return
	}
	filter, ok := parseOwnerModelMarketFilter(w, r)
	if !ok {
		return
	}
	projection, _, ok := s.readUpstreamIntelligenceProjection(w, r, upstreamIntelligenceQuery{userID: owner.ID})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, buildOwnerModelMarket(projection, filter))
}

func parseOwnerModelMarketFilter(w http.ResponseWriter, r *http.Request) (ownerModelMarketFilter, bool) {
	filter := ownerModelMarketFilter{limit: ownerModelMarketDefaultLimit}
	parsed, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "query string is invalid")
		return ownerModelMarketFilter{}, false
	}
	allowed := map[string]bool{"q": true, "price_dimension": true, "limit": true}
	for key, values := range parsed {
		if !allowed[key] {
			writeError(w, http.StatusBadRequest, "validation_failed", "unknown query parameter: "+key)
			return ownerModelMarketFilter{}, false
		}
		if len(values) != 1 {
			writeError(w, http.StatusBadRequest, "validation_failed", "query parameter must not be repeated: "+key)
			return ownerModelMarketFilter{}, false
		}
		value := strings.TrimSpace(values[0])
		if value == "" {
			writeError(w, http.StatusBadRequest, "validation_failed", "query parameter must not be empty: "+key)
			return ownerModelMarketFilter{}, false
		}
		switch key {
		case "q":
			if len(value) > 128 {
				writeError(w, http.StatusBadRequest, "validation_failed", "q is too long")
				return ownerModelMarketFilter{}, false
			}
			filter.query = strings.ToLower(value)
		case "price_dimension":
			filter.dimension = contracts.UpstreamPriceDimension(value)
			if !upstreamIntelligenceAllowedPriceDimensions[value] {
				writeError(w, http.StatusBadRequest, "validation_failed", "invalid price_dimension")
				return ownerModelMarketFilter{}, false
			}
		case "limit":
			limit, err := strconv.Atoi(value)
			if err != nil || limit <= 0 || limit > ownerModelMarketMaximumLimit {
				writeError(w, http.StatusBadRequest, "validation_failed", "limit must be between 1 and "+strconv.Itoa(ownerModelMarketMaximumLimit))
				return ownerModelMarketFilter{}, false
			}
			filter.limit = limit
		}
	}
	return filter, true
}

func buildOwnerModelMarket(projection upstreamIntelligenceReadProjection, filter ownerModelMarketFilter) ownerModelMarketResponse {
	byModel := make(map[string]*ownerModelMarketAccumulator)
	modelFor := func(modelKey string) *ownerModelMarketAccumulator {
		modelKey = strings.TrimSpace(modelKey)
		if modelKey == "" || filter.query != "" && !strings.Contains(strings.ToLower(modelKey), filter.query) {
			return nil
		}
		if existing := byModel[modelKey]; existing != nil {
			return existing
		}
		created := &ownerModelMarketAccumulator{
			model: ownerModelMarketModel{
				ModelKey: modelKey, Status: ownerModelMarketInsufficientEvidence,
				Prices: []ownerModelMarketPrice{},
			},
			prices: make(map[ownerModelMarketPriceKey]*ownerModelMarketPrice),
		}
		byModel[modelKey] = created
		return created
	}

	for _, rate := range projection.rates {
		if filter.dimension != "" && rate.PriceDimension != filter.dimension {
			continue
		}
		model := modelFor(rate.ModelKey)
		if model == nil {
			continue
		}
		model.model.ObservedOfferCount++
		model.model.FreshestEvidence = fresherOwnerModelMarketEvidence(model.model.FreshestEvidence, rate.Evidence.Freshness)
		if !rate.Comparable {
			continue
		}
		if !ownerModelMarketRateHasComparablePrice(rate) {
			continue
		}
		model.model.ComparableOfferCount++
		key := ownerModelMarketPriceKey{rate.PriceDimension, rate.SettlementCurrency, rate.PerTokens}
		price := model.prices[key]
		if price == nil {
			price = &ownerModelMarketPrice{
				Dimension: rate.PriceDimension, Currency: rate.SettlementCurrency, PerTokens: rate.PerTokens,
				MinimumCost: cloneDecimalPtr(rate.EffectiveUnitCost), MaximumCost: cloneDecimalPtr(rate.EffectiveUnitCost),
			}
			model.prices[key] = price
		}
		price.TrustedOptionCount++
		if compareUpstreamIntelligenceDecimal(*rate.EffectiveUnitCost, *price.MinimumCost) < 0 {
			price.MinimumCost = cloneDecimalPtr(rate.EffectiveUnitCost)
		}
		if compareUpstreamIntelligenceDecimal(*rate.EffectiveUnitCost, *price.MaximumCost) > 0 {
			price.MaximumCost = cloneDecimalPtr(rate.EffectiveUnitCost)
		}
	}

	for _, point := range projection.frontier {
		if filter.dimension != "" && point.Rate.PriceDimension != filter.dimension {
			continue
		}
		model := modelFor(point.Rate.ModelKey)
		if model == nil || model.model.ComparableOfferCount == 0 ||
			point.Status != contracts.UpstreamIntelligenceFrontierEligible || point.QualityEvidence == nil || point.QualityScore == nil ||
			!point.Rate.Comparable || !ownerModelMarketRateHasComparablePrice(point.Rate) {
			continue
		}
		model.model.QualityOptionCount++
		if point.OnFrontier {
			model.model.FrontierOptionCount++
		}
		model.model.FreshestEvidence = fresherOwnerModelMarketEvidence(model.model.FreshestEvidence, point.QualityEvidence.Freshness)
		quality := ownerModelMarketQualityFromPoint(point)
		if model.model.BestQuality == nil || ownerModelMarketQualityBetter(quality, *model.model.BestQuality) {
			model.model.BestQuality = &quality
		}
	}

	models := make([]ownerModelMarketModel, 0, len(byModel))
	metrics := ownerModelMarketMetrics{}
	for _, accumulator := range byModel {
		for _, price := range accumulator.prices {
			accumulator.model.Prices = append(accumulator.model.Prices, *price)
		}
		sort.SliceStable(accumulator.model.Prices, func(i, j int) bool {
			left, right := accumulator.model.Prices[i], accumulator.model.Prices[j]
			if rank := ownerModelMarketDimensionRank(left.Dimension) - ownerModelMarketDimensionRank(right.Dimension); rank != 0 {
				return rank < 0
			}
			if left.Currency != right.Currency {
				return left.Currency < right.Currency
			}
			return left.PerTokens < right.PerTokens
		})
		switch {
		case accumulator.model.QualityOptionCount > 0:
			accumulator.model.Status = ownerModelMarketReady
			metrics.ReadyModelCount++
			metrics.QualityCoveredModelCount++
		case accumulator.model.ComparableOfferCount > 0:
			accumulator.model.Status = ownerModelMarketPriceOnly
			metrics.PriceOnlyModelCount++
		default:
			accumulator.model.Status = ownerModelMarketInsufficientEvidence
			metrics.InsufficientEvidenceModelCount++
		}
		metrics.ComparableOfferCount += accumulator.model.ComparableOfferCount
		models = append(models, accumulator.model)
	}
	metrics.ModelCount = len(models)
	sort.SliceStable(models, func(i, j int) bool {
		left, right := models[i], models[j]
		if rank := ownerModelMarketStatusRank(left.Status) - ownerModelMarketStatusRank(right.Status); rank != 0 {
			return rank < 0
		}
		if left.FrontierOptionCount != right.FrontierOptionCount {
			return left.FrontierOptionCount > right.FrontierOptionCount
		}
		if left.ComparableOfferCount != right.ComparableOfferCount {
			return left.ComparableOfferCount > right.ComparableOfferCount
		}
		return strings.ToLower(left.ModelKey) < strings.ToLower(right.ModelKey)
	})
	truncated := len(models) > filter.limit
	if truncated {
		models = models[:filter.limit]
	}
	return ownerModelMarketResponse{
		FactVersion: projection.metadata.FactVersion, GeneratedAt: projection.metadata.GeneratedAt,
		Metrics: metrics, Models: models, Returned: len(models), Truncated: truncated,
	}
}

func ownerModelMarketRateHasComparablePrice(rate contracts.UpstreamIntelligenceRateReadModel) bool {
	if rate.EffectiveUnitCost == nil || !rate.EffectiveUnitCost.Valid() || strings.TrimSpace(rate.SettlementCurrency) == "" ||
		rate.PerTokens <= 0 || !upstreamIntelligenceAllowedPriceDimensions[string(rate.PriceDimension)] {
		return false
	}
	cost, err := rate.EffectiveUnitCost.Rat()
	return err == nil && cost.Sign() >= 0
}

func ownerModelMarketQualityFromPoint(point contracts.UpstreamIntelligenceFrontierPoint) ownerModelMarketQuality {
	evidence := point.QualityEvidence
	return ownerModelMarketQuality{
		QualityScore: cloneDecimalPtr(point.QualityScore), SuccessRate: cloneDecimalPtr(evidence.SuccessRate),
		TTFTP95MS: cloneDecimalPtr(evidence.TTFTP95Milliseconds), DurationP95MS: cloneDecimalPtr(evidence.DurationP95Milliseconds),
		SampleCount: evidence.QualitySampleCount, HealthState: evidence.HealthState, Freshness: evidence.Freshness,
		OnFrontier: point.OnFrontier, EffectiveCost: cloneDecimalPtr(point.Rate.EffectiveUnitCost),
		Currency: point.Rate.SettlementCurrency, PerTokens: point.Rate.PerTokens, Dimension: point.Rate.PriceDimension,
	}
}

func ownerModelMarketQualityBetter(left, right ownerModelMarketQuality) bool {
	if left.OnFrontier != right.OnFrontier {
		return left.OnFrontier
	}
	if comparison := compareOwnerModelMarketOptionalDecimal(left.QualityScore, right.QualityScore); comparison != 0 {
		return comparison > 0
	}
	if comparison := compareOwnerModelMarketOptionalDecimal(left.SuccessRate, right.SuccessRate); comparison != 0 {
		return comparison > 0
	}
	if comparison := compareOwnerModelMarketOptionalDecimal(left.TTFTP95MS, right.TTFTP95MS); comparison != 0 {
		return comparison < 0
	}
	return compareOwnerModelMarketOptionalDecimal(left.EffectiveCost, right.EffectiveCost) < 0
}

func compareOwnerModelMarketOptionalDecimal(left, right *contracts.CanonicalDecimal) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return -1
	}
	if right == nil {
		return 1
	}
	return compareUpstreamIntelligenceDecimal(*left, *right)
}

func fresherOwnerModelMarketEvidence(current, candidate contracts.UpstreamIntelligenceFreshness) contracts.UpstreamIntelligenceFreshness {
	if ownerModelMarketFreshnessRank(candidate) < ownerModelMarketFreshnessRank(current) {
		return candidate
	}
	return current
}

func ownerModelMarketFreshnessRank(value contracts.UpstreamIntelligenceFreshness) int {
	switch value {
	case contracts.UpstreamFreshnessCurrent:
		return 0
	case contracts.UpstreamFreshnessStale:
		return 1
	case contracts.UpstreamFreshnessExpired:
		return 2
	default:
		return 3
	}
}

func ownerModelMarketStatusRank(value ownerModelMarketStatus) int {
	switch value {
	case ownerModelMarketReady:
		return 0
	case ownerModelMarketPriceOnly:
		return 1
	default:
		return 2
	}
}

func ownerModelMarketDimensionRank(value contracts.UpstreamPriceDimension) int {
	switch value {
	case contracts.UpstreamPriceInput:
		return 0
	case contracts.UpstreamPriceOutput:
		return 1
	case contracts.UpstreamPriceCachedInput:
		return 2
	case contracts.UpstreamPriceRequest:
		return 3
	default:
		return 4
	}
}

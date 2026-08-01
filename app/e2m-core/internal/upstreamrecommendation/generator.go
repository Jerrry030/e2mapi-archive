package upstreamrecommendation

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"e2m.local/contracts"
)

var ErrInvalidGeneratorInput = errors.New("upstream recommendation: invalid generator input")

const (
	DefaultRecommendationTTL          = time.Hour
	MaximumRecommendationTTL          = 24 * time.Hour
	recommendationQualityFreshness    = 5 * time.Minute
	recommendationMinimumQualityCount = 5
	recommendationSuccessFloor        = 0.95
	recommendationMaximumTTFTP95MS    = 4000.0
	recommendationMaximumDurationP95  = 20000.0
	maximumGeneratorCollectionSize    = 50_000
	effectiveCostFormulaVersionV1     = "effective-cost/v1"
)

// GeneratorLinkResolution is owner-derived proof that one link resolves to
// exactly one allocated channel. Connector-local source identities never enter
// the generator.
type GeneratorLinkResolution struct {
	LinkID         string
	UserID         int64
	ChannelID      string
	TargetVerified bool
}

// GeneratorInputs is a pure, owner-scoped consistency snapshot. The adapter
// must fill all collections from one read boundary. AllocatedChannels contains
// only channels proven allocated to UserID; the generator still enforces their
// lifecycle, inventory, model and group gates.
type GeneratorInputs struct {
	UserID      int64
	GeneratedAt time.Time
	// QualityReferenceTime is an optional immutable evidence boundary. It is
	// used by rollout regression revalidation to prove historical quality at
	// the recommendation generation instant while every non-quality fact stays
	// evaluated at GeneratedAt. Ordinary generation leaves it zero.
	QualityReferenceTime    time.Time
	RecommendationTTL       time.Duration
	IntelligenceFactVersion int64
	CostLedgerFactVersion   int64
	Sources                 []contracts.UpstreamIntelligenceSource
	LatestRuns              []contracts.UpstreamCollectionRun
	Wallets                 []contracts.UpstreamWalletObservation
	Offers                  []contracts.UpstreamOfferObservation
	Links                   []contracts.UpstreamIntelligenceLink
	LinkResolutions         []GeneratorLinkResolution
	QualitySnapshots        []contracts.ChannelHealthSnapshot
	CostFacts               []contracts.UpstreamCostFact
	RoutePlans              []contracts.RoutePlan
	AllocatedChannels       []contracts.UpstreamChannel
	Bindings                []contracts.PublishedBinding
}

// RecommendationIDFactory supplies an opaque persistence identity. The
// generator never derives an id from owner or upstream evidence; ids are also
// excluded from the stable recommendation fingerprint.
type RecommendationIDFactory func() string

type generatorLane struct {
	source  contracts.UpstreamIntelligenceSource
	run     contracts.UpstreamCollectionRun
	offer   contracts.UpstreamOfferObservation
	wallet  contracts.UpstreamWalletObservation
	link    contracts.UpstreamIntelligenceLink
	channel contracts.UpstreamChannel
	binding contracts.PublishedBinding
	quality contracts.ChannelHealthSnapshot
	cost    contracts.UpstreamCostFact
}

type generatorIndex struct {
	input       GeneratorInputs
	plan        contracts.RoutePlan
	sources     map[string]contracts.UpstreamIntelligenceSource
	runs        map[string]contracts.UpstreamCollectionRun
	wallets     map[string]contracts.UpstreamWalletObservation
	links       map[string]contracts.UpstreamIntelligenceLink
	resolutions map[string]GeneratorLinkResolution
	channels    map[string]contracts.UpstreamChannel
	bindings    map[string]contracts.PublishedBinding
}

// Generate constructs only fully evidenced recommendations. Missing, stale,
// partial, estimated, ambiguous or cross-owner evidence produces allowlisted
// diagnostics and never an executable-looking recommendation.
func Generate(input GeneratorInputs, nextID RecommendationIDFactory) (contracts.UpstreamRecommendationGenerationResult, error) {
	input.GeneratedAt = normalizeGeneratorTime(input.GeneratedAt)
	input.QualityReferenceTime = normalizeGeneratorTime(input.QualityReferenceTime)
	if nextID == nil || !validGeneratorEnvelope(input) {
		return contracts.UpstreamRecommendationGenerationResult{}, ErrInvalidGeneratorInput
	}
	blocked := make(map[contracts.UpstreamRecommendationGenerationReason]int)
	if input.IntelligenceFactVersion <= 0 || input.CostLedgerFactVersion <= 0 || len(input.Offers) == 0 || len(input.CostFacts) == 0 {
		addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationNoCurrentFacts, 1)
		return generatorResult(nil, blocked), nil
	}
	plan, ok := singlePublishedGeneratorPlan(input)
	if !ok {
		addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationNoPublishedPlan, 1)
		return generatorResult(nil, blocked), nil
	}
	index, err := buildGeneratorIndex(input, plan)
	if err != nil {
		return contracts.UpstreamRecommendationGenerationResult{}, err
	}
	offers := append([]contracts.UpstreamOfferObservation(nil), input.Offers...)
	sort.Slice(offers, func(i, j int) bool { return generatorOfferKey(offers[i]) < generatorOfferKey(offers[j]) })
	lanes := make([]generatorLane, 0, len(offers))
	for _, offer := range offers {
		built, reasons := index.lanesForOffer(offer)
		lanes = append(lanes, built...)
		for _, reason := range reasons {
			addGeneratorBlock(blocked, reason, 1)
		}
	}
	sort.Slice(lanes, func(i, j int) bool { return generatorLaneKey(lanes[i]) < generatorLaneKey(lanes[j]) })
	if len(lanes) < 2 {
		addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationNoCallablePair, 1)
		return generatorResult(nil, blocked), nil
	}

	ttl := input.RecommendationTTL
	if ttl == 0 {
		ttl = DefaultRecommendationTTL
	}
	recommendations := make([]contracts.UpstreamRecommendation, 0)
	fingerprints := make(map[string]struct{})
	ids := make(map[string]struct{})
	for left := 0; left < len(lanes); left++ {
		for right := left + 1; right < len(lanes); right++ {
			one, two := lanes[left], lanes[right]
			if one.channel.ID == two.channel.ID || one.source.ID == two.source.ID {
				continue
			}
			if !generatorLanesComparable(one, two) {
				addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationIncomparableCost, 1)
				continue
			}
			comparison, err := compareGeneratorCosts(one.offer, two.offer)
			if err != nil {
				addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationMissingCost, 1)
				continue
			}
			if comparison == 0 {
				addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationNoProvenSavings, 1)
				continue
			}
			from, to := one, two
			if comparison < 0 {
				from, to = two, one
			}
			candidate, err := generatorCandidate(input, plan, from, to, ttl)
			if err != nil {
				return contracts.UpstreamRecommendationGenerationResult{}, err
			}
			fingerprint, err := Fingerprint(candidate)
			if err != nil {
				addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationNoProvenSavings, 1)
				continue
			}
			if _, duplicate := fingerprints[fingerprint]; duplicate {
				continue
			}
			id := nextID()
			if !validOpaqueRecommendationID(id) {
				return contracts.UpstreamRecommendationGenerationResult{}, ErrInvalidGeneratorInput
			}
			if _, duplicate := ids[id]; duplicate {
				return contracts.UpstreamRecommendationGenerationResult{}, ErrInvalidGeneratorInput
			}
			recommendation, err := Build(id, candidate)
			if err != nil || recommendation.Fingerprint != fingerprint {
				return contracts.UpstreamRecommendationGenerationResult{}, ErrInvalidGeneratorInput
			}
			ids[id], fingerprints[fingerprint] = struct{}{}, struct{}{}
			recommendations = append(recommendations, recommendation)
		}
	}
	if len(recommendations) == 0 && blocked[contracts.UpstreamRecommendationGenerationIncomparableCost] == 0 && blocked[contracts.UpstreamRecommendationGenerationNoProvenSavings] == 0 {
		addGeneratorBlock(blocked, contracts.UpstreamRecommendationGenerationNoCallablePair, 1)
	}
	sort.Slice(recommendations, func(i, j int) bool { return recommendations[i].Fingerprint < recommendations[j].Fingerprint })
	return generatorResult(recommendations, blocked), nil
}

func validGeneratorEnvelope(input GeneratorInputs) bool {
	if input.UserID <= 0 || input.GeneratedAt.IsZero() || input.RecommendationTTL < 0 || input.RecommendationTTL > MaximumRecommendationTTL ||
		!input.QualityReferenceTime.IsZero() && input.QualityReferenceTime.After(input.GeneratedAt) {
		return false
	}
	counts := []int{len(input.Sources), len(input.LatestRuns), len(input.Wallets), len(input.Offers), len(input.Links), len(input.LinkResolutions), len(input.QualitySnapshots), len(input.CostFacts), len(input.RoutePlans), len(input.AllocatedChannels), len(input.Bindings)}
	for _, count := range counts {
		if count > maximumGeneratorCollectionSize {
			return false
		}
	}
	for _, value := range input.Sources {
		if value.UserID != input.UserID {
			return false
		}
	}
	for _, value := range input.LatestRuns {
		if value.UserID != input.UserID {
			return false
		}
	}
	for _, value := range input.Wallets {
		if value.UserID != input.UserID {
			return false
		}
	}
	for _, value := range input.Offers {
		if value.UserID != input.UserID {
			return false
		}
	}
	for _, value := range input.Links {
		if value.UserID != input.UserID {
			return false
		}
	}
	for _, value := range input.LinkResolutions {
		if value.UserID != input.UserID {
			return false
		}
	}
	for _, value := range input.CostFacts {
		if value.UserID != input.UserID {
			return false
		}
	}
	for _, value := range input.RoutePlans {
		if value.UserID != input.UserID {
			return false
		}
	}
	offerIDs := make(map[string]struct{}, len(input.Offers))
	offerKeys := make(map[string]struct{}, len(input.Offers))
	for _, value := range input.Offers {
		key := generatorOfferKey(value)
		if value.ID == "" {
			return false
		}
		if _, duplicate := offerIDs[value.ID]; duplicate {
			return false
		}
		if _, duplicate := offerKeys[key]; duplicate {
			return false
		}
		offerIDs[value.ID], offerKeys[key] = struct{}{}, struct{}{}
	}
	return true
}

func singlePublishedGeneratorPlan(input GeneratorInputs) (contracts.RoutePlan, bool) {
	var selected contracts.RoutePlan
	count := 0
	for _, plan := range input.RoutePlans {
		if plan.Status != contracts.RoutePlanPublished {
			continue
		}
		count++
		selected = plan
	}
	return selected, count == 1 && selected.ID != "" && selected.InstanceID != "" && selected.PoolID != "" && selected.SchedulingGeneration > 0
}

func buildGeneratorIndex(input GeneratorInputs, plan contracts.RoutePlan) (generatorIndex, error) {
	index := generatorIndex{
		input: input, plan: plan,
		sources: make(map[string]contracts.UpstreamIntelligenceSource), runs: make(map[string]contracts.UpstreamCollectionRun),
		wallets: make(map[string]contracts.UpstreamWalletObservation), links: make(map[string]contracts.UpstreamIntelligenceLink),
		resolutions: make(map[string]GeneratorLinkResolution), channels: make(map[string]contracts.UpstreamChannel),
		bindings: make(map[string]contracts.PublishedBinding),
	}
	for _, source := range input.Sources {
		if source.ID == "" {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		if _, duplicate := index.sources[source.ID]; duplicate {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		index.sources[source.ID] = source
	}
	for _, run := range input.LatestRuns {
		if run.ID == "" || run.SourceID == "" {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		if _, duplicate := index.runs[run.SourceID]; duplicate {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		index.runs[run.SourceID] = run
	}
	for _, wallet := range input.Wallets {
		if wallet.ID == "" || wallet.SourceID == "" {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		if _, duplicate := index.wallets[wallet.SourceID]; duplicate {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		index.wallets[wallet.SourceID] = wallet
	}
	for _, link := range input.Links {
		if link.ID == "" {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		if _, duplicate := index.links[link.ID]; duplicate {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		index.links[link.ID] = link
	}
	for _, resolution := range input.LinkResolutions {
		if resolution.LinkID == "" || resolution.ChannelID == "" {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		if _, duplicate := index.resolutions[resolution.LinkID]; duplicate {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		index.resolutions[resolution.LinkID] = resolution
	}
	for _, channel := range input.AllocatedChannels {
		if channel.ID == "" {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		if _, duplicate := index.channels[channel.ID]; duplicate {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		index.channels[channel.ID] = channel
	}
	for _, binding := range input.Bindings {
		if binding.ID == "" || binding.PlanID != plan.ID || binding.InstanceID != plan.InstanceID || binding.ChannelID == "" {
			continue
		}
		if _, duplicate := index.bindings[binding.ChannelID]; duplicate {
			return generatorIndex{}, ErrInvalidGeneratorInput
		}
		index.bindings[binding.ChannelID] = binding
	}
	return index, nil
}

func (index generatorIndex) lanesForOffer(offer contracts.UpstreamOfferObservation) ([]generatorLane, []contracts.UpstreamRecommendationGenerationReason) {
	reasons := make([]contracts.UpstreamRecommendationGenerationReason, 0, 4)
	add := func(reason contracts.UpstreamRecommendationGenerationReason) { reasons = append(reasons, reason) }
	if offer.ID == "" || offer.RunID == "" || offer.SourceID == "" || offer.GroupKey == "" || offer.ModelKey == "" ||
		!validPriceDimension(offer.PriceDimension) || offer.PerTokens <= 0 || offer.EffectiveUnitCost == nil ||
		!validCurrency(offer.SettlementCurrency) || offer.FormulaVersion != effectiveCostFormulaVersionV1 ||
		offer.Coverage != contracts.UpstreamCoverageComplete ||
		(offer.Accuracy != contracts.UpstreamEvidenceExact && offer.Accuracy != contracts.UpstreamEvidenceDerived) ||
		len(offer.MissingFields) != 0 || offer.ReasonCode != "" {
		add(contracts.UpstreamRecommendationGenerationMissingCost)
		return nil, reasons
	}
	run, runOK := index.runs[offer.SourceID]
	if !runOK || run.ID != offer.RunID || run.Status != contracts.UpstreamCollectionSucceeded || run.Coverage != contracts.UpstreamCoverageComplete ||
		run.FinalizedFactVersion <= 0 || run.FinalizedFactVersion > index.input.IntelligenceFactVersion || run.CompletedAt == nil ||
		run.ObservedAt.After(index.input.GeneratedAt) || run.CompletedAt.After(index.input.GeneratedAt) ||
		offer.ObservedAt.After(index.input.GeneratedAt) || offer.EffectiveAt.After(index.input.GeneratedAt) ||
		!index.input.GeneratedAt.Before(offer.FreshUntil) || offer.ValidUntil != nil && !index.input.GeneratedAt.Before(*offer.ValidUntil) {
		add(contracts.UpstreamRecommendationGenerationStalePrice)
		return nil, reasons
	}
	source, sourceOK := index.sources[offer.SourceID]
	if !sourceOK || source.Status != contracts.UpstreamSourceActive {
		add(contracts.UpstreamRecommendationGenerationNoCallablePair)
		return nil, reasons
	}
	wallet, walletOK := index.wallets[offer.SourceID]
	if !walletOK || !generatorWalletPasses(wallet, offer, run, index.input.GeneratedAt) {
		add(contracts.UpstreamRecommendationGenerationInsufficientBalance)
		return nil, reasons
	}

	matched := make([]generatorLane, 0, 1)
	for _, link := range index.input.Links {
		if link.IntelligenceSourceID != offer.SourceID || link.Status != contracts.UpstreamLinkActive || link.VerifiedAt == nil || link.VerifiedAt.IsZero() ||
			link.VerifiedAt.After(index.input.GeneratedAt) || link.PriceDimension != offer.PriceDimension {
			continue
		}
		resolution, ok := index.resolutions[link.ID]
		if !ok || !resolution.TargetVerified || resolution.UserID != index.input.UserID {
			continue
		}
		if link.Scope != contracts.UpstreamLinkChannel && link.Scope != contracts.UpstreamLinkSourceIdentity ||
			link.Scope == contracts.UpstreamLinkChannel && link.ChannelID != resolution.ChannelID {
			continue
		}
		channel, ok := index.channels[resolution.ChannelID]
		if !ok || channel.PoolID != index.plan.PoolID || channel.Status != contracts.UpstreamChannelActive || !channel.IsInventoryReady() ||
			!generatorStringSetAllows(channel.Models, offer.ModelKey) || !generatorStringSetAllows(channel.Groups, offer.GroupKey) {
			continue
		}
		binding, ok := index.bindings[channel.ID]
		if !ok || !binding.IsCallable() || binding.SchedulingGeneration != index.plan.SchedulingGeneration {
			continue
		}
		qualityAt := index.input.QualityReferenceTime
		if qualityAt.IsZero() {
			qualityAt = index.input.GeneratedAt
		}
		quality, ok := generatorQuality(index.input.QualitySnapshots, qualityAt, channel.ID, index.plan.InstanceID, offer.ModelKey)
		if !ok {
			add(contracts.UpstreamRecommendationGenerationInsufficientQuality)
			continue
		}
		cost, ok := generatorCost(index.input.CostFacts, index.input.CostLedgerFactVersion, index.input.GeneratedAt, index.input.UserID, channel.ID, index.plan.InstanceID, offer)
		if !ok {
			add(contracts.UpstreamRecommendationGenerationMissingCost)
			continue
		}
		matched = append(matched, generatorLane{source: source, run: run, offer: offer, wallet: wallet, link: link, channel: channel, binding: binding, quality: quality, cost: cost})
	}
	if len(matched) == 0 && len(reasons) == 0 {
		add(contracts.UpstreamRecommendationGenerationMissingLink)
		add(contracts.UpstreamRecommendationGenerationNoCallablePair)
	}
	return matched, reasons
}

func generatorWalletPasses(wallet contracts.UpstreamWalletObservation, offer contracts.UpstreamOfferObservation, run contracts.UpstreamCollectionRun, now time.Time) bool {
	if wallet.RunID != run.ID || wallet.SourceID != offer.SourceID || wallet.BalanceAmount == nil || wallet.Currency != offer.SettlementCurrency ||
		wallet.UnitKind != contracts.UpstreamWalletFiat || wallet.Coverage != contracts.UpstreamCoverageComplete ||
		(wallet.Accuracy != contracts.UpstreamEvidenceExact && wallet.Accuracy != contracts.UpstreamEvidenceDerived) ||
		wallet.ObservedAt.After(now) || !now.Before(wallet.FreshUntil) || len(wallet.MissingFields) != 0 || wallet.ReasonCode != "" {
		return false
	}
	value, err := wallet.BalanceAmount.Rat()
	return err == nil && value.Sign() > 0
}

func generatorQuality(values []contracts.ChannelHealthSnapshot, now time.Time, channelID, instanceID, model string) (contracts.ChannelHealthSnapshot, bool) {
	var found contracts.ChannelHealthSnapshot
	count := 0
	for _, value := range values {
		if value.ChannelID != channelID || value.InstanceID != instanceID || value.Model != model || value.Window != contracts.Window5m {
			continue
		}
		count++
		found = value
	}
	if count != 1 || found.ID == "" || found.CreatedAt.IsZero() || found.CreatedAt.After(now) || now.Sub(found.CreatedAt) > recommendationQualityFreshness ||
		found.QualitySampleCount < recommendationMinimumQualityCount || found.HealthState != contracts.HealthHealthy ||
		found.QualitySuccessRate < recommendationSuccessFloor || found.TTFTP95 > recommendationMaximumTTFTP95MS || found.DurationP95 > recommendationMaximumDurationP95 ||
		found.AuthFailureCount > 0 || found.InsufficientBalanceCount > 0 || !finiteGeneratorMetric(found.QualitySuccessRate) ||
		!finiteGeneratorMetric(found.TTFTP95) || !finiteGeneratorMetric(found.DurationP95) || !finiteGeneratorMetric(found.QualityScore) {
		return contracts.ChannelHealthSnapshot{}, false
	}
	return found, true
}

func generatorCost(values []contracts.UpstreamCostFact, factVersion int64, now time.Time, userID int64, channelID, instanceID string, offer contracts.UpstreamOfferObservation) (contracts.UpstreamCostFact, bool) {
	var found contracts.UpstreamCostFact
	foundAny := false
	for _, value := range values {
		if value.UserID != userID || value.FactVersion <= 0 || value.FactVersion > factVersion || value.ChannelID != channelID || value.InstanceID != instanceID ||
			value.IntelligenceSourceID != offer.SourceID || value.ModelKey != offer.ModelKey || value.GroupKey != offer.GroupKey ||
			value.PriceDimension != offer.PriceDimension || value.PerTokens != offer.PerTokens || value.Currency != offer.SettlementCurrency ||
			value.PriceObservationID != offer.ID || value.PriceStatus != contracts.UpstreamCostPriceValid ||
			(value.Attribution != contracts.UpstreamCostExact && value.Attribution != contracts.UpstreamCostDerived) ||
			value.UnitCost == nil || value.Amount == nil || value.Quantity == nil || value.PriceEffectiveAt == nil ||
			value.CalculationVersion != contracts.UpstreamCostCalculationVersionV1 || value.ReasonCode != "" || len(value.MissingFields) != 0 ||
			value.OccurredAt.After(now) || value.PriceValidUntil != nil && !now.Before(*value.PriceValidUntil) {
			continue
		}
		if offer.Accuracy == contracts.UpstreamEvidenceExact && value.Attribution != contracts.UpstreamCostExact ||
			offer.Accuracy == contracts.UpstreamEvidenceDerived && value.Attribution != contracts.UpstreamCostDerived {
			continue
		}
		if !foundAny || value.OccurredAt.After(found.OccurredAt) || value.OccurredAt.Equal(found.OccurredAt) && (value.FactVersion > found.FactVersion || value.FactVersion == found.FactVersion && value.ID > found.ID) {
			found, foundAny = value, true
		}
	}
	return found, foundAny && found.ID != "" && *found.UnitCost == *offer.EffectiveUnitCost && found.PriceEffectiveAt.Equal(offer.EffectiveAt)
}

func generatorCandidate(input GeneratorInputs, plan contracts.RoutePlan, from, to generatorLane, ttl time.Duration) (contracts.UpstreamRecommendationCandidate, error) {
	fromCost, err := generatorCostRange(*from.offer.EffectiveUnitCost, from.offer.Accuracy)
	if err != nil {
		return contracts.UpstreamRecommendationCandidate{}, err
	}
	toCost, err := generatorCostRange(*to.offer.EffectiveUnitCost, to.offer.Accuracy)
	if err != nil {
		return contracts.UpstreamRecommendationCandidate{}, err
	}
	constraints := []contracts.UpstreamRecommendationConstraint{
		{Kind: contracts.UpstreamRecommendationConstraintQuality, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{from.quality.ID, to.quality.ID}},
		{Kind: contracts.UpstreamRecommendationConstraintCapacity, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{from.binding.ID, to.binding.ID, from.link.ID, to.link.ID}},
		{Kind: contracts.UpstreamRecommendationConstraintBalance, Status: contracts.UpstreamRecommendationConstraintPassed, EvidenceIDs: []string{from.wallet.ID, to.wallet.ID}},
	}
	evidence := []string{
		from.offer.ID, from.cost.ID, from.quality.ID, from.wallet.ID, from.link.ID, from.binding.ID,
		to.offer.ID, to.cost.ID, to.quality.ID, to.wallet.ID, to.link.ID, to.binding.ID,
	}
	return contracts.UpstreamRecommendationCandidate{
		UserID: input.UserID, IntelligenceFactVersion: input.IntelligenceFactVersion, CostLedgerFactVersion: input.CostLedgerFactVersion,
		LinkFactVersion: input.IntelligenceFactVersion, PlanGeneration: plan.SchedulingGeneration,
		FromSourceID: from.source.ID, FromChannelID: from.channel.ID, FromGroupKey: from.offer.GroupKey,
		ToSourceID: to.source.ID, ToChannelID: to.channel.ID, ToGroupKey: to.offer.GroupKey, ModelKey: from.offer.ModelKey,
		PriceDimension: from.offer.PriceDimension, SettlementCurrency: from.offer.SettlementCurrency, PerTokens: from.offer.PerTokens,
		AffectedPlanIDs: []string{plan.ID}, AffectedDownstreams: []string{plan.InstanceID}, EvidenceIDs: evidence,
		Constraints: constraints, FromCost: fromCost, ToCost: toCost,
		FormulaVersion: contracts.UpstreamRecommendationFormulaVersionV1, StrategyVersion: contracts.UpstreamRecommendationStrategyVersionV1,
		CreatedAt: input.GeneratedAt, ExpiresAt: input.GeneratedAt.Add(ttl),
	}, nil
}

func generatorCostRange(value contracts.CanonicalDecimal, accuracy contracts.UpstreamEvidenceAccuracy) (contracts.UpstreamRecommendationCostRange, error) {
	parsed, err := value.Rat()
	if err != nil || parsed.Sign() < 0 {
		return contracts.UpstreamRecommendationCostRange{}, ErrInvalidGeneratorInput
	}
	if accuracy != contracts.UpstreamEvidenceExact && accuracy != contracts.UpstreamEvidenceDerived {
		return contracts.UpstreamRecommendationCostRange{}, ErrInvalidGeneratorInput
	}
	// Derived means a deterministic, complete Core formula, not an estimate.
	// Do not invent an uncertainty band that is absent from the evidence.
	return contracts.UpstreamRecommendationCostRange{Lower: value, Expected: value, Upper: value}, nil
}

func generatorLanesComparable(left, right generatorLane) bool {
	return left.offer.ModelKey == right.offer.ModelKey && left.offer.PriceDimension == right.offer.PriceDimension &&
		left.offer.SettlementCurrency == right.offer.SettlementCurrency && left.offer.PerTokens == right.offer.PerTokens
}

func compareGeneratorCosts(left, right contracts.UpstreamOfferObservation) (int, error) {
	leftRange, err := generatorCostRange(*left.EffectiveUnitCost, left.Accuracy)
	if err != nil {
		return 0, err
	}
	rightRange, err := generatorCostRange(*right.EffectiveUnitCost, right.Accuracy)
	if err != nil {
		return 0, err
	}
	leftLower, _ := leftRange.Lower.Rat()
	leftUpper, _ := leftRange.Upper.Rat()
	rightLower, _ := rightRange.Lower.Rat()
	rightUpper, _ := rightRange.Upper.Rat()
	if leftLower.Cmp(rightUpper) > 0 {
		return 1, nil
	}
	if rightLower.Cmp(leftUpper) > 0 {
		return -1, nil
	}
	return 0, nil
}

func generatorResult(recommendations []contracts.UpstreamRecommendation, blocked map[contracts.UpstreamRecommendationGenerationReason]int) contracts.UpstreamRecommendationGenerationResult {
	diagnostics := make([]contracts.UpstreamRecommendationGenerationDiagnostic, 0, len(blocked))
	for reason, count := range blocked {
		if count > 0 && contracts.IsUpstreamRecommendationGenerationReason(reason) {
			diagnostics = append(diagnostics, contracts.UpstreamRecommendationGenerationDiagnostic{Reason: reason, Count: count})
		}
	}
	sort.Slice(diagnostics, func(i, j int) bool { return diagnostics[i].Reason < diagnostics[j].Reason })
	return contracts.UpstreamRecommendationGenerationResult{Recommendations: recommendations, Blocked: diagnostics}
}

func addGeneratorBlock(values map[contracts.UpstreamRecommendationGenerationReason]int, reason contracts.UpstreamRecommendationGenerationReason, count int) {
	if count > 0 && contracts.IsUpstreamRecommendationGenerationReason(reason) {
		values[reason] += count
	}
}

func generatorStringSetAllows(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func generatorOfferKey(value contracts.UpstreamOfferObservation) string {
	return strings.Join([]string{value.SourceID, value.GroupKey, value.ModelKey, string(value.PriceDimension), value.ID}, "\x00")
}

func generatorLaneKey(value generatorLane) string {
	return strings.Join([]string{generatorOfferKey(value.offer), value.channel.ID, value.binding.ID, value.link.ID}, "\x00")
}

func finiteGeneratorMetric(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func normalizeGeneratorTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

func validOpaqueRecommendationID(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 128 || !utf8.ValidString(value) || contracts.LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) || character == '/' || character == '\\' {
			return false
		}
	}
	return true
}

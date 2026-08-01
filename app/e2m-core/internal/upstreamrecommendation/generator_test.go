package upstreamrecommendation

import (
	"errors"
	"math"
	"reflect"
	"sort"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestGenerateBuildsEvidenceClosedStableRecommendation(t *testing.T) {
	if DefaultRecommendationTTL != time.Hour {
		t.Fatalf("default recommendation TTL=%s, want 1h", DefaultRecommendationTTL)
	}
	input := generatorFixture()
	result, err := Generate(input, sequenceRecommendationIDs("rec-opaque-a"))
	if err != nil || len(result.Recommendations) != 1 {
		t.Fatalf("recommendations=%+v blocked=%+v err=%v", result.Recommendations, result.Blocked, err)
	}
	got := result.Recommendations[0]
	if got.ID != "rec-opaque-a" || got.FromSourceID != "source-expensive" || got.ToSourceID != "source-cheap" ||
		got.FromChannelID != "channel-expensive" || got.ToChannelID != "channel-cheap" || got.LinkFactVersion != input.IntelligenceFactVersion ||
		got.PlanGeneration != 19 || !got.ExpiresAt.Equal(input.GeneratedAt.Add(DefaultRecommendationTTL)) {
		t.Fatalf("unexpected recommendation: %+v", got)
	}
	wantEvidence := []string{
		"binding-cheap", "binding-expensive", "cost-cheap", "cost-expensive", "link-cheap", "link-expensive",
		"offer-cheap", "offer-expensive", "quality-cheap", "quality-expensive", "wallet-cheap", "wallet-expensive",
	}
	actual := append([]string(nil), got.EvidenceIDs...)
	sort.Strings(actual)
	if !reflect.DeepEqual(actual, wantEvidence) {
		t.Fatalf("evidence=%v want=%v", actual, wantEvidence)
	}
	if len(got.Constraints) != 3 || got.Savings.AmountLower != "4" || got.Savings.PercentLower != "0.4" {
		t.Fatalf("constraints/savings mismatch: %+v %+v", got.Constraints, got.Savings)
	}
	if err := Validate(got); err != nil {
		t.Fatalf("generated recommendation does not validate: %v", err)
	}

	reordered := cloneGeneratorFixture(input)
	reverseGeneratorInputs(&reordered)
	second, err := Generate(reordered, sequenceRecommendationIDs("different-opaque-id"))
	if err != nil || len(second.Recommendations) != 1 {
		t.Fatalf("reordered result=%+v err=%v", second, err)
	}
	if got.Fingerprint != second.Recommendations[0].Fingerprint || got.ID == second.Recommendations[0].ID {
		t.Fatalf("fingerprint/id stability mismatch: first=%+v second=%+v", got, second.Recommendations[0])
	}
}

func TestGenerateFailsClosedAtEveryLaneGate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GeneratorInputs)
		want   contracts.UpstreamRecommendationGenerationReason
	}{
		{"unfinalized run", func(v *GeneratorInputs) { v.LatestRuns[1].FinalizedFactVersion = 0 }, contracts.UpstreamRecommendationGenerationStalePrice},
		{"partial run", func(v *GeneratorInputs) {
			v.LatestRuns[1].Status, v.LatestRuns[1].Coverage = contracts.UpstreamCollectionPartial, contracts.UpstreamCoveragePartial
		}, contracts.UpstreamRecommendationGenerationStalePrice},
		{"stale offer", func(v *GeneratorInputs) { v.Offers[1].FreshUntil = v.GeneratedAt }, contracts.UpstreamRecommendationGenerationStalePrice},
		{"estimated offer", func(v *GeneratorInputs) { v.Offers[1].Accuracy = contracts.UpstreamEvidenceEstimated }, contracts.UpstreamRecommendationGenerationMissingCost},
		{"missing current cost", func(v *GeneratorInputs) { v.CostFacts[1].PriceObservationID = "other" }, contracts.UpstreamRecommendationGenerationMissingCost},
		{"estimated cost", func(v *GeneratorInputs) { v.CostFacts[1].Attribution = contracts.UpstreamCostEstimated }, contracts.UpstreamRecommendationGenerationMissingCost},
		{"stale quality", func(v *GeneratorInputs) {
			v.QualitySnapshots[1].CreatedAt = v.GeneratedAt.Add(-5*time.Minute - time.Microsecond)
		}, contracts.UpstreamRecommendationGenerationInsufficientQuality},
		{"low samples", func(v *GeneratorInputs) { v.QualitySnapshots[1].QualitySampleCount = 4 }, contracts.UpstreamRecommendationGenerationInsufficientQuality},
		{"quality floor", func(v *GeneratorInputs) { v.QualitySnapshots[1].QualitySuccessRate = .94 }, contracts.UpstreamRecommendationGenerationInsufficientQuality},
		{"hard auth failure", func(v *GeneratorInputs) { v.QualitySnapshots[1].AuthFailureCount = 1 }, contracts.UpstreamRecommendationGenerationInsufficientQuality},
		{"non finite quality", func(v *GeneratorInputs) { v.QualitySnapshots[1].TTFTP95 = math.Inf(1) }, contracts.UpstreamRecommendationGenerationInsufficientQuality},
		{"zero balance", func(v *GeneratorInputs) { zero := contracts.CanonicalDecimal("0"); v.Wallets[1].BalanceAmount = &zero }, contracts.UpstreamRecommendationGenerationInsufficientBalance},
		{"cross currency balance", func(v *GeneratorInputs) { v.Wallets[1].Currency = "CNY" }, contracts.UpstreamRecommendationGenerationInsufficientBalance},
		{"inactive link", func(v *GeneratorInputs) { v.Links[1].Status = contracts.UpstreamLinkInactive }, contracts.UpstreamRecommendationGenerationMissingLink},
		{"unverified resolution", func(v *GeneratorInputs) { v.LinkResolutions[1].TargetVerified = false }, contracts.UpstreamRecommendationGenerationMissingLink},
		{"maintenance channel", func(v *GeneratorInputs) { v.AllocatedChannels[1].Status = contracts.UpstreamChannelMaintenance }, contracts.UpstreamRecommendationGenerationNoCallablePair},
		{"inventory not ready", func(v *GeneratorInputs) { v.AllocatedChannels[1].InventoryState = contracts.UpstreamInventoryDraft }, contracts.UpstreamRecommendationGenerationNoCallablePair},
		{"unsupported model", func(v *GeneratorInputs) { v.AllocatedChannels[1].Models = []string{"other"} }, contracts.UpstreamRecommendationGenerationNoCallablePair},
		{"uncallable binding", func(v *GeneratorInputs) {
			v.Bindings[1].VerificationStatus = contracts.BindingVerificationPublishedPending
		}, contracts.UpstreamRecommendationGenerationNoCallablePair},
		{"stale generation binding", func(v *GeneratorInputs) { v.Bindings[1].SchedulingGeneration-- }, contracts.UpstreamRecommendationGenerationNoCallablePair},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := generatorFixture()
			tt.mutate(&input)
			result, err := Generate(input, sequenceRecommendationIDs("should-not-be-used"))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Recommendations) != 0 || !hasGeneratorReason(result, tt.want) {
				t.Fatalf("recommendations=%+v blocked=%+v want reason=%s", result.Recommendations, result.Blocked, tt.want)
			}
			for _, diagnostic := range result.Blocked {
				if !contracts.IsUpstreamRecommendationGenerationReason(diagnostic.Reason) || diagnostic.Count <= 0 {
					t.Fatalf("unsafe diagnostic: %+v", diagnostic)
				}
			}
		})
	}
}

func TestGenerateCanRevalidateHistoricalQualityWithoutBackdatingOtherFacts(t *testing.T) {
	input := generatorFixture()
	qualityAt := input.GeneratedAt
	input.GeneratedAt = input.GeneratedAt.Add(10 * time.Minute)
	input.QualityReferenceTime = qualityAt
	for index := range input.Offers {
		input.Offers[index].FreshUntil = input.GeneratedAt.Add(time.Hour)
	}
	for index := range input.Wallets {
		input.Wallets[index].FreshUntil = input.GeneratedAt.Add(time.Hour)
	}
	result, err := Generate(input, sequenceRecommendationIDs("historical-quality"))
	if err != nil || len(result.Recommendations) != 1 {
		t.Fatalf("historical quality result=%+v err=%v", result, err)
	}

	input.QualityReferenceTime = input.GeneratedAt.Add(time.Second)
	if _, err := Generate(input, sequenceRecommendationIDs("future-quality")); !errors.Is(err, ErrInvalidGeneratorInput) {
		t.Fatalf("future quality reference err=%v", err)
	}
}

func TestGenerateRequiresOnePublishedPlanComparableUnitsAndOpaqueUniqueIDs(t *testing.T) {
	t.Run("no plan", func(t *testing.T) {
		input := generatorFixture()
		input.RoutePlans[0].Status = contracts.RoutePlanDraft
		result, err := Generate(input, sequenceRecommendationIDs("unused"))
		if err != nil || len(result.Recommendations) != 0 || !hasGeneratorReason(result, contracts.UpstreamRecommendationGenerationNoPublishedPlan) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("ambiguous published plans", func(t *testing.T) {
		input := generatorFixture()
		other := input.RoutePlans[0]
		other.ID = "plan-other"
		input.RoutePlans = append(input.RoutePlans, other)
		result, err := Generate(input, sequenceRecommendationIDs("unused"))
		if err != nil || len(result.Recommendations) != 0 || !hasGeneratorReason(result, contracts.UpstreamRecommendationGenerationNoPublishedPlan) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("currency mismatch", func(t *testing.T) {
		input := generatorFixture()
		input.Offers[1].SettlementCurrency, input.Wallets[1].Currency, input.CostFacts[1].Currency = "CNY", "CNY", "CNY"
		result, err := Generate(input, sequenceRecommendationIDs("unused"))
		if err != nil || len(result.Recommendations) != 0 || !hasGeneratorReason(result, contracts.UpstreamRecommendationGenerationIncomparableCost) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	t.Run("overlapping derived interval is not invented", func(t *testing.T) {
		input := generatorFixture()
		cheap := contracts.CanonicalDecimal("10")
		input.Offers[1].EffectiveUnitCost, input.CostFacts[1].UnitCost = &cheap, &cheap
		result, err := Generate(input, sequenceRecommendationIDs("unused"))
		if err != nil || len(result.Recommendations) != 0 || !hasGeneratorReason(result, contracts.UpstreamRecommendationGenerationNoProvenSavings) {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
	for _, id := range []string{"", " derived-owner-42 ", "https://secret.example", "line\nbreak"} {
		t.Run("invalid id "+id, func(t *testing.T) {
			_, err := Generate(generatorFixture(), sequenceRecommendationIDs(id))
			if !errors.Is(err, ErrInvalidGeneratorInput) {
				t.Fatalf("id=%q err=%v", id, err)
			}
		})
	}
}

func TestGenerateRejectsCrossOwnerAndDoesNotConsumeIDWhenBlocked(t *testing.T) {
	input := generatorFixture()
	input.Offers[1].UserID++
	if _, err := Generate(input, sequenceRecommendationIDs("unused")); !errors.Is(err, ErrInvalidGeneratorInput) {
		t.Fatalf("cross-owner error=%v", err)
	}
	input = generatorFixture()
	input.Wallets[1].BalanceAmount = nil
	calls := 0
	result, err := Generate(input, func() string { calls++; return "unused" })
	if err != nil || len(result.Recommendations) != 0 || calls != 0 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
}

func generatorFixture() GeneratorInputs {
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	verified := now.Add(-time.Hour)
	completed := now.Add(-2 * time.Minute)
	prices := []contracts.CanonicalDecimal{"10", "6"}
	sourceIDs := []string{"source-expensive", "source-cheap"}
	channelIDs := []string{"channel-expensive", "channel-cheap"}
	input := GeneratorInputs{
		UserID: 42, GeneratedAt: now, IntelligenceFactVersion: 7, CostLedgerFactVersion: 9,
		RoutePlans: []contracts.RoutePlan{{ID: "plan-1", UserID: 42, InstanceID: "instance-1", PoolID: "pool-1", Status: contracts.RoutePlanPublished, SchedulingGeneration: 19}},
	}
	for index := range sourceIDs {
		suffix := "expensive"
		if index == 1 {
			suffix = "cheap"
		}
		runID := "run-" + suffix
		offerID := "offer-" + suffix
		input.Sources = append(input.Sources, contracts.UpstreamIntelligenceSource{ID: sourceIDs[index], UserID: 42, Status: contracts.UpstreamSourceActive})
		input.LatestRuns = append(input.LatestRuns, contracts.UpstreamCollectionRun{
			ID: runID, UserID: 42, SourceID: sourceIDs[index], Status: contracts.UpstreamCollectionSucceeded,
			Coverage: contracts.UpstreamCoverageComplete, ObservedAt: now.Add(-3 * time.Minute), CompletedAt: &completed, FinalizedFactVersion: int64(6 + index),
		})
		price := prices[index]
		balance := contracts.CanonicalDecimal("100")
		input.Offers = append(input.Offers, contracts.UpstreamOfferObservation{
			ID: offerID, RunID: runID, UserID: 42, SourceID: sourceIDs[index], GroupKey: "paid", ModelKey: "gpt-test",
			PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
			EffectiveUnitCost: &price, FormulaVersion: effectiveCostFormulaVersionV1, Accuracy: contracts.UpstreamEvidenceExact,
			Coverage: contracts.UpstreamCoverageComplete, ObservedAt: now.Add(-3 * time.Minute), EffectiveAt: now.Add(-time.Hour), FreshUntil: now.Add(time.Hour),
		})
		input.Wallets = append(input.Wallets, contracts.UpstreamWalletObservation{
			ID: "wallet-" + suffix, RunID: runID, UserID: 42, SourceID: sourceIDs[index], BalanceAmount: &balance,
			UnitKind: contracts.UpstreamWalletFiat, Currency: "USD", Accuracy: contracts.UpstreamEvidenceExact,
			Coverage: contracts.UpstreamCoverageComplete, ObservedAt: now.Add(-3 * time.Minute), FreshUntil: now.Add(time.Hour),
		})
		input.Links = append(input.Links, contracts.UpstreamIntelligenceLink{
			ID: "link-" + suffix, UserID: 42, IntelligenceSourceID: sourceIDs[index], Scope: contracts.UpstreamLinkChannel,
			ChannelID: channelIDs[index], PriceDimension: contracts.UpstreamPriceInput, Status: contracts.UpstreamLinkActive, VerifiedAt: &verified,
		})
		input.LinkResolutions = append(input.LinkResolutions, GeneratorLinkResolution{LinkID: "link-" + suffix, UserID: 42, ChannelID: channelIDs[index], TargetVerified: true})
		input.AllocatedChannels = append(input.AllocatedChannels, contracts.UpstreamChannel{
			ID: channelIDs[index], PoolID: "pool-1", Models: []string{"gpt-test"}, Groups: []string{"paid"}, Status: contracts.UpstreamChannelActive, InventoryState: contracts.UpstreamInventoryReady,
		})
		input.Bindings = append(input.Bindings, contracts.PublishedBinding{
			ID: "binding-" + suffix, PlanID: "plan-1", InstanceID: "instance-1", ChannelID: channelIDs[index], State: contracts.BindingActive,
			VerificationStatus: contracts.BindingVerificationPassiveVerified, SchedulingGeneration: 19,
		})
		input.QualitySnapshots = append(input.QualitySnapshots, contracts.ChannelHealthSnapshot{
			ID: "quality-" + suffix, ChannelID: channelIDs[index], InstanceID: "instance-1", Model: "gpt-test", Window: contracts.Window5m,
			QualitySampleCount: 20, QualitySuccessRate: .99, TTFTP95: 500, DurationP95: 1200, QualityScore: 95,
			HealthState: contracts.HealthHealthy, CreatedAt: now.Add(-time.Minute),
		})
		quantity := int64(1_000_000)
		amount := price
		priceAt := now.Add(-time.Hour)
		input.CostFacts = append(input.CostFacts, contracts.UpstreamCostFact{
			ID: "cost-" + suffix, UserID: 42, FactVersion: 9, UsageObservationID: "usage-" + suffix,
			ChannelID: channelIDs[index], InstanceID: "instance-1", IntelligenceSourceID: sourceIDs[index], ModelKey: "gpt-test", GroupKey: "paid",
			PriceDimension: contracts.UpstreamPriceInput, Quantity: &quantity, PerTokens: 1_000_000, PriceObservationID: offerID,
			PriceEffectiveAt: &priceAt, UnitCost: &price, Amount: &amount, Currency: "USD", Attribution: contracts.UpstreamCostExact,
			PriceStatus: contracts.UpstreamCostPriceValid, CalculationVersion: contracts.UpstreamCostCalculationVersionV1, OccurredAt: now.Add(-30 * time.Second),
		})
	}
	return input
}

func sequenceRecommendationIDs(values ...string) RecommendationIDFactory {
	index := 0
	return func() string {
		if index >= len(values) {
			return "duplicate-id"
		}
		value := values[index]
		index++
		return value
	}
}

func hasGeneratorReason(result contracts.UpstreamRecommendationGenerationResult, reason contracts.UpstreamRecommendationGenerationReason) bool {
	for _, diagnostic := range result.Blocked {
		if diagnostic.Reason == reason && diagnostic.Count > 0 {
			return true
		}
	}
	return false
}

func cloneGeneratorFixture(value GeneratorInputs) GeneratorInputs {
	result := value
	result.Sources = append([]contracts.UpstreamIntelligenceSource(nil), value.Sources...)
	result.LatestRuns = append([]contracts.UpstreamCollectionRun(nil), value.LatestRuns...)
	result.Wallets = append([]contracts.UpstreamWalletObservation(nil), value.Wallets...)
	result.Offers = append([]contracts.UpstreamOfferObservation(nil), value.Offers...)
	result.Links = append([]contracts.UpstreamIntelligenceLink(nil), value.Links...)
	result.LinkResolutions = append([]GeneratorLinkResolution(nil), value.LinkResolutions...)
	result.QualitySnapshots = append([]contracts.ChannelHealthSnapshot(nil), value.QualitySnapshots...)
	result.CostFacts = append([]contracts.UpstreamCostFact(nil), value.CostFacts...)
	result.RoutePlans = append([]contracts.RoutePlan(nil), value.RoutePlans...)
	result.AllocatedChannels = append([]contracts.UpstreamChannel(nil), value.AllocatedChannels...)
	result.Bindings = append([]contracts.PublishedBinding(nil), value.Bindings...)
	return result
}

func reverseGeneratorInputs(value *GeneratorInputs) {
	reverse := func(slice reflect.Value) {
		for left, right := 0, slice.Len()-1; left < right; left, right = left+1, right-1 {
			leftValue := reflect.New(slice.Index(left).Type()).Elem()
			leftValue.Set(slice.Index(left))
			slice.Index(left).Set(slice.Index(right))
			slice.Index(right).Set(leftValue)
		}
	}
	for _, collection := range []any{&value.Sources, &value.LatestRuns, &value.Wallets, &value.Offers, &value.Links, &value.LinkResolutions, &value.QualitySnapshots, &value.CostFacts, &value.AllocatedChannels, &value.Bindings} {
		reverse(reflect.ValueOf(collection).Elem())
	}
}

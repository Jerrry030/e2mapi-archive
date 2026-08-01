package upstreamcost

import (
	"reflect"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestBuildFactsUsesHistoricalIntervalAndExactDecimal(t *testing.T) {
	occurred := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	input, zero := int64(500_000), int64(0)
	usage := contracts.UpstreamCostUsage{ObservationID: "usage-1", UserID: 42, ChannelID: "ch-1", ModelKey: "model", GroupKey: "group", InputTokens: &input, OutputTokens: &zero, CachedInputTokens: &zero, RequestCount: &zero, OccurredAt: occurred}
	old := evidence(t, occurred.Add(-time.Hour), occurred.Add(time.Hour), "offer-old", "2")
	newPrice := evidence(t, occurred.Add(time.Hour), occurred.Add(3*time.Hour), "offer-new", "99")
	facts, err := BuildFacts(usage, []PriceEvidence{newPrice, old}, contracts.UpstreamCostCalculationVersionV1)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 4 || facts[0].PriceObservationID != "offer-old" || facts[0].Amount == nil || *facts[0].Amount != "1" || facts[0].Attribution != contracts.UpstreamCostExact {
		t.Fatalf("historical price not sealed: %+v", facts)
	}
	replayed, err := BuildFacts(usage, []PriceEvidence{old, newPrice}, contracts.UpstreamCostCalculationVersionV1)
	if err != nil || !reflect.DeepEqual(facts, replayed) {
		t.Fatalf("replay changed: %+v / %+v err=%v", facts, replayed, err)
	}
}

func TestBuildFactsFailsClosedForMissingPresenceAndGroup(t *testing.T) {
	now := time.Now().UTC()
	usage := contracts.UpstreamCostUsage{ObservationID: "usage", UserID: 42, ChannelID: "ch-1", ModelKey: "model", OccurredAt: now}
	facts, err := BuildFacts(usage, nil, "v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range facts {
		if fact.Amount != nil || fact.Attribution != contracts.UpstreamCostUnattributed || fact.ReasonCode != contracts.UpstreamCostReasonMissingGroup {
			t.Fatalf("missing group did not fail closed: %+v", fact)
		}
	}
}

func TestBuildFactsTreatsEndBoundaryAsExpired(t *testing.T) {
	now := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	quantity := int64(1)
	usage := contracts.UpstreamCostUsage{ObservationID: "usage", UserID: 42, ChannelID: "ch-1", ModelKey: "model", GroupKey: "group", InputTokens: &quantity, OccurredAt: now}
	price := evidence(t, now.Add(-time.Hour), now, "offer", "1")
	facts, err := BuildFacts(usage, []PriceEvidence{price}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if facts[0].Amount != nil || facts[0].PriceStatus != contracts.UpstreamCostPriceExpired || facts[0].ReasonCode != contracts.UpstreamCostReasonPriceExpired {
		t.Fatalf("end boundary not expired: %+v", facts[0])
	}
}

func TestBuildFactsFuturePriceIsUnavailableNotExpired(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	quantity := int64(1)
	usage := contracts.UpstreamCostUsage{UserID: 42, InputTokens: &quantity, OccurredAt: now}
	usage.ObservationID = string([]byte{117, 115, 97, 103, 101})
	usage.ChannelID = string([]byte{99, 104, 49})
	usage.ModelKey = string([]byte{109, 111, 100, 101, 108})
	usage.GroupKey = string([]byte{103, 114, 111, 117, 112})
	future := evidence(t, now.Add(time.Hour), now.Add(2*time.Hour), string([]byte{102, 117, 116, 117, 114, 101}), string([]byte{49}))
	facts, err := BuildFacts(usage, []PriceEvidence{future}, string([]byte{118, 49}))
	if err != nil {
		t.Fatal(err)
	}
	if facts[0].PriceStatus != contracts.UpstreamCostPriceUnavailable {
		t.Fail()
	}
}

func TestBuildFactsFreshnessDoesNotTruncateHistoricalPrice(t *testing.T) {
	effectiveAt := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	occurredAt := effectiveAt.Add(2 * time.Hour)
	quantity := int64(1_000_000)
	usage := contracts.UpstreamCostUsage{ObservationID: "usage-stale", UserID: 42, ChannelID: "ch-1", ModelKey: "model", GroupKey: "group", InputTokens: &quantity, OccurredAt: occurredAt}
	price := evidence(t, effectiveAt, effectiveAt.Add(time.Minute), "offer-stale", "2")
	price.Offer.ValidUntil = nil
	facts, err := BuildFacts(usage, []PriceEvidence{price}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if facts[0].Amount == nil || *facts[0].Amount != "2" || facts[0].PriceStatus != contracts.UpstreamCostPriceValid || facts[0].PriceValidUntil != nil {
		t.Fatalf("freshness rewrote historical validity: %+v", facts[0])
	}
}

func TestBuildFactsNextEffectivePriceClosesPreviousInterval(t *testing.T) {
	effectiveAt := time.Date(2026, 7, 25, 1, 0, 0, 0, time.UTC)
	boundary := effectiveAt.Add(time.Hour)
	quantity := int64(1_000_000)
	usage := contracts.UpstreamCostUsage{ObservationID: "usage-boundary", UserID: 42, ChannelID: "ch-1", ModelKey: "model", GroupKey: "group", InputTokens: &quantity, OccurredAt: boundary}
	old := evidence(t, effectiveAt, effectiveAt.Add(time.Minute), "offer-old", "2")
	old.Offer.ValidUntil = nil
	current := evidence(t, boundary, boundary.Add(time.Minute), "offer-current", "3")
	current.Offer.ValidUntil = nil
	facts, err := BuildFacts(usage, []PriceEvidence{current, old}, "v1")
	if err != nil {
		t.Fatal(err)
	}
	if facts[0].PriceObservationID != "offer-current" || facts[0].Amount == nil || *facts[0].Amount != "3" {
		t.Fatalf("half-open interval selected wrong price: %+v", facts[0])
	}
}

func TestBuildFactsRejectsNegativeUsage(t *testing.T) {
	negative := int64(-1)
	_, err := BuildFacts(contracts.UpstreamCostUsage{ObservationID: "usage", UserID: 1, OccurredAt: time.Now(), InputTokens: &negative}, nil, "v1")
	if err == nil {
		t.Fatal("negative quantity accepted")
	}
}

func evidence(t *testing.T, from, until time.Time, id, cost string) PriceEvidence {
	t.Helper()
	value, err := contracts.ParseCanonicalDecimal(cost)
	if err != nil {
		t.Fatal(err)
	}
	return PriceEvidence{OwnerID: 42, ChannelID: "ch-1", IntelligenceSourceID: "source-1", TargetVerified: true, Offer: contracts.UpstreamOfferObservation{
		ID: id, UserID: 42, SourceID: "source-1", GroupKey: "group", ModelKey: "model", PriceDimension: contracts.UpstreamPriceInput,
		SettlementCurrency: "USD", PerTokens: 1_000_000, EffectiveUnitCost: &value, Accuracy: contracts.UpstreamEvidenceExact,
		Coverage: contracts.UpstreamCoverageComplete, EffectiveAt: from, FreshUntil: until, ValidUntil: &until,
	}}
}

package upstreamcost

import (
	"context"
	"errors"
	"testing"
	"time"

	"e2m.local/contracts"
)

type recordingCostFactAppender struct {
	calls int
	facts []contracts.UpstreamCostFact
	err   error
}

func (appender *recordingCostFactAppender) AppendUpstreamCostFacts(_ context.Context, facts []contracts.UpstreamCostFact) ([]contracts.UpstreamCostFact, contracts.UpstreamCostFactVersion, error) {
	appender.calls++
	appender.facts = append([]contracts.UpstreamCostFact(nil), facts...)
	if appender.err != nil {
		return nil, contracts.UpstreamCostFactVersion{}, appender.err
	}
	return facts, contracts.UpstreamCostFactVersion{UserID: facts[0].UserID, FactVersion: 1}, nil
}

func TestUsageFromConnectorObservationUsesOnlyPresenceSafeCostUsage(t *testing.T) {
	now := time.Date(2026, 7, 26, 1, 2, 3, 456789123, time.UTC)
	legacyOnly := contracts.ConnectorChannelObservation{
		ObservationID: "legacy", Model: "gpt", InputTokens: 999, OutputTokens: 888, ObservedAt: now,
	}
	usage, err := UsageFromConnectorObservation(42, "inst", "channel", "core-legacy", legacyOnly)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != nil || usage.OutputTokens != nil || usage.CachedInputTokens != nil || usage.RequestCount != nil || usage.GroupKey != "" {
		t.Fatalf("legacy quality fields became financial facts: %+v", usage)
	}
	zero, requests := int64(0), int64(1)
	group := "paid"
	presenceSafe := legacyOnly
	presenceSafe.ObservationID = "safe"
	presenceSafe.CostUsage = &contracts.ConnectorCostUsage{InputTokens: &zero, RequestCount: &requests, GroupKey: &group}
	usage, err = UsageFromConnectorObservation(42, "inst", "channel", "core-safe", presenceSafe)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 0 || usage.OutputTokens != nil || usage.RequestCount == nil || *usage.RequestCount != 1 || usage.GroupKey != "paid" {
		t.Fatalf("presence semantics lost: %+v", usage)
	}
	zero = 7
	group = "mutated"
	if *usage.InputTokens != 0 || usage.GroupKey != "paid" {
		t.Fatal("usage aliases Connector-owned pointer fields")
	}
}

func TestUsageFromConnectorObservationRejectsNegativeOrSensitiveCostUsage(t *testing.T) {
	negative := int64(-1)
	group := "https://secret.example"
	base := contracts.ConnectorChannelObservation{ObservationID: "usage", Model: "gpt", ObservedAt: time.Now().UTC()}
	base.CostUsage = &contracts.ConnectorCostUsage{InputTokens: &negative}
	if _, err := UsageFromConnectorObservation(42, "inst", "channel", "core-usage", base); !errors.Is(err, ErrInvalidUsageObservation) {
		t.Fatalf("negative quantity error=%v", err)
	}
	base.CostUsage = &contracts.ConnectorCostUsage{GroupKey: &group}
	if _, err := UsageFromConnectorObservation(42, "inst", "channel", "core-usage", base); !errors.Is(err, ErrInvalidUsageObservation) {
		t.Fatalf("sensitive group error=%v", err)
	}
}

func TestResolvePriceEvidenceRequiresExplicitVerifiedOwnerChannelLink(t *testing.T) {
	now := time.Now().UTC()
	verifiedAt := now.Add(-time.Hour)
	offers := []contracts.UpstreamOfferObservation{
		{ID: "input", UserID: 42, SourceID: "source-a", PriceDimension: contracts.UpstreamPriceInput},
		{ID: "output", UserID: 42, SourceID: "source-a", PriceDimension: contracts.UpstreamPriceOutput},
		{ID: "foreign", UserID: 77, SourceID: "source-a", PriceDimension: contracts.UpstreamPriceInput},
	}
	links := []contracts.UpstreamIntelligenceLink{
		{UserID: 42, IntelligenceSourceID: "source-a", Scope: contracts.UpstreamLinkChannel, ChannelID: "channel", PriceDimension: contracts.UpstreamPriceInput, Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt},
		{UserID: 42, IntelligenceSourceID: "source-a", Scope: contracts.UpstreamLinkChannel, ChannelID: "other", PriceDimension: contracts.UpstreamPriceOutput, Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt},
	}
	got := ResolvePriceEvidence(42, "channel", links, offers)
	if len(got) != 1 || got[0].Offer.ID != "input" || !got[0].TargetVerified || got[0].OwnerID != 42 || got[0].ChannelID != "channel" {
		t.Fatalf("explicit link resolution mismatch: %+v", got)
	}
	links[0].VerifiedAt = nil
	if got := ResolvePriceEvidence(42, "channel", links, offers); len(got) != 0 {
		t.Fatalf("unverified link authorized evidence: %+v", got)
	}
}

func TestAttributeObservationBuildsFourFactsWithoutEstimatedCostFallback(t *testing.T) {
	now := time.Date(2026, 7, 26, 2, 0, 0, 0, time.UTC)
	input, output, requests := int64(500000), int64(0), int64(1)
	group := "paid"
	cost := contracts.CanonicalDecimal("2")
	verifiedAt := now.Add(-time.Hour)
	validUntil := now.Add(time.Hour)
	observation := contracts.ConnectorChannelObservation{
		ObservationID: "usage-1", Model: "gpt", ObservedAt: now,
		InputTokens: 999999, OutputTokens: 999999,
		CostUsage: &contracts.ConnectorCostUsage{InputTokens: &input, OutputTokens: &output, RequestCount: &requests, GroupKey: &group},
	}
	facts, err := AttributeObservation(AttributionInput{
		OwnerID: 42, InstanceID: "inst", ChannelID: "channel", UsageObservationID: "core-usage-1", Observation: observation,
		Links: []contracts.UpstreamIntelligenceLink{{UserID: 42, IntelligenceSourceID: "source", Scope: contracts.UpstreamLinkChannel, ChannelID: "channel", Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt}},
		Offers: []contracts.UpstreamOfferObservation{{
			ID: "price-input", UserID: 42, SourceID: "source", ModelKey: "gpt", GroupKey: "paid",
			PriceDimension: contracts.UpstreamPriceInput, EffectiveUnitCost: &cost, PerTokens: 1000000,
			SettlementCurrency: "USD", Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete,
			EffectiveAt: now.Add(-time.Hour), ValidUntil: &validUntil,
		}},
	})
	if err != nil || len(facts) != 4 {
		t.Fatalf("attribute facts=%+v err=%v", facts, err)
	}
	if facts[0].Amount == nil || *facts[0].Amount != "1" || facts[0].Quantity == nil || *facts[0].Quantity != 500000 {
		t.Fatalf("financial input used legacy tokens or wrong decimal: %+v", facts[0])
	}
	if facts[1].Amount != nil || facts[1].ReasonCode != contracts.UpstreamCostReasonPriceUnavailable {
		t.Fatalf("missing output price did not fail closed: %+v", facts[1])
	}
	if facts[2].Amount != nil || facts[2].ReasonCode != contracts.UpstreamCostReasonMissingQuantity {
		t.Fatalf("missing cached input became zero: %+v", facts[2])
	}
}

func TestAttributeAndAppendCommitsOneCoreScopedFourDimensionBatch(t *testing.T) {
	now := time.Date(2026, 7, 26, 3, 0, 0, 0, time.UTC)
	appender := &recordingCostFactAppender{}
	localObservationID := "connector-local-usage"
	facts, version, err := AttributeAndAppend(context.Background(), appender, AttributionInput{
		OwnerID: 42, InstanceID: "inst", ChannelID: "channel", UsageObservationID: "c9:connector:connector-local-usage",
		Observation: contracts.ConnectorChannelObservation{ObservationID: localObservationID, Model: "gpt", ObservedAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if appender.calls != 1 || len(appender.facts) != 4 || len(facts) != 4 || version.UserID != 42 || version.FactVersion != 1 {
		t.Fatalf("append calls=%d recorded=%d facts=%d version=%+v", appender.calls, len(appender.facts), len(facts), version)
	}
	for _, fact := range appender.facts {
		if fact.UsageObservationID != "c9:connector:connector-local-usage" || fact.UsageObservationID == localObservationID {
			t.Fatalf("fact used connector-local id: %+v", fact)
		}
		if fact.Amount != nil || fact.Attribution != contracts.UpstreamCostUnattributed {
			t.Fatalf("unknown dimension was dropped or invented: %+v", fact)
		}
	}
}

func TestAttributeAndAppendDoesNotCallStoreWhenConversionFails(t *testing.T) {
	appender := &recordingCostFactAppender{}
	_, _, err := AttributeAndAppend(context.Background(), appender, AttributionInput{
		OwnerID: 42, InstanceID: "inst", ChannelID: "channel",
		Observation: contracts.ConnectorChannelObservation{ObservationID: "local", Model: "gpt", ObservedAt: time.Now().UTC()},
	})
	if !errors.Is(err, ErrInvalidUsageObservation) || appender.calls != 0 {
		t.Fatalf("conversion err=%v calls=%d", err, appender.calls)
	}
}

package upstreamintelligence

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestBuildFrontierBlocksUnverifiedOrMismatchedLinks(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	verifiedAt := now.Add(-time.Hour)
	tests := []struct {
		name   string
		mutate func(*FrontierCandidate)
	}{
		{name: "missing link", mutate: func(candidate *FrontierCandidate) { candidate.Link = nil }},
		{name: "cross owner", mutate: func(candidate *FrontierCandidate) { candidate.Link.UserID++ }},
		{name: "wrong intelligence source", mutate: func(candidate *FrontierCandidate) { candidate.Link.IntelligenceSourceID = "source-other" }},
		{name: "target not verified", mutate: func(candidate *FrontierCandidate) { candidate.TargetVerified = false }},
		{name: "resolved channel cross owner", mutate: func(candidate *FrontierCandidate) { candidate.ResolvedChannelOwnerID++ }},
		{name: "resolved channel mismatch", mutate: func(candidate *FrontierCandidate) { candidate.ResolvedChannelID = "channel-other" }},
		{name: "inactive", mutate: func(candidate *FrontierCandidate) { candidate.Link.Status = contracts.UpstreamLinkInactive }},
		{name: "not verified", mutate: func(candidate *FrontierCandidate) { candidate.Link.VerifiedAt = nil }},
		{name: "wrong dimension", mutate: func(candidate *FrontierCandidate) { candidate.Link.PriceDimension = contracts.UpstreamPriceOutput }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := eligibleCandidate(now, "offer-a", "source-a", "channel-a", "model-a", "group-a", "1", "90")
			candidate.Link.VerifiedAt = &verifiedAt
			test.mutate(&candidate)
			point := BuildFrontier([]FrontierCandidate{candidate}, now)[0]
			assertBlocked(t, point, contracts.UpstreamIntelligenceNotComparableUnlinkedQuality)
			if point.LinkState != contracts.UpstreamIntelligenceFrontierUnlinked || point.ChannelID != "" || point.QualityScore != nil || point.QualityEvidence != nil {
				t.Fatalf("unverified link exposed a join: %+v", point)
			}
		})
	}
}

func TestBuildFrontierAcceptsOwnerVerifiedSourceIdentityResolutionWithoutExposingIdentity(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	candidate := eligibleCandidate(now, "offer-a", "source-a", "channel-a", "model-a", "group-a", "1", "90")
	candidate.Link.Scope, candidate.Link.ChannelID, candidate.Link.UpstreamSourceIdentity = contracts.UpstreamLinkSourceIdentity, "", "opaque-local-identity"
	point := BuildFrontier([]FrontierCandidate{candidate}, now)[0]
	if point.Status != contracts.UpstreamIntelligenceFrontierEligible || !point.OnFrontier || point.ChannelID != "channel-a" {
		t.Fatalf("verified source identity resolution was not eligible: %+v", point)
	}
	payload, err := json.Marshal(point)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), candidate.Link.UpstreamSourceIdentity) {
		t.Fatal("frontier DTO exposed the opaque source identity")
	}
}

func TestBuildFrontierBlocksUnavailableInsufficientAndStaleQuality(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*FrontierCandidate)
		want   contracts.UpstreamIntelligenceComparabilityReason
	}{
		{name: "missing quality", mutate: func(candidate *FrontierCandidate) { candidate.Quality = nil }, want: contracts.UpstreamIntelligenceNotComparableQualityUnavailable},
		{name: "quality cross owner", mutate: func(candidate *FrontierCandidate) { candidate.Quality.OwnerID++ }, want: contracts.UpstreamIntelligenceNotComparableQualityUnavailable},
		{name: "quality wrong channel", mutate: func(candidate *FrontierCandidate) { candidate.Quality.ChannelID = "channel-other" }, want: contracts.UpstreamIntelligenceNotComparableQualityUnavailable},
		{name: "quality wrong model", mutate: func(candidate *FrontierCandidate) { candidate.Quality.ModelKey = "model-other" }, want: contracts.UpstreamIntelligenceNotComparableQualityUnavailable},
		{name: "unknown score", mutate: func(candidate *FrontierCandidate) { candidate.Quality.QualityScore = nil }, want: contracts.UpstreamIntelligenceNotComparableQualityUnavailable},
		{name: "unknown health", mutate: func(candidate *FrontierCandidate) { candidate.Quality.HealthState = contracts.HealthUnknown }, want: contracts.UpstreamIntelligenceNotComparableQualityUnavailable},
		{name: "too few samples", mutate: func(candidate *FrontierCandidate) { candidate.Quality.QualitySampleCount = 4 }, want: contracts.UpstreamIntelligenceNotComparableQualityInsufficient},
		{name: "invalid threshold", mutate: func(candidate *FrontierCandidate) { candidate.Quality.MinimumSampleCount = 0 }, want: contracts.UpstreamIntelligenceNotComparableQualityInsufficient},
		{name: "stale", mutate: func(candidate *FrontierCandidate) { candidate.Quality.FreshUntil = now.Add(-time.Minute) }, want: contracts.UpstreamIntelligenceNotComparableQualityStale},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := eligibleCandidate(now, "offer-a", "source-a", "channel-a", "model-a", "group-a", "1", "90")
			test.mutate(&candidate)
			point := BuildFrontier([]FrontierCandidate{candidate}, now)[0]
			assertBlocked(t, point, test.want)
			if point.OnFrontier {
				t.Fatal("blocked quality entered the frontier")
			}
		})
	}
}

func TestBuildFrontierTreatsExactQualityExpiryBoundaryAsCurrent(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	candidate := eligibleCandidate(now, "offer-boundary", "source-a", "channel-a", "model-a", "group-a", "1", "90")
	candidate.Quality.FreshUntil = now
	point := BuildFrontier([]FrontierCandidate{candidate}, now)[0]
	if point.Status != contracts.UpstreamIntelligenceFrontierEligible || !point.OnFrontier || point.QualityEvidence == nil ||
		point.QualityEvidence.Freshness != contracts.UpstreamFreshnessCurrent {
		t.Fatalf("exact fresh-until boundary must remain current: %+v", point)
	}
}

func TestBuildFrontierCarriesCostBlockerAndNeverTreatsUnknownAsZero(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	candidate := eligibleCandidate(now, "offer-a", "source-a", "channel-a", "model-a", "group-a", "1", "90")
	candidate.Rate.Comparable = false
	candidate.Rate.ComparabilityReason = contracts.UpstreamIntelligenceNotComparableMissingPrice
	candidate.Rate.EffectiveUnitCost = nil
	point := BuildFrontier([]FrontierCandidate{candidate}, now)[0]
	assertBlocked(t, point, contracts.UpstreamIntelligenceNotComparableMissingPrice)
	if point.QualityScore == nil || point.OnFrontier {
		t.Fatalf("cost-unknown point should retain quality evidence but not rank: %+v", point)
	}
}

func TestBuildFrontierParetoUsesExactDecimalsAndHighQualityIsBetter(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	candidates := []FrontierCandidate{
		eligibleCandidate(now, "cheap", "source-a", "channel-a", "model-a", "group-a", "0.100000000000000002", "80"),
		eligibleCandidate(now, "balanced", "source-b", "channel-b", "model-a", "group-a", "0.100000000000000003", "90"),
		eligibleCandidate(now, "dominated", "source-c", "channel-c", "model-a", "group-a", "0.100000000000000003", "89.999999999999999999"),
		eligibleCandidate(now, "best-quality", "source-d", "channel-d", "model-a", "group-a", "0.100000000000000004", "95"),
	}
	points := BuildFrontier(candidates, now)
	want := map[string]bool{"cheap": true, "balanced": true, "dominated": false, "best-quality": true}
	for _, point := range points {
		if point.Status != contracts.UpstreamIntelligenceFrontierEligible || point.OnFrontier != want[point.Rate.ObservationID] {
			t.Fatalf("point %s status/frontier = %s/%v; want eligible/%v", point.Rate.ObservationID, point.Status, point.OnFrontier, want[point.Rate.ObservationID])
		}
	}
}

func TestBuildFrontierSameCostHigherQualityDominates(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	points := BuildFrontier([]FrontierCandidate{
		eligibleCandidate(now, "better", "source-a", "channel-a", "model-a", "group-a", "1", "91"),
		eligibleCandidate(now, "worse", "source-b", "channel-b", "model-a", "group-a", "1", "90"),
	}, now)
	if !points[0].OnFrontier || points[1].OnFrontier {
		t.Fatalf("same-cost higher quality must dominate: %+v", points)
	}
}

func TestBuildFrontierEqualPointsDoNotDominateEachOther(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	points := BuildFrontier([]FrontierCandidate{
		eligibleCandidate(now, "a", "source-a", "channel-a", "model-a", "group-a", "1", "90"),
		eligibleCandidate(now, "b", "source-b", "channel-b", "model-a", "group-a", "1", "90"),
	}, now)
	if !points[0].OnFrontier || !points[1].OnFrontier {
		t.Fatalf("equal points must coexist on frontier: %+v", points)
	}
}

func TestBuildFrontierIsolatesEveryCohortDimension(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	base := eligibleCandidate(now, "base", "source-a", "channel-a", "model-a", "group-a", "1", "99")
	tests := []struct {
		name   string
		mutate func(*FrontierCandidate)
	}{
		{name: "model", mutate: func(candidate *FrontierCandidate) {
			candidate.Rate.ModelKey, candidate.Quality.ModelKey = "model-b", "model-b"
		}},
		{name: "dimension", mutate: func(candidate *FrontierCandidate) {
			candidate.Rate.PriceDimension, candidate.Link.PriceDimension = contracts.UpstreamPriceOutput, contracts.UpstreamPriceOutput
		}},
		{name: "currency", mutate: func(candidate *FrontierCandidate) { candidate.Rate.SettlementCurrency = "CNY" }},
		{name: "per tokens", mutate: func(candidate *FrontierCandidate) { candidate.Rate.PerTokens = 1_000 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := eligibleCandidate(now, "isolated", "source-b", "channel-b", "model-a", "group-a", "100", "1")
			test.mutate(&candidate)
			points := BuildFrontier([]FrontierCandidate{base, candidate}, now)
			if !points[0].OnFrontier || !points[1].OnFrontier {
				t.Fatalf("different %s cohorts dominated one another: %+v", test.name, points)
			}
		})
	}
}

func TestBuildFrontierComparesDifferentGroupsWithinOnePriceCohort(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	cheap := eligibleCandidate(now, "cheap", "source-a", "channel-a", "model-a", "group-a", "1", "90")
	expensive := eligibleCandidate(now, "expensive", "source-b", "channel-b", "model-a", "group-b", "2", "80")
	points := BuildFrontier([]FrontierCandidate{cheap, expensive}, now)
	if !points[0].OnFrontier || points[1].OnFrontier {
		t.Fatalf("group key incorrectly split the comparable cohort: %+v", points)
	}
}

func TestBuildFrontierDoesNotMutateInputs(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	candidate := eligibleCandidate(now, "a", "source-a", "channel-a", "model-a", "group-a", "1", "90")
	before := *candidate.Quality.QualityScore
	points := BuildFrontier([]FrontierCandidate{candidate}, now)
	*points[0].QualityScore = contracts.CanonicalDecimal("1")
	*points[0].QualityEvidence.SuccessRate = contracts.CanonicalDecimal("0")
	if *candidate.Quality.QualityScore != before || *candidate.Quality.SuccessRate == contracts.CanonicalDecimal("0") {
		t.Fatal("frontier output aliases input decimal pointers")
	}
}

func eligibleCandidate(now time.Time, observationID, sourceID, channelID, model, group, cost, quality string) FrontierCandidate {
	verifiedAt := now.Add(-time.Hour)
	costValue, qualityValue := decimal(cost), decimal(quality)
	success, ttft, duration := decimal("0.99"), decimal("120"), decimal("600")
	return FrontierCandidate{
		OwnerID: 42, ResolvedChannelID: channelID, ResolvedChannelOwnerID: 42, TargetVerified: true,
		Rate: contracts.UpstreamIntelligenceRateReadModel{
			ObservationID: observationID, Source: contracts.UpstreamIntelligenceReadSourceSummary{ID: sourceID},
			GroupKey: group, ModelKey: model, PriceDimension: contracts.UpstreamPriceInput,
			SettlementCurrency: "USD", PerTokens: 1_000_000, EffectiveUnitCost: &costValue,
			UpstreamIntelligenceComparability: contracts.UpstreamIntelligenceComparability{Comparable: true},
		},
		Link: &contracts.UpstreamIntelligenceLink{
			UserID: 42, IntelligenceSourceID: sourceID, Scope: contracts.UpstreamLinkChannel, ChannelID: channelID,
			PriceDimension: contracts.UpstreamPriceInput, Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		},
		Quality: &QualityCandidate{
			OwnerID: 42, ChannelID: channelID, ModelKey: model, SnapshotID: "snapshot-" + observationID,
			Window: contracts.Window5m, QualityScore: &qualityValue, QualitySampleCount: 10, MinimumSampleCount: 5,
			SuccessRate: &success, TTFTP95Milliseconds: &ttft, DurationP95Milliseconds: &duration,
			HealthState: contracts.HealthHealthy, ObservedAt: now.Add(-time.Minute), FreshUntil: now.Add(time.Minute),
		},
	}
}

func decimal(value string) contracts.CanonicalDecimal {
	result, err := contracts.ParseCanonicalDecimal(value)
	if err != nil {
		panic(err)
	}
	return result
}

func assertBlocked(t *testing.T, point contracts.UpstreamIntelligenceFrontierPoint, reason contracts.UpstreamIntelligenceComparabilityReason) {
	t.Helper()
	if point.Status != contracts.UpstreamIntelligenceFrontierBlocked || point.OnFrontier {
		t.Fatalf("point was not blocked: %+v", point)
	}
	for _, got := range point.BlockedReasons {
		if got == reason {
			return
		}
	}
	t.Fatalf("blocked reasons %v do not contain %s", point.BlockedReasons, reason)
}

func TestBuildFrontierBlockersAreDeterministic(t *testing.T) {
	now := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	candidate := eligibleCandidate(now, "a", "source-a", "channel-a", "model-a", "group-a", "1", "90")
	candidate.Rate.Comparable = false
	candidate.Rate.ComparabilityReason = contracts.UpstreamIntelligenceNotComparableExpiredEvidence
	candidate.Quality.QualitySampleCount = 0
	candidate.Quality.FreshUntil = now.Add(-time.Hour)
	first := BuildFrontier([]FrontierCandidate{candidate}, now)[0].BlockedReasons
	second := BuildFrontier([]FrontierCandidate{candidate}, now)[0].BlockedReasons
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("blocker order changed: %v / %v", first, second)
	}
}

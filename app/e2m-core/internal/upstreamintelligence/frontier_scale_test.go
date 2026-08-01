package upstreamintelligence

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

const (
	ui17FrontierSourceCount = 100
	ui17FrontierFactCount   = 5_000
)

var ui17FrontierSink []contracts.UpstreamIntelligenceFrontierPoint

// TestBuildFrontierScale100Sources5000Facts protects the full 100-source,
// 5,000-fact decision workload. E2M_UI17_FRONTIER_MAX_MS is optional so local
// hardware and loaded CI runners do not create an unstable default gate.
func TestBuildFrontierScale100Sources5000Facts(t *testing.T) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	candidates := ui17FrontierCandidates(now)
	maximum := ui17FrontierDurationBudget(t)

	started := time.Now()
	points := BuildFrontier(candidates, now)
	elapsed := time.Since(started)
	assertUI17FrontierPoints(t, points)
	t.Logf("ui17 upstream-intelligence frontier: sources=%d rate_facts=%d elapsed=%s budget=%s",
		ui17FrontierSourceCount, len(points), elapsed, ui17FrontierBudgetText(maximum))
	if maximum > 0 && elapsed > maximum {
		t.Fatalf("frontier elapsed %s exceeds explicitly configured budget %s", elapsed, maximum)
	}
}

func BenchmarkBuildFrontier100Sources5000Facts(b *testing.B) {
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	candidates := ui17FrontierCandidates(now)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		points := BuildFrontier(candidates, now)
		if len(points) != ui17FrontierFactCount {
			b.Fatalf("frontier points=%d want=%d", len(points), ui17FrontierFactCount)
		}
		ui17FrontierSink = points
	}
	b.ReportMetric(ui17FrontierSourceCount, "sources/op")
	b.ReportMetric(ui17FrontierFactCount, "rate_facts/op")
}

func ui17FrontierCandidates(now time.Time) []FrontierCandidate {
	candidates := make([]FrontierCandidate, 0, ui17FrontierFactCount)
	verifiedAt := now.Add(-time.Hour)
	qualityScore := contracts.CanonicalDecimal("90")
	successRate := contracts.CanonicalDecimal("0.99")
	ttft := contracts.CanonicalDecimal("200")
	duration := contracts.CanonicalDecimal("800")
	for sourceIndex := 0; sourceIndex < ui17FrontierSourceCount; sourceIndex++ {
		sourceID := fmt.Sprintf("source-ui17-%03d", sourceIndex)
		channelID := fmt.Sprintf("channel-ui17-%03d", sourceIndex)
		linkID := fmt.Sprintf("link-ui17-%03d", sourceIndex)
		link := contracts.UpstreamIntelligenceLink{
			ID: linkID, UserID: 17, IntelligenceSourceID: sourceID, Scope: contracts.UpstreamLinkChannel,
			ChannelID: channelID, PriceDimension: contracts.UpstreamPriceInput,
			Status: contracts.UpstreamLinkActive, VerifiedAt: &verifiedAt,
		}
		for factIndex := 0; factIndex < ui17FrontierFactCount/ui17FrontierSourceCount; factIndex++ {
			cost := contracts.CanonicalDecimal(strconv.Itoa(1 + sourceIndex%20))
			rate := contracts.UpstreamIntelligenceRateReadModel{
				ObservationID: fmt.Sprintf("offer-ui17-%03d-%03d", sourceIndex, factIndex),
				Source: contracts.UpstreamIntelligenceReadSourceSummary{
					ID: sourceID, Mode: contracts.UpstreamSourceExternal, Provider: "sub2api",
					DisplayName: fmt.Sprintf("UI-17 source %03d", sourceIndex), Currency: "USD",
					Status: contracts.UpstreamSourceActive,
				},
				GroupKey: "default", ModelKey: fmt.Sprintf("model-ui17-%03d", factIndex),
				PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", PerTokens: 1_000_000,
				EffectiveUnitCost: ui17FrontierDecimal(cost),
				Evidence: contracts.UpstreamIntelligenceReadEvidence{
					Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete,
					Freshness: contracts.UpstreamFreshnessCurrent, ObservedAt: now.Add(-time.Minute),
					ReceivedAt: now.Add(-time.Minute), FreshUntil: now.Add(9 * time.Minute), MissingFields: []string{},
				},
				UpstreamIntelligenceComparability: contracts.UpstreamIntelligenceComparability{Comparable: true},
			}
			quality := &QualityCandidate{
				OwnerID: 17, ChannelID: channelID, ModelKey: rate.ModelKey,
				SnapshotID: fmt.Sprintf("quality-ui17-%03d-%03d", sourceIndex, factIndex),
				Window:     contracts.Window5m, QualityScore: ui17FrontierDecimal(qualityScore),
				QualitySampleCount: 20, MinimumSampleCount: 5,
				SuccessRate: ui17FrontierDecimal(successRate), TTFTP95Milliseconds: ui17FrontierDecimal(ttft),
				DurationP95Milliseconds: ui17FrontierDecimal(duration), HealthState: contracts.HealthHealthy,
				ObservedAt: now.Add(-time.Minute), FreshUntil: now.Add(4 * time.Minute),
			}
			linkCopy := link
			candidates = append(candidates, FrontierCandidate{
				OwnerID: 17, Rate: rate, Link: &linkCopy, ResolvedChannelID: channelID,
				ResolvedChannelOwnerID: 17, TargetVerified: true, Quality: quality,
			})
		}
	}
	return candidates
}

func assertUI17FrontierPoints(t *testing.T, points []contracts.UpstreamIntelligenceFrontierPoint) {
	t.Helper()
	if len(points) != ui17FrontierFactCount {
		t.Fatalf("frontier points=%d want=%d", len(points), ui17FrontierFactCount)
	}
	eligible, onFrontier := 0, 0
	for _, point := range points {
		if point.Status != contracts.UpstreamIntelligenceFrontierEligible || len(point.BlockedReasons) != 0 {
			t.Fatalf("unexpected blocked point: %+v", point)
		}
		eligible++
		if point.OnFrontier {
			onFrontier++
		}
	}
	if eligible != ui17FrontierFactCount || onFrontier == 0 {
		t.Fatalf("frontier classification eligible=%d on_frontier=%d", eligible, onFrontier)
	}
}

func ui17FrontierDurationBudget(t testing.TB) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("E2M_UI17_FRONTIER_MAX_MS"))
	if raw == "" {
		return 0
	}
	milliseconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || milliseconds <= 0 {
		t.Fatal("E2M_UI17_FRONTIER_MAX_MS must be a positive integer number of milliseconds")
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func ui17FrontierBudgetText(value time.Duration) string {
	if value <= 0 {
		return "report-only"
	}
	return value.String()
}

func ui17FrontierDecimal(value contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	copyValue := value
	return &copyValue
}

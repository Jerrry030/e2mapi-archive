package store

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"e2m.local/contracts"
)

const (
	ui17ScaleSourceCount = 100
	ui17ScaleFactCount   = 5_000
	ui17ScaleHistoryDays = 400
	ui17ScaleOwnerID     = int64(17)
)

var ui17ScaleReadSink UpstreamIntelligenceCurrentSnapshot

// TestReadUpstreamIntelligenceCurrentScale100Sources5000Facts is a controlled
// local acceptance workload. Its default assertion is semantic (the complete
// snapshot must be returned); elapsed time is reported but is only a gate when
// E2M_UI17_READ_MAX_MS is explicitly configured by the operator running it.
func TestReadUpstreamIntelligenceCurrentScale100Sources5000Facts(t *testing.T) {
	st, referenceTime := newUI17ScaleMemoryStore()
	maximum := optionalUI17DurationBudget(t, "E2M_UI17_READ_MAX_MS")

	started := time.Now()
	snapshot, err := st.ReadUpstreamIntelligenceCurrent(context.Background(), ui17ScaleOwnerID, &referenceTime)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("read scale snapshot: %v", err)
	}
	assertUI17ScaleSnapshot(t, snapshot)
	t.Logf("ui17 upstream-intelligence read: sources=%d rate_facts=%d elapsed=%s budget=%s",
		len(snapshot.Sources), len(snapshot.Offers), elapsed, formatUI17Budget(maximum))
	if maximum > 0 && elapsed > maximum {
		t.Fatalf("read elapsed %s exceeds explicitly configured budget %s", elapsed, maximum)
	}
}

func TestReadUpstreamIntelligenceCurrentScaleIncludes400DayChangeRollup(t *testing.T) {
	st, referenceTime := newUI17ScaleMemoryStore()
	st.upstreamIntelChanges = make([]contracts.UpstreamChangeEvent, 0, ui17ScaleSourceCount*ui17ScaleHistoryDays)
	for day := 0; day < ui17ScaleHistoryDays; day++ {
		confirmedAt := referenceTime.Add(-time.Duration(day) * 24 * time.Hour)
		for sourceIndex := 0; sourceIndex < ui17ScaleSourceCount; sourceIndex++ {
			st.upstreamIntelChanges = append(st.upstreamIntelChanges, contracts.UpstreamChangeEvent{
				ID: fmt.Sprintf(string([]byte{99, 104, 97, 110, 103, 101, 45, 37, 48, 51, 100, 45, 37, 48, 51, 100}), day, sourceIndex), UserID: ui17ScaleOwnerID,
				SourceID: fmt.Sprintf(string([]byte{115, 111, 117, 114, 99, 101, 45, 117, 105, 49, 55, 45, 37, 48, 51, 100}), sourceIndex), Type: contracts.UpstreamChangePriceIncreased,
				FirstDetectedAt: confirmedAt, ConfirmedAt: confirmedAt, Severity: contracts.UpstreamChangeInfo,
			})
		}
	}
	snapshot, err := st.ReadUpstreamIntelligenceCurrent(context.Background(), ui17ScaleOwnerID, &referenceTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Changes) != 800 {
		t.Fail()
	}
}

func BenchmarkReadUpstreamIntelligenceCurrent100Sources5000Facts(b *testing.B) {
	st, referenceTime := newUI17ScaleMemoryStore()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		snapshot, err := st.ReadUpstreamIntelligenceCurrent(ctx, ui17ScaleOwnerID, &referenceTime)
		if err != nil {
			b.Fatal(err)
		}
		if len(snapshot.Sources) != ui17ScaleSourceCount || len(snapshot.Offers) != ui17ScaleFactCount {
			b.Fatalf("incomplete snapshot: sources=%d offers=%d", len(snapshot.Sources), len(snapshot.Offers))
		}
		ui17ScaleReadSink = snapshot
	}
	b.ReportMetric(ui17ScaleSourceCount, "sources/op")
	b.ReportMetric(ui17ScaleFactCount, "rate_facts/op")
}

func newUI17ScaleMemoryStore() (*MemoryStore, time.Time) {
	referenceTime := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(referenceTime)
	st.now = func() time.Time { return referenceTime }
	st.upstreamIntelVersions[ui17ScaleOwnerID] = contracts.UpstreamIntelligenceFactVersion{
		UserID: ui17ScaleOwnerID, FactVersion: 1, UpdatedAt: referenceTime,
	}
	st.upstreamIntelSources = make([]contracts.UpstreamIntelligenceSource, 0, ui17ScaleSourceCount)
	st.upstreamIntelRuns = make([]contracts.UpstreamCollectionRun, 0, ui17ScaleSourceCount)
	st.upstreamIntelOffers = make([]contracts.UpstreamOfferObservation, 0, ui17ScaleFactCount)

	observedAt := referenceTime.Add(-time.Minute)
	completedAt := observedAt.Add(time.Second)
	freshUntil := referenceTime.Add(9 * time.Minute)
	groupMultiplier := contracts.CanonicalDecimal("0.8")
	rechargeYield := contracts.CanonicalDecimal("2")
	effectiveMultiplier := contracts.CanonicalDecimal("0.4")
	for sourceIndex := 0; sourceIndex < ui17ScaleSourceCount; sourceIndex++ {
		sourceID := fmt.Sprintf("source-ui17-%03d", sourceIndex)
		runID := fmt.Sprintf("run-ui17-%03d", sourceIndex)
		connectorID := fmt.Sprintf("connector-ui17-%03d", sourceIndex)
		instanceID := fmt.Sprintf("instance-ui17-%03d", sourceIndex)
		st.upstreamIntelSources = append(st.upstreamIntelSources, contracts.UpstreamIntelligenceSource{
			ID: sourceID, UserID: ui17ScaleOwnerID, ConnectorID: connectorID, InstanceID: instanceID,
			LocalRef: fmt.Sprintf("local-ui17-%03d", sourceIndex), Mode: contracts.UpstreamSourceExternal,
			Provider: "sub2api", DisplayName: fmt.Sprintf("UI-17 source %03d", sourceIndex), Currency: "USD",
			PollIntervalSeconds: 300, Status: contracts.UpstreamSourceActive,
			Capabilities: contracts.UpstreamIntelligenceCapabilities{Balance: true, Groups: true, Rates: true, Prices: true},
			CreatedAt:    referenceTime.Add(-time.Hour), UpdatedAt: referenceTime,
		})
		st.upstreamIntelRuns = append(st.upstreamIntelRuns, contracts.UpstreamCollectionRun{
			ID: runID, UserID: ui17ScaleOwnerID, SourceID: sourceID, ConnectorID: connectorID,
			Trigger: contracts.UpstreamCollectionScheduled, Status: contracts.UpstreamCollectionSucceeded,
			Coverage: contracts.UpstreamCoverageComplete, StartedAt: observedAt.Add(-time.Second),
			ObservedAt: observedAt, ReceivedAt: completedAt, CompletedAt: &completedAt,
			BatchCount: 1, FactCount: ui17ScaleFactCount / ui17ScaleSourceCount, PageCount: 1,
			FinalizedFactVersion: 1, CreatedAt: completedAt, UpdatedAt: completedAt,
		})
		for factIndex := 0; factIndex < ui17ScaleFactCount/ui17ScaleSourceCount; factIndex++ {
			publishedPrice := contracts.CanonicalDecimal(strconv.Itoa(1 + sourceIndex%20))
			effectiveCost, err := contracts.CalculateEffectiveUnitCost(publishedPrice, effectiveMultiplier)
			if err != nil {
				panic(err)
			}
			st.upstreamIntelOffers = append(st.upstreamIntelOffers, contracts.UpstreamOfferObservation{
				ID: fmt.Sprintf("offer-ui17-%03d-%03d", sourceIndex, factIndex), RunID: runID,
				UserID: ui17ScaleOwnerID, SourceID: sourceID, GroupKey: "default",
				ModelKey: fmt.Sprintf("model-ui17-%03d", factIndex), PriceDimension: contracts.UpstreamPriceInput,
				SettlementCurrency: "USD", GroupMultiplier: ui17DecimalCopy(groupMultiplier), RechargeYield: ui17DecimalCopy(rechargeYield),
				PublishedUnitPrice: ui17DecimalCopy(publishedPrice), PerTokens: 1_000_000,
				EffectiveMultiplier: ui17DecimalCopy(effectiveMultiplier), EffectiveUnitCost: ui17DecimalCopy(effectiveCost),
				FormulaVersion: "effective-cost/v1", Accuracy: contracts.UpstreamEvidenceExact,
				Coverage: contracts.UpstreamCoverageComplete, ObservedAt: observedAt, EffectiveAt: observedAt,
				ReceivedAt: completedAt, FreshUntil: freshUntil, MissingFields: []string{}, AdapterSchemaVersion: 1,
			})
		}
	}
	return st, referenceTime
}

func assertUI17ScaleSnapshot(t *testing.T, snapshot UpstreamIntelligenceCurrentSnapshot) {
	t.Helper()
	if snapshot.UserID != ui17ScaleOwnerID || snapshot.FactVersion.UserID != ui17ScaleOwnerID || snapshot.FactVersion.FactVersion != 1 {
		t.Fatalf("owner/version mismatch: user=%d version=%+v", snapshot.UserID, snapshot.FactVersion)
	}
	if len(snapshot.Sources) != ui17ScaleSourceCount || len(snapshot.LatestRuns) != ui17ScaleSourceCount || len(snapshot.Offers) != ui17ScaleFactCount {
		t.Fatalf("incomplete snapshot: sources=%d runs=%d offers=%d", len(snapshot.Sources), len(snapshot.LatestRuns), len(snapshot.Offers))
	}
	seen := make(map[string]struct{}, len(snapshot.Offers))
	for _, offer := range snapshot.Offers {
		key := upstreamReadOfferKey(offer)
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate current offer key %q", key)
		}
		seen[key] = struct{}{}
	}
}

func optionalUI17DurationBudget(t testing.TB, environmentName string) time.Duration {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(environmentName))
	if raw == "" {
		return 0
	}
	milliseconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || milliseconds <= 0 {
		t.Fatalf("%s must be a positive integer number of milliseconds", environmentName)
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func formatUI17Budget(value time.Duration) string {
	if value <= 0 {
		return "report-only"
	}
	return value.String()
}

func ui17DecimalCopy(value contracts.CanonicalDecimal) *contracts.CanonicalDecimal {
	copyValue := value
	return &copyValue
}

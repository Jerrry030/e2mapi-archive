package store

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryOperationalMetricsAggregatesWithoutIdentifiers(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	st := NewMemoryStore(now)
	st.now = func() time.Time { return now }
	lastSuccess := now.Add(-20 * time.Minute)
	st.upstreamIntelSources = []contracts.UpstreamIntelligenceSource{{
		ID: "private-source", UserID: 42, Status: contracts.UpstreamSourceActive,
		PollIntervalSeconds: 300, LastSuccessAt: &lastSuccess, LastErrorCode: contracts.UpstreamCollectionErrorRateLimited,
		CreatedAt: now.Add(-time.Hour),
	}}
	completedAt := now.Add(-time.Minute)
	st.upstreamIntelRuns = []contracts.UpstreamCollectionRun{{
		ID: "run-1", UserID: 42, SourceID: "private-source", Status: contracts.UpstreamCollectionSucceeded,
		FinalizedFactVersion: 1, CompletedAt: &completedAt,
	}}
	cost := contracts.CanonicalDecimal("1.5")
	st.upstreamIntelOffers = []contracts.UpstreamOfferObservation{
		{ID: "fresh", RunID: "run-1", UserID: 42, SourceID: "private-source", GroupKey: "default", ModelKey: "fresh",
			PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", EffectiveUnitCost: &cost,
			Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete,
			ObservedAt: now.Add(-time.Minute), FreshUntil: now.Add(time.Minute)},
		{ID: "stale", RunID: "run-1", UserID: 42, SourceID: "private-source", GroupKey: "default", ModelKey: "stale",
			PriceDimension: contracts.UpstreamPriceInput, SettlementCurrency: "USD", EffectiveUnitCost: &cost,
			Accuracy: contracts.UpstreamEvidenceExact, Coverage: contracts.UpstreamCoverageComplete,
			ObservedAt: now.Add(-time.Hour), FreshUntil: now.Add(-time.Second)},
	}
	st.upstreamRecommendations = []contracts.UpstreamRecommendation{{Status: contracts.UpstreamRecommendationOpen}}
	st.upstreamShadowResults = []contracts.UpstreamShadowResult{{ID: "shadow-private"}}
	st.upstreamDryRunResults = []contracts.UpstreamDryRunResult{{ID: "dry-private"}}
	st.operationalMetricCounters[operationalMetricKey{Metric: "experiments", Label: "shadow"}] = 1
	st.operationalMetricCounters[operationalMetricKey{Metric: "experiments", Label: "dry_run"}] = 1
	st.connectorTasks = []contracts.ConnectorTask{{Status: contracts.ConnectorTaskPending, CreatedAt: now.Add(-2 * time.Minute), ExpiresAt: now.Add(time.Hour)}}
	st.upstreamCostJobs = []UpstreamCostAttributionJob{{Status: UpstreamCostJobRetrying, CreatedAt: now.Add(-3 * time.Minute)}}
	st.recommendationRollouts = []contracts.RecommendationRollout{
		{State: contracts.RecommendationRolloutState{ID: "rollout-closed", Status: contracts.RecommendationRolloutCompleted, Stage: contracts.RecommendationRolloutStage100}},
		{State: contracts.RecommendationRolloutState{ID: "rollout-actionable", Status: contracts.RecommendationRolloutRollbackRequired, Stage: contracts.RecommendationRolloutStage10}},
		{State: contracts.RecommendationRolloutState{ID: "rollout-rollback", Status: contracts.RecommendationRolloutRollbackRequired, Stage: contracts.RecommendationRolloutStage25}},
	}
	st.recommendationRolloutOperations = []contracts.RecommendationRolloutOperation{
		{ID: "old-failure", RolloutID: "rollout-closed", Status: contracts.RecommendationRolloutOperationFailed, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour)},
		{ID: "closed-failure", RolloutID: "rollout-closed", Action: contracts.RecommendationRolloutOperationRollback, Status: contracts.RecommendationRolloutOperationFailed, CreatedAt: now.Add(-30 * time.Minute), UpdatedAt: now.Add(-30 * time.Minute)},
		{ID: "old-active-failure", RolloutID: "rollout-actionable", Status: contracts.RecommendationRolloutOperationFailed, CreatedAt: now.Add(-20 * time.Minute), UpdatedAt: now.Add(-20 * time.Minute)},
		{ID: "retry", RolloutID: "rollout-actionable", Status: contracts.RecommendationRolloutOperationFailed, CreatedAt: now.Add(-20 * time.Minute), UpdatedAt: now.Add(-4 * time.Minute)},
		{ID: "rollback-failure", RolloutID: "rollout-rollback", Action: contracts.RecommendationRolloutOperationRollback, Status: contracts.RecommendationRolloutOperationFailed, CreatedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-2 * time.Minute)},
	}

	got, err := st.GetOperationalMetrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got.SourcesByState["active"] != 1 || got.SourcesByState["stale"] != 1 || got.SourcesByState["failed"] != 1 {
		t.Fatalf("source aggregates=%v", got.SourcesByState)
	}
	if got.StaleEvidenceCount != 1 || got.FreshComparableCoverage == nil || *got.FreshComparableCoverage != .5 {
		t.Fatalf("evidence aggregates=%+v", got)
	}
	if got.RecommendationsByStatus["open"] != 1 || got.ExperimentsByKind["shadow"] != 1 || got.ExperimentsByKind["dry_run"] != 1 {
		t.Fatalf("recommendation aggregates=%+v", got)
	}
	if got.RolloutsByStatus["completed"] != 1 || got.RolloutsByStatus["rollback_required"] != 2 || got.RolloutsByStage["100"] != 1 || got.RolloutsByStage["10"] != 1 || got.RolloutsByStage["25"] != 1 {
		t.Fatalf("rollout aggregates=%+v", got)
	}
	if got.OldestConnectorTaskAgeSeconds != 120 || got.OldestCostJobAgeSeconds != 180 {
		t.Fatalf("queue ages=%+v", got)
	}
	if got.OldestRolloutActionAgeSeconds != 240 {
		t.Fatalf("latest rollout action age=%v, want 240", got.OldestRolloutActionAgeSeconds)
	}
	if got.RollbackFailureCount != 1 {
		t.Fatalf("active latest rollback failures=%d, want 1", got.RollbackFailureCount)
	}
}

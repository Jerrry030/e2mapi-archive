package store

import (
	"context"
	"strconv"
	"time"

	"e2m.local/contracts"
	"e2m.local/core/internal/operationalmetrics"
)

const operationalMetricsSourceStaleFactor = 2

var (
	_ operationalmetrics.Store = (*MemoryStore)(nil)
	_ operationalmetrics.Store = (*PostgresStore)(nil)
)

func newOperationalMetricsSnapshot() operationalmetrics.Snapshot {
	return operationalmetrics.Snapshot{
		SourcesByState: map[string]int64{
			"active": 0, "paused": 0, "disconnected": 0, "stale": 0, "failed": 0,
		},
		RecommendationsByStatus: map[string]int64{
			"open": 0, "shadowing": 0, "ready_for_dry_run": 0, "dry_running": 0,
			"dry_run_passed": 0, "dry_run_blocked": 0, "dismissed": 0, "expired": 0,
		},
		ExperimentsByKind: map[string]int64{"shadow": 0, "dry_run": 0},
		IngestRunsByResult: map[string]int64{
			"succeeded": 0, "partial": 0, "failed": 0,
		},
		CollectionRunDurationSeconds: map[string]operationalmetrics.DurationSummary{
			"succeeded": newOperationalDurationSummary(), "partial": newOperationalDurationSummary(), "failed": newOperationalDurationSummary(),
		},
		CollectionFactsByResult:   map[string]int64{"succeeded": 0, "partial": 0, "failed": 0},
		CollectionCoverageByLevel: map[string]int64{"complete": 0, "partial": 0, "unavailable": 0},
		IngestFactsByOutcome:      map[string]int64{"accepted": 0, "duplicate": 0},
		ChangeEventsByType: map[string]int64{
			"balance_low": 0, "balance_recovered": 0, "group_added": 0, "group_changed": 0, "group_removed": 0,
			"model_added": 0, "price_increased": 0, "price_decreased": 0, "model_removed": 0, "source_stale": 0, "source_recovered": 0,
		},
		ReconcileRunsByKind:  map[string]int64{"dry_run": 0, "apply": 0, "rollback": 0},
		SecurityEventsByKind: map[string]int64{"credential_leak_detected": 0, "cross_owner_rejected": 0},
		ConnectorTasksByState: map[string]int64{
			"pending": 0, "leased": 0, "succeeded": 0, "failed": 0, "expired": 0,
		},
		RolloutsByStatus: map[string]int64{
			"ready": 0, "applying": 0, "observing": 0, "rollback_required": 0,
			"completed": 0, "rolled_back": 0, "blocked": 0,
		},
		RolloutsByStage:        map[string]int64{"0": 0, "10": 0, "25": 0, "50": 0, "100": 0},
		RolloutStageAgeSeconds: map[string]float64{},
	}
}

func newOperationalDurationSummary() operationalmetrics.DurationSummary {
	buckets := make(map[float64]int64, 9)
	for _, upper := range []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300} {
		buckets[upper] = 0
	}
	return operationalmetrics.DurationSummary{Buckets: buckets}
}

func (s *MemoryStore) GetOperationalMetrics(ctx context.Context, now time.Time) (operationalmetrics.Snapshot, error) {
	if err := ctx.Err(); err != nil || now.IsZero() {
		if err != nil {
			return operationalmetrics.Snapshot{}, err
		}
		return operationalmetrics.Snapshot{}, ErrInvalid
	}
	now = normalizeUpstreamTime(now)
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := newOperationalMetricsSnapshot()

	for _, source := range s.upstreamIntelSources {
		snapshot.SourcesByState[string(source.Status)]++
		if source.Status == contracts.UpstreamSourceActive && source.LastErrorCode != "" {
			snapshot.SourcesByState["failed"]++
		}
		if source.Status == contracts.UpstreamSourceActive && source.LastSuccessAt == nil {
			snapshot.CollectionWithoutSuccess++
		}
		if source.LastSuccessAt != nil && !source.LastSuccessAt.After(now) {
			age := now.Sub(*source.LastSuccessAt).Seconds()
			if snapshot.CollectionLastSuccessAge == nil || age < *snapshot.CollectionLastSuccessAge {
				ageCopy := age
				snapshot.CollectionLastSuccessAge = &ageCopy
			}
		}
		staleAfter := source.LastSuccessAt
		if staleAfter == nil {
			staleAfter = metricsTimePointer(source.CreatedAt)
		}
		if staleAfter != nil {
			interval := time.Duration(source.PollIntervalSeconds*operationalMetricsSourceStaleFactor) * time.Second
			if interval <= 0 {
				interval = 10 * time.Minute
			}
			if !now.Before(staleAfter.Add(interval)) {
				snapshot.SourcesByState["stale"]++
			}
		}
	}
	for _, recommendation := range s.upstreamRecommendations {
		snapshot.RecommendationsByStatus[string(recommendation.Status)]++
	}
	for key, count := range s.operationalMetricCounters {
		switch key.Metric {
		case "collection_runs":
			snapshot.IngestRunsByResult[key.Label] = count
		case "collection_facts":
			snapshot.CollectionFactsByResult[key.Label] = count
		case "collection_coverage":
			snapshot.CollectionCoverageByLevel[key.Label] = count
		case "ingest_facts":
			snapshot.IngestFactsByOutcome[key.Label] = count
		case "change_events":
			snapshot.ChangeEventsByType[key.Label] = count
		case "reconcile_runs":
			snapshot.ReconcileRunsByKind[key.Label] = count
		case "experiments":
			snapshot.ExperimentsByKind[key.Label] = count
		}
	}
	for result, duration := range s.operationalCollectionDurations {
		if _, ok := snapshot.CollectionRunDurationSeconds[result]; ok {
			snapshot.CollectionRunDurationSeconds[result] = duration
		}
	}
	for _, task := range s.connectorTasks {
		status := task.Status
		if (status == contracts.ConnectorTaskPending || status == contracts.ConnectorTaskLeased) && !task.ExpiresAt.IsZero() && !now.Before(task.ExpiresAt) {
			status = contracts.ConnectorTaskExpired
		}
		snapshot.ConnectorTasksByState[string(status)]++
		if status == contracts.ConnectorTaskPending || status == contracts.ConnectorTaskLeased {
			snapshot.OldestConnectorTaskAgeSeconds = maxMetricsAge(snapshot.OldestConnectorTaskAgeSeconds, now, task.CreatedAt)
		}
	}
	for _, job := range s.upstreamCostJobs {
		if job.Status == UpstreamCostJobPending || job.Status == UpstreamCostJobRetrying || job.Status == UpstreamCostJobProcessing {
			snapshot.OldestCostJobAgeSeconds = maxMetricsAge(snapshot.OldestCostJobAgeSeconds, now, job.CreatedAt)
		}
	}
	activeRollouts := make(map[string]struct{}, len(s.recommendationRollouts))
	for _, rollout := range s.recommendationRollouts {
		if contracts.IsRecommendationRolloutStatus(rollout.State.Status) && contracts.IsRecommendationRolloutStage(rollout.State.Stage) {
			snapshot.RolloutsByStatus[string(rollout.State.Status)]++
			snapshot.RolloutsByStage[strconv.Itoa(int(rollout.State.Stage))]++
		}
		if recommendationRolloutActive(rollout.State.Status) {
			activeRollouts[rollout.State.ID] = struct{}{}
		}
		if rollout.State.StageStartedAt != nil && recommendationRolloutActive(rollout.State.Status) {
			stage := strconv.Itoa(int(rollout.State.Stage))
			snapshot.RolloutStageAgeSeconds[stage] = maxMetricsAge(snapshot.RolloutStageAgeSeconds[stage], now, *rollout.State.StageStartedAt)
		}
	}
	latestOperation := make(map[string]contracts.RecommendationRolloutOperation)
	latestRollbackOperation := make(map[string]contracts.RecommendationRolloutOperation)
	for _, operation := range s.recommendationRolloutOperations {
		if _, active := activeRollouts[operation.RolloutID]; !active {
			continue
		}
		if current, exists := latestOperation[operation.RolloutID]; !exists || operationalMetricsOperationNewer(operation, current) {
			latestOperation[operation.RolloutID] = operation
		}
		if operation.Action == contracts.RecommendationRolloutOperationRollback {
			if current, exists := latestRollbackOperation[operation.RolloutID]; !exists || operationalMetricsOperationNewer(operation, current) {
				latestRollbackOperation[operation.RolloutID] = operation
			}
		}
	}
	for _, operation := range latestOperation {
		if operation.Status == contracts.RecommendationRolloutOperationPending || operation.Status == contracts.RecommendationRolloutOperationRunning || operation.Status == contracts.RecommendationRolloutOperationFailed {
			snapshot.OldestRolloutActionAgeSeconds = maxMetricsAge(snapshot.OldestRolloutActionAgeSeconds, now, operation.UpdatedAt)
		}
	}
	for _, operation := range latestRollbackOperation {
		if operation.Status == contracts.RecommendationRolloutOperationFailed {
			snapshot.RollbackFailureCount++
		}
	}
	for kind, count := range s.operationalEventCounters {
		switch kind {
		case operationalEventFalseRemovalInvariant:
			snapshot.FalseRemovalViolations = count
		case operationalEventCredentialLeakDetected, operationalEventCrossOwnerRejected:
			snapshot.SecurityEventsByKind[string(kind)] = count
		}
	}

	finalized := make(map[string]struct{}, len(s.upstreamIntelRuns))
	for _, run := range s.upstreamIntelRuns {
		if run.FinalizedFactVersion > 0 {
			finalized[memoryUpstreamFinalizationKey(run.UserID, run.ID)] = struct{}{}
		}
	}
	latestOffers := make(map[string]contracts.UpstreamOfferObservation)
	for _, offer := range s.upstreamIntelOffers {
		if _, ok := finalized[memoryUpstreamFinalizationKey(offer.UserID, offer.RunID)]; !ok {
			continue
		}
		key := operationalMetricsOfferKey(offer)
		if current, exists := latestOffers[key]; !exists || upstreamReadOfferNewer(offer, current) {
			latestOffers[key] = offer
		}
	}
	var comparable int64
	for _, offer := range latestOffers {
		if !now.Before(offer.FreshUntil) || offer.ValidUntil != nil && !now.Before(*offer.ValidUntil) {
			snapshot.StaleEvidenceCount++
			continue
		}
		if (offer.Accuracy == contracts.UpstreamEvidenceExact || offer.Accuracy == contracts.UpstreamEvidenceDerived) &&
			offer.Coverage == contracts.UpstreamCoverageComplete && offer.EffectiveUnitCost != nil && offer.SettlementCurrency != "" {
			comparable++
		}
	}
	if len(latestOffers) > 0 {
		coverage := float64(comparable) / float64(len(latestOffers))
		snapshot.FreshComparableCoverage = &coverage
	}
	return snapshot, nil
}

func addOperationalDuration(values map[string]operationalmetrics.DurationSummary, result string, seconds float64) {
	if seconds < 0 {
		return
	}
	summary, ok := values[result]
	if !ok {
		return
	}
	summary.Count++
	summary.Sum += seconds
	for upper := range summary.Buckets {
		if seconds <= upper {
			summary.Buckets[upper]++
		}
	}
	values[result] = summary
}

func operationalMetricsOfferKey(value contracts.UpstreamOfferObservation) string {
	return strconv.FormatInt(value.UserID, 10) + "\x00" + upstreamReadOfferKey(value)
}

func operationalMetricsOperationNewer(left, right contracts.RecommendationRolloutOperation) bool {
	return left.UpdatedAt.After(right.UpdatedAt) || left.UpdatedAt.Equal(right.UpdatedAt) && left.ID > right.ID
}

func maxMetricsAge(current float64, now, createdAt time.Time) float64 {
	if createdAt.IsZero() || createdAt.After(now) {
		return current
	}
	age := now.Sub(createdAt).Seconds()
	if age > current {
		return age
	}
	return current
}

func metricsTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

package store

import (
	"context"
	"encoding/json"
	"time"

	"e2m.local/core/internal/operationalmetrics"
)

// GetOperationalMetrics performs one bounded PostgreSQL round trip. Counters
// come from retention-independent aggregate tables; current-state gauges come
// from bounded aggregate CTEs. Every label belongs to a closed vocabulary.
func (s *PostgresStore) GetOperationalMetrics(ctx context.Context, now time.Time) (operationalmetrics.Snapshot, error) {
	if now.IsZero() {
		return operationalmetrics.Snapshot{}, ErrInvalid
	}
	now = normalizeUpstreamTime(now)
	snapshot := newOperationalMetricsSnapshot()
	var sources, recommendations, counters, connectorTasks, rolloutStatuses, rolloutStages, rolloutStageAges, events []byte
	var durationRows []byte
	var comparableCoverageCount, totalCoverageCount int64
	var lastSuccessAge *float64
	if err := s.pool.QueryRow(ctx, `WITH
	 source_counts AS (
	   SELECT jsonb_object_agg(state,count) values FROM (
	     SELECT state,count(*)::bigint count FROM (
	       SELECT status state FROM upstream_intelligence_sources
	       UNION ALL SELECT 'failed' FROM upstream_intelligence_sources WHERE status='active' AND last_error_code<>''
	       UNION ALL SELECT 'stale' FROM upstream_intelligence_sources
	         WHERE $1 >= COALESCE(last_success_at,created_at) +
	           (GREATEST(poll_interval_seconds*$2,600)::bigint * interval '1 second')
	     ) source_states GROUP BY state
	   ) grouped
	 ), recommendation_counts AS (
	   SELECT jsonb_object_agg(status,count) values FROM (
	     SELECT status,count(*)::bigint count FROM upstream_recommendations GROUP BY status
	   ) grouped
	 ), metric_counts AS (
	   SELECT jsonb_object_agg(metric||':'||label,total) values FROM operational_metric_counters
	 ), duration_counts AS (
	   SELECT COALESCE(jsonb_agg(jsonb_build_object(
	     'result',result,'count',count,'sum',sum_seconds,
	     'b01',le_0_1,'b05',le_0_5,'b1',le_1,'b2',le_2,'b5',le_5,
	     'b10',le_10,'b30',le_30,'b60',le_60,'b300',le_300
	   ) ORDER BY result),'[]'::jsonb) values FROM operational_collection_duration_counters
	 ), connector_counts AS (
	   SELECT jsonb_object_agg(state,count) values FROM (
	     SELECT state,count(*)::bigint count FROM (
	       SELECT CASE WHEN status IN ('pending','leased') AND expires_at<=$1 THEN 'expired' ELSE status END state
	       FROM connector_tasks
	     ) tasks GROUP BY state
	   ) grouped
	 ), rollout_status_counts AS (
	   SELECT jsonb_object_agg(status,count) values FROM (
	     SELECT status,count(*)::bigint count FROM recommendation_rollouts GROUP BY status
	   ) grouped
	 ), rollout_stage_counts AS (
	   SELECT jsonb_object_agg(stage,count) values FROM (
	     SELECT stage::text stage,count(*)::bigint count FROM recommendation_rollouts GROUP BY stage
	   ) grouped
	 ), rollout_stage_ages AS (
	   SELECT jsonb_object_agg(stage,age) values FROM (
	     SELECT stage::text stage,GREATEST(EXTRACT(EPOCH FROM ($1-MIN(stage_started_at))),0)::float8 age
	       FROM recommendation_rollouts
	      WHERE status IN ('ready','applying','observing','rollback_required','blocked') AND stage_started_at IS NOT NULL
	      GROUP BY stage
	   ) grouped
	 ), task_ages AS (
	   SELECT GREATEST(EXTRACT(EPOCH FROM ($1-MIN(created_at))),0)::float8 oldest
	   FROM connector_tasks WHERE status IN ('pending','leased') AND expires_at>$1
	 ), cost_ages AS (
	   SELECT GREATEST(EXTRACT(EPOCH FROM ($1-MIN(created_at))),0)::float8 oldest
	   FROM upstream_cost_attribution_jobs WHERE status IN ('pending','retrying','processing')
	 ), rollout_ages AS (
	   SELECT GREATEST(EXTRACT(EPOCH FROM ($1-MIN(updated_at))),0)::float8 oldest
	   FROM (
	     SELECT DISTINCT ON (operation.rollout_id) operation.rollout_id,operation.status,operation.updated_at,operation.id
	     FROM recommendation_rollout_operations operation
	     JOIN recommendation_rollouts rollout ON rollout.id=operation.rollout_id
	     WHERE rollout.status IN ('ready','applying','observing','rollback_required','blocked')
	     ORDER BY operation.rollout_id,operation.updated_at DESC,operation.id DESC
	   ) latest WHERE status IN ('pending','running','failed')
	 ), rollback_failures AS (
	   SELECT count(*)::bigint count FROM (
	     SELECT DISTINCT ON (operation.rollout_id) operation.rollout_id,operation.status FROM recommendation_rollout_operations operation
	     JOIN recommendation_rollouts rollout ON rollout.id=operation.rollout_id
	     WHERE operation.action='rollback' AND rollout.status IN ('ready','applying','observing','rollback_required','blocked')
	     ORDER BY operation.rollout_id,operation.updated_at DESC,operation.id DESC
	   ) latest WHERE status='failed'
	 ), current_offers AS (
	   SELECT DISTINCT ON (offer.user_id,offer.source_id,offer.group_key,offer.model_key,offer.price_dimension,offer.settlement_currency,offer.per_tokens)
	     offer.* FROM upstream_offer_observations offer
	   JOIN upstream_collection_runs run ON run.user_id=offer.user_id AND run.id=offer.run_id AND run.finalized_fact_version>0
	   ORDER BY offer.user_id,offer.source_id,offer.group_key,offer.model_key,offer.price_dimension,offer.settlement_currency,offer.per_tokens,
	            offer.observed_at DESC,offer.run_id DESC
	 ), coverage AS (
	   SELECT count(*)::bigint total,
	     count(*) FILTER (WHERE fresh_until>$1 AND (valid_until IS NULL OR valid_until>$1)
	       AND accuracy IN ('exact','derived') AND coverage='complete' AND effective_unit_cost IS NOT NULL AND settlement_currency<>'')::bigint comparable,
	     count(*) FILTER (WHERE fresh_until<=$1 OR valid_until IS NOT NULL AND valid_until<=$1)::bigint stale
	   FROM current_offers
	 ), collection_health AS (
	   SELECT MIN(EXTRACT(EPOCH FROM ($1-last_success_at))) FILTER (WHERE last_success_at IS NOT NULL AND last_success_at<=$1)::float8 last_success_age,
	          count(*) FILTER (WHERE status='active' AND last_success_at IS NULL)::bigint without_success
	     FROM upstream_intelligence_sources
	 ), event_counts AS (
	   SELECT COALESCE(jsonb_object_agg(kind,total),'{}'::jsonb) values FROM operational_event_counters
	 )
	 SELECT COALESCE((SELECT values FROM source_counts),'{}'::jsonb),
	        COALESCE((SELECT values FROM recommendation_counts),'{}'::jsonb),
	        COALESCE((SELECT values FROM metric_counts),'{}'::jsonb),
	        (SELECT values FROM duration_counts),
	        COALESCE((SELECT values FROM connector_counts),'{}'::jsonb),
	        COALESCE((SELECT values FROM rollout_status_counts),'{}'::jsonb),
	        COALESCE((SELECT values FROM rollout_stage_counts),'{}'::jsonb),
	        COALESCE((SELECT values FROM rollout_stage_ages),'{}'::jsonb),
	        COALESCE((SELECT values FROM event_counts),'{}'::jsonb),
	        COALESCE((SELECT oldest FROM task_ages),0),COALESCE((SELECT oldest FROM cost_ages),0),
	        COALESCE((SELECT oldest FROM rollout_ages),0),(SELECT count FROM rollback_failures),
	        (SELECT stale FROM coverage),(SELECT comparable FROM coverage),(SELECT total FROM coverage),
	        (SELECT last_success_age FROM collection_health),(SELECT without_success FROM collection_health)`,
		now, operationalMetricsSourceStaleFactor).Scan(
		&sources, &recommendations, &counters, &durationRows, &connectorTasks, &rolloutStatuses, &rolloutStages,
		&rolloutStageAges, &events, &snapshot.OldestConnectorTaskAgeSeconds, &snapshot.OldestCostJobAgeSeconds,
		&snapshot.OldestRolloutActionAgeSeconds, &snapshot.RollbackFailureCount, &snapshot.StaleEvidenceCount,
		&comparableCoverageCount, &totalCoverageCount, &lastSuccessAge, &snapshot.CollectionWithoutSuccess,
	); err != nil {
		return operationalmetrics.Snapshot{}, err
	}
	mergeClosedMetricCounts(snapshot.SourcesByState, sources)
	mergeClosedMetricCounts(snapshot.RecommendationsByStatus, recommendations)
	mergeOperationalCounters(&snapshot, counters)
	mergeOperationalDurations(snapshot.CollectionRunDurationSeconds, durationRows)
	mergeClosedMetricCounts(snapshot.ConnectorTasksByState, connectorTasks)
	mergeClosedMetricCounts(snapshot.RolloutsByStatus, rolloutStatuses)
	mergeClosedMetricCounts(snapshot.RolloutsByStage, rolloutStages)
	mergeClosedMetricFloatCounts(snapshot.RolloutStageAgeSeconds, rolloutStageAges)
	mergeOperationalEvents(&snapshot, events)
	snapshot.CollectionLastSuccessAge = lastSuccessAge
	if totalCoverageCount > 0 {
		coverage := float64(comparableCoverageCount) / float64(totalCoverageCount)
		snapshot.FreshComparableCoverage = &coverage
	}
	return snapshot, nil
}

func mergeClosedMetricCounts(target map[string]int64, raw []byte) {
	if len(raw) == 0 {
		return
	}
	var values map[string]int64
	if json.Unmarshal(raw, &values) != nil {
		return
	}
	for key := range target {
		if value, ok := values[key]; ok && value >= 0 {
			target[key] = value
		}
	}
}

func mergeClosedMetricFloatCounts(target map[string]float64, raw []byte) {
	var values map[string]float64
	if json.Unmarshal(raw, &values) != nil {
		return
	}
	for _, key := range []string{"0", "10", "25", "50", "100"} {
		if value, ok := values[key]; ok && value >= 0 {
			target[key] = value
		}
	}
}

func mergeOperationalCounters(snapshot *operationalmetrics.Snapshot, raw []byte) {
	var values map[string]int64
	if snapshot == nil || json.Unmarshal(raw, &values) != nil {
		return
	}
	targets := map[string]map[string]int64{
		"collection_runs": snapshot.IngestRunsByResult, "collection_facts": snapshot.CollectionFactsByResult,
		"collection_coverage": snapshot.CollectionCoverageByLevel, "ingest_facts": snapshot.IngestFactsByOutcome,
		"change_events": snapshot.ChangeEventsByType, "reconcile_runs": snapshot.ReconcileRunsByKind,
		"experiments": snapshot.ExperimentsByKind,
	}
	for metric, target := range targets {
		for label := range target {
			if value, ok := values[metric+":"+label]; ok && value >= 0 {
				target[label] = value
			}
		}
	}
}

func mergeOperationalDurations(target map[string]operationalmetrics.DurationSummary, raw []byte) {
	type row struct {
		Result string  `json:"result"`
		Count  int64   `json:"count"`
		Sum    float64 `json:"sum"`
		B01    int64   `json:"b01"`
		B05    int64   `json:"b05"`
		B1     int64   `json:"b1"`
		B2     int64   `json:"b2"`
		B5     int64   `json:"b5"`
		B10    int64   `json:"b10"`
		B30    int64   `json:"b30"`
		B60    int64   `json:"b60"`
		B300   int64   `json:"b300"`
	}
	var rows []row
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
	for _, value := range rows {
		if _, ok := target[value.Result]; !ok || value.Count < 0 || value.Sum < 0 {
			continue
		}
		target[value.Result] = operationalmetrics.DurationSummary{Count: value.Count, Sum: value.Sum, Buckets: map[float64]int64{
			0.1: value.B01, 0.5: value.B05, 1: value.B1, 2: value.B2, 5: value.B5,
			10: value.B10, 30: value.B30, 60: value.B60, 300: value.B300,
		}}
	}
}

func mergeOperationalEvents(snapshot *operationalmetrics.Snapshot, raw []byte) {
	var values map[string]int64
	if snapshot == nil || json.Unmarshal(raw, &values) != nil {
		return
	}
	for key := range snapshot.SecurityEventsByKind {
		if value, ok := values[key]; ok && value >= 0 {
			snapshot.SecurityEventsByKind[key] = value
		}
	}
	if value, ok := values[string(operationalEventFalseRemovalInvariant)]; ok && value >= 0 {
		snapshot.FalseRemovalViolations = value
	}
}

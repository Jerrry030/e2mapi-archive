// Package operationalmetrics exposes low-cardinality, Prometheus-compatible
// control-plane health metrics. It deliberately aggregates across owners and
// never emits identifiers, URLs, credentials, or arbitrary error text.
package operationalmetrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"e2m.local/contracts"
)

const (
	defaultCollectionTimeout = 5 * time.Second
)

// Snapshot is an already-aggregated view. Every map key belongs to a closed
// vocabulary; no caller-provided or upstream-provided value is used as a label.
type Snapshot struct {
	SourcesByState                map[string]int64
	RecommendationsByStatus       map[string]int64
	ExperimentsByKind             map[string]int64
	IngestRunsByResult            map[string]int64
	CollectionRunDurationSeconds  map[string]DurationSummary
	CollectionFactsByResult       map[string]int64
	CollectionCoverageByLevel     map[string]int64
	CollectionLastSuccessAge      *float64
	CollectionWithoutSuccess      int64
	IngestFactsByOutcome          map[string]int64
	ChangeEventsByType            map[string]int64
	ReconcileRunsByKind           map[string]int64
	ConnectorTasksByState         map[string]int64
	StaleEvidenceCount            int64
	OldestCostJobAgeSeconds       float64
	OldestConnectorTaskAgeSeconds float64
	FreshComparableCoverage       *float64
	RolloutsByStatus              map[string]int64
	RolloutsByStage               map[string]int64
	OldestRolloutActionAgeSeconds float64
	RollbackFailureCount          int64
	RolloutStageAgeSeconds        map[string]float64
	FalseRemovalViolations        int64
	SecurityEventsByKind          map[string]int64
}

// DurationSummary is a cumulative histogram projection. Count and Sum cover
// the same finalized collection-run population; Buckets are cumulative upper
// bounds in seconds. Unknown durations are excluded instead of becoming zero.
type DurationSummary struct {
	Count   int64
	Sum     float64
	Buckets map[float64]int64
}

// metricLabelAllowlist is the final cardinality boundary for every labelled
// metric. Store queries are deliberately aggregated, but their group values
// still originate in persisted data. Keep this vocabulary local to the
// exporter so a corrupt/legacy row cannot become a new Prometheus time series.
var metricLabelAllowlist = map[string]map[string]struct{}{
	"e2m_upstream_sources": labelSet(
		"active", "paused", "disconnected", "stale", "failed",
	),
	"e2m_upstream_recommendations": labelSet(
		"open", "shadowing", "ready_for_dry_run", "dry_running",
		"dry_run_passed", "dry_run_blocked", "dismissed", "expired",
	),
	"e2m_upstream_experiments_total": labelSet("shadow", "dry_run"),
	"e2m_upstream_ingest_runs_total": labelSet(
		"succeeded", "partial", "failed",
	),
	"e2m_upstream_collection_run_duration_seconds": labelSet(
		"succeeded", "partial", "failed",
	),
	"e2m_upstream_collection_facts_total": labelSet(
		"succeeded", "partial", "failed",
	),
	"e2m_upstream_collection_runs_by_coverage_total": labelSet(
		"complete", "partial", "unavailable",
	),
	"e2m_upstream_ingest_facts_total": labelSet(
		"accepted", "duplicate",
	),
	"e2m_upstream_change_events_total": labelSet(
		"balance_low", "balance_recovered", "group_added", "group_changed", "group_removed",
		"model_added", "price_increased", "price_decreased", "model_removed", "source_stale", "source_recovered",
	),
	"e2m_upstream_reconcile_runs_total":  labelSet("dry_run", "apply", "rollback"),
	"e2m_upstream_security_events_total": labelSet("credential_leak_detected", "cross_owner_rejected"),
	"e2m_connector_tasks": labelSet(
		"pending", "leased", "succeeded", "failed", "expired",
	),
	"e2m_recommendation_rollouts": labelSet(
		"ready", "applying", "observing", "rollback_required",
		"completed", "rolled_back", "blocked",
	),
	"e2m_recommendation_rollout_stages": labelSet("0", "10", "25", "50", "100"),
}

var collectionDurationBuckets = []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300}

// Store is implemented by MemoryStore and PostgresStore using a single
// bounded aggregate read. It is intentionally separate from store.Store.
type Store interface {
	GetOperationalMetrics(context.Context, time.Time) (Snapshot, error)
}

// Collector serves one scrape at a time from current store facts.
type Collector struct {
	store   Store
	now     func() time.Time
	timeout time.Duration
}

func New(st any) *Collector {
	metricsStore, _ := st.(Store)
	return &Collector{
		store: metricsStore,
		now:   func() time.Time { return time.Now().UTC() }, timeout: defaultCollectionTimeout,
	}
}

func (c *Collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c == nil || c.store == nil || c.now == nil {
		http.Error(w, "metrics unavailable", http.StatusServiceUnavailable)
		return
	}
	timeout := c.timeout
	if timeout <= 0 {
		timeout = defaultCollectionTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	now := c.now().UTC()
	snapshot, err := c.store.GetOperationalMetrics(ctx, now)
	if err != nil {
		http.Error(w, "metrics collection failed", http.StatusServiceUnavailable)
		return
	}
	if err := writeSnapshot(w, snapshot); err != nil {
		return
	}
}

func writeSnapshot(w io.Writer, snapshot Snapshot) error {
	if snapshot.RolloutsByStatus == nil {
		snapshot.RolloutsByStatus = map[string]int64{}
	}
	if snapshot.RolloutsByStage == nil {
		snapshot.RolloutsByStage = map[string]int64{}
	}
	sections := []struct {
		help, name, label, metricType string
		values                        map[string]int64
	}{
		{"Sanitized upstream intelligence sources by operational state.", "e2m_upstream_sources", "state", "gauge", snapshot.SourcesByState},
		{"Recommendations by closed lifecycle status.", "e2m_upstream_recommendations", "status", "gauge", snapshot.RecommendationsByStatus},
		{"Immutable recommendation experiments by kind.", "e2m_upstream_experiments_total", "kind", "counter", snapshot.ExperimentsByKind},
		{"Finalized collection runs by closed result.", "e2m_upstream_ingest_runs_total", "result", "counter", snapshot.IngestRunsByResult},
		{"Facts declared by finalized collection runs by result.", "e2m_upstream_collection_facts_total", "result", "counter", snapshot.CollectionFactsByResult},
		{"Finalized collection runs by closed evidence coverage.", "e2m_upstream_collection_runs_by_coverage_total", "coverage", "counter", snapshot.CollectionCoverageByLevel},
		{"Durably ingested facts by idempotent outcome.", "e2m_upstream_ingest_facts_total", "outcome", "counter", snapshot.IngestFactsByOutcome},
		{"Durable upstream change events by closed type.", "e2m_upstream_change_events_total", "type", "counter", snapshot.ChangeEventsByType},
		{"Reconcile executions by closed kind.", "e2m_upstream_reconcile_runs_total", "kind", "counter", snapshot.ReconcileRunsByKind},
		{"Durable Connector tasks by closed state.", "e2m_connector_tasks", "state", "gauge", snapshot.ConnectorTasksByState},
		{"Recommendation rollouts by closed status.", "e2m_recommendation_rollouts", "status", "gauge", snapshot.RolloutsByStatus},
		{"Recommendation rollouts by admitted traffic stage.", "e2m_recommendation_rollout_stages", "stage", "gauge", snapshot.RolloutsByStage},
	}
	for _, section := range sections {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", section.name, section.help, section.name, section.metricType); err != nil {
			return err
		}
		keys := make([]string, 0, len(section.values))
		for key := range section.values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !allowedMetricLabel(section.name, key) {
				continue
			}
			if _, err := fmt.Fprintf(w, "%s{%s=%q} %d\n", section.name, section.label, key, maxInt64(section.values[key], 0)); err != nil {
				return err
			}
		}
	}
	if err := writeCollectionDurationHistogram(w, snapshot.CollectionRunDurationSeconds); err != nil {
		return err
	}
	scalars := []struct {
		help, name string
		value      float64
	}{
		{"Current evidence rows past their freshness deadline.", "e2m_upstream_stale_evidence", float64(maxInt64(snapshot.StaleEvidenceCount, 0))},
		{"Age of the oldest actionable cost-attribution job.", "e2m_upstream_cost_job_oldest_age_seconds", nonNegativeFinite(snapshot.OldestCostJobAgeSeconds)},
		{"Age of the oldest pending or leased Connector task.", "e2m_connector_task_oldest_age_seconds", nonNegativeFinite(snapshot.OldestConnectorTaskAgeSeconds)},
		{"Age of the oldest actionable recommendation rollout.", "e2m_recommendation_rollout_oldest_action_age_seconds", nonNegativeFinite(snapshot.OldestRolloutActionAgeSeconds)},
		{"Recommendation rollouts whose latest rollback attempt failed.", "e2m_recommendation_rollback_failures", float64(maxInt64(snapshot.RollbackFailureCount, 0))},
		{"Active upstream intelligence sources that have never completed a successful collection.", "e2m_upstream_collection_sources_without_success", float64(maxInt64(snapshot.CollectionWithoutSuccess, 0))},
	}
	if _, err := fmt.Fprintln(w, "# HELP e2m_upstream_collection_last_success_age_seconds Age of the newest successful upstream collection.\n# TYPE e2m_upstream_collection_last_success_age_seconds gauge"); err != nil {
		return err
	}
	if snapshot.CollectionLastSuccessAge != nil {
		if _, err := fmt.Fprintf(w, "e2m_upstream_collection_last_success_age_seconds %s\n", strconv.FormatFloat(nonNegativeFinite(*snapshot.CollectionLastSuccessAge), 'f', -1, 64)); err != nil {
			return err
		}
	}
	if err := writeFloatMap(w, "Time spent in the current admitted rollout stage.", "e2m_recommendation_rollout_stage_age_seconds", "stage", snapshot.RolloutStageAgeSeconds, "gauge"); err != nil {
		return err
	}
	if err := writeIntMap(w, "Durable upstream security boundary events.", "e2m_upstream_security_events_total", "kind", snapshot.SecurityEventsByKind, "counter"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "# HELP e2m_upstream_false_removal_invariant_violations_total Durable false-removal safety invariant violations.\n# TYPE e2m_upstream_false_removal_invariant_violations_total counter\ne2m_upstream_false_removal_invariant_violations_total %d\n", maxInt64(snapshot.FalseRemovalViolations, 0)); err != nil {
		return err
	}
	for _, scalar := range scalars {
		if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", scalar.name, scalar.help, scalar.name, scalar.name, strconv.FormatFloat(scalar.value, 'f', -1, 64)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, "# HELP e2m_upstream_fresh_comparable_coverage_ratio Fraction of current rate facts that are fresh and comparable.\n# TYPE e2m_upstream_fresh_comparable_coverage_ratio gauge"); err != nil {
		return err
	}
	if snapshot.FreshComparableCoverage != nil && validRatio(*snapshot.FreshComparableCoverage) {
		value := *snapshot.FreshComparableCoverage
		_, err := fmt.Fprintf(w, "e2m_upstream_fresh_comparable_coverage_ratio %s\n", strconv.FormatFloat(value, 'f', -1, 64))
		return err
	}
	return nil
}

func writeCollectionDurationHistogram(w io.Writer, values map[string]DurationSummary) error {
	const name = "e2m_upstream_collection_run_duration_seconds"
	if _, err := fmt.Fprintf(w, "# HELP %s Finalized upstream collection duration in seconds.\n# TYPE %s histogram\n", name, name); err != nil {
		return err
	}
	keys := sortedAllowedKeys(name, values)
	for _, key := range keys {
		value := values[key]
		for _, upper := range collectionDurationBuckets {
			if _, err := fmt.Fprintf(w, "%s_bucket{result=%q,le=%q} %d\n", name, key, strconv.FormatFloat(upper, 'f', -1, 64), maxInt64(value.Buckets[upper], 0)); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%s_bucket{result=%q,le=%q} %d\n%s_sum{result=%q} %s\n%s_count{result=%q} %d\n",
			name, key, "+Inf", maxInt64(value.Count, 0), name, key, strconv.FormatFloat(nonNegativeFinite(value.Sum), 'f', -1, 64), name, key, maxInt64(value.Count, 0)); err != nil {
			return err
		}
	}
	return nil
}

func writeIntMap(w io.Writer, help, name, label string, values map[string]int64, metricType string) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType); err != nil {
		return err
	}
	for _, key := range sortedAllowedKeys(name, values) {
		if _, err := fmt.Fprintf(w, "%s{%s=%q} %d\n", name, label, key, maxInt64(values[key], 0)); err != nil {
			return err
		}
	}
	return nil
}

func writeFloatMap(w io.Writer, help, name, label string, values map[string]float64, metricType string) error {
	if _, err := fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType); err != nil {
		return err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if key == "0" || key == "10" || key == "25" || key == "50" || key == "100" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(w, "%s{%s=%q} %s\n", name, label, key, strconv.FormatFloat(nonNegativeFinite(values[key]), 'f', -1, 64)); err != nil {
			return err
		}
	}
	return nil
}

func sortedAllowedKeys[T any](metric string, values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if allowedMetricLabel(metric, key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func labelSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func allowedMetricLabel(metric, value string) bool {
	allowed, ok := metricLabelAllowlist[metric]
	if !ok || !safeLabelValue(value) {
		return false
	}
	_, ok = allowed[value]
	return ok
}

func safeLabelValue(value string) bool {
	if value == "" || len(value) > 64 || contracts.LooksLikeConnectorSensitiveValue(value) {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return !strings.Contains(value, "error:")
}

func nonNegativeFinite(value float64) float64 {
	if value < 0 || value != value || value > 1.7976931348623157e+308 {
		return 0
	}
	return value
}

func validRatio(value float64) bool {
	return value >= 0 && value <= 1 && value == value
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

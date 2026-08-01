package operationalmetrics

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type metricsStoreStub struct {
	snapshot Snapshot
	err      error
}

func (s metricsStoreStub) GetOperationalMetrics(context.Context, time.Time) (Snapshot, error) {
	return s.snapshot, s.err
}

func TestCollectorEmitsOnlyLowCardinalityClosedLabels(t *testing.T) {
	coverage := 0.75
	collector := New(metricsStoreStub{snapshot: Snapshot{
		SourcesByState: map[string]int64{
			"active": 2, "customer_alpha": 98, "https://must-not-leak.invalid": 99,
		},
		RecommendationsByStatus: map[string]int64{"open": 3, "approved": 6, "Bearer secret": 7},
		ExperimentsByKind:       map[string]int64{"shadow": 4, "dry_run": 5, "customer_alpha": 9},
		IngestRunsByResult:      map[string]int64{"succeeded": 8, "running": 9, "timed_out": 10},
		ConnectorTasksByState:   map[string]int64{"pending": 1, "cancelled": 11},
		StaleEvidenceCount:      6, OldestCostJobAgeSeconds: 12,
		OldestConnectorTaskAgeSeconds: 21, FreshComparableCoverage: &coverage,
		RolloutsByStatus:              map[string]int64{"rollback_required": 1, "paused": 12},
		RolloutsByStage:               map[string]int64{"25": 1, "75": 13},
		OldestRolloutActionAgeSeconds: 30, RollbackFailureCount: 1,
	}})
	collector.now = func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) }

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	collector.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, required := range []string{
		`e2m_upstream_sources{state="active"} 2`,
		`e2m_upstream_recommendations{status="open"} 3`,
		`e2m_upstream_experiments_total{kind="dry_run"} 5`,
		`e2m_recommendation_rollouts{status="rollback_required"} 1`,
		`e2m_recommendation_rollout_stages{stage="25"} 1`,
		`e2m_upstream_fresh_comparable_coverage_ratio 0.75`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("metrics lacks %q:\n%s", required, body)
		}
	}
	for _, forbidden := range []string{
		"must-not-leak", "Bearer secret", "customer_alpha", "approved", "running", "timed_out",
		"cancelled", `status="paused"`, `stage="75"`, "owner=", "source_id=", "plan_id=",
		"instance_id=", "url=",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("metrics leaked forbidden label/value %q: %s", forbidden, body)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("metrics response must be no-store: %q", response.Header().Get("Cache-Control"))
	}
}

func TestCollectorOmitsUnknownFreshComparableCoverageInsteadOfGuessingZero(t *testing.T) {
	invalid := []struct {
		name  string
		value *float64
	}{
		{name: "missing", value: nil},
		{name: "negative", value: float64Pointer(-0.1)},
		{name: "above_one", value: float64Pointer(1.1)},
		{name: "nan", value: float64Pointer(math.NaN())},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			New(metricsStoreStub{snapshot: Snapshot{FreshComparableCoverage: test.value}}).ServeHTTP(
				response,
				httptest.NewRequest(http.MethodGet, "/metrics", nil),
			)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			for _, line := range strings.Split(response.Body.String(), "\n") {
				if strings.HasPrefix(line, "e2m_upstream_fresh_comparable_coverage_ratio ") {
					t.Fatalf("unknown coverage must have no sample, got %q", line)
				}
			}
		})
	}
}

func float64Pointer(value float64) *float64 {
	return &value
}

func TestCollectorFailsClosedWithoutStoreOrOnCollectionError(t *testing.T) {
	for name, collector := range map[string]*Collector{
		"missing": New(nil),
		"failed":  New(metricsStoreStub{err: errors.New("database endpoint https://must-not-leak.invalid")}),
	} {
		t.Run(name, func(t *testing.T) {
			response := httptest.NewRecorder()
			collector.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
			if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "must-not-leak") {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCollectorRejectsNonGET(t *testing.T) {
	response := httptest.NewRecorder()
	New(metricsStoreStub{}).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("response=%d allow=%q", response.Code, response.Header().Get("Allow"))
	}
}

package healthmetrics

import (
	"math"
	"testing"
	"time"

	"e2m.local/contracts"
)

func obs(success bool, ttft, total float64, errType contracts.ObservationErrorType) contracts.ChannelObservation {
	return contracts.ChannelObservation{
		ChannelID:    "ch-1",
		PoolID:       "pool-1",
		InstanceID:   "inst-1",
		Success:      success,
		FirstTokenMS: ttft,
		TotalMS:      total,
		ErrorType:    errType,
		ObservedAt:   time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
	}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// A window with too few samples must stay unknown, never healthy or unhealthy,
// so an idle channel is never mistaken for a failing one.
func TestAggregateBelowMinSamplesIsUnknown(t *testing.T) {
	got := Aggregate("ch-1", contracts.Window5m, []contracts.ChannelObservation{
		obs(true, 100, 500, contracts.ErrorNone),
	}, Thresholds{MinSamples: 5})
	if got.HealthState != contracts.HealthUnknown {
		t.Fatalf("want unknown, got %q", got.HealthState)
	}
	if got.SampleCount != 1 {
		t.Fatalf("want sample_count 1, got %d", got.SampleCount)
	}
	if got.QualitySampleCount != 1 {
		t.Fatalf("want quality_sample_count 1, got %d", got.QualitySampleCount)
	}
}

// Empty input is a well-formed unknown snapshot, not a panic or a zero-sample
// "healthy".
func TestAggregateEmpty(t *testing.T) {
	got := Aggregate("ch-1", contracts.Window1m, nil, DefaultThresholds())
	if got.HealthState != contracts.HealthUnknown || got.SampleCount != 0 {
		t.Fatalf("want empty unknown snapshot, got %+v", got)
	}
	if got.SuccessRate != 0 || got.QualitySuccessRate != 0 || got.TTFTP95 != 0 {
		t.Fatalf("empty snapshot must have zeroed metrics, got %+v", got)
	}
}

func TestAggregateSuccessRateAndErrorRate(t *testing.T) {
	var list []contracts.ChannelObservation
	for i := 0; i < 8; i++ {
		list = append(list, obs(true, 200, 1000, contracts.ErrorNone))
	}
	list = append(list, obs(false, 0, 0, contracts.ErrorServer))
	list = append(list, obs(false, 0, 0, contracts.ErrorTimeout))

	got := Aggregate("ch-1", contracts.Window5m, list, DefaultThresholds())
	if !approx(got.SuccessRate, 0.8) {
		t.Fatalf("success_rate want 0.8, got %v", got.SuccessRate)
	}
	if !approx(got.ErrorRate, 0.2) {
		t.Fatalf("error_rate want 0.2, got %v", got.ErrorRate)
	}
	if got.QualitySampleCount != 10 || !approx(got.QualitySuccessRate, 0.8) || !approx(got.QualityErrorRate, 0.2) {
		t.Fatalf("quality metrics want 10/0.8/0.2, got %d/%v/%v", got.QualitySampleCount, got.QualitySuccessRate, got.QualityErrorRate)
	}
	if !approx(got.TimeoutRate, 0.1) {
		t.Fatalf("timeout_rate want 0.1, got %v", got.TimeoutRate)
	}
}

func TestAggregateAttributesOnlyUpstreamResponsibility(t *testing.T) {
	list := []contracts.ChannelObservation{
		obs(true, 200, 1000, contracts.ErrorNone),
		obs(false, 0, 0, contracts.ErrorTimeout),
		obs(false, 0, 0, contracts.ErrorRateLimit),
		obs(false, 0, 0, contracts.ErrorServer),
		obs(false, 0, 0, contracts.ErrorNetwork),
		obs(false, 0, 0, contracts.ErrorUnknown),
		obs(false, 0, 0, contracts.ErrorClient),
		obs(false, 0, 0, contracts.ErrorCanceled),
		obs(false, 0, 0, contracts.ErrorPlatform),
		obs(false, 0, 0, contracts.ErrorAuth),
		obs(false, 0, 0, contracts.ErrorInsufficientBalance),
	}

	got := Aggregate("ch-1", contracts.Window5m, list, DefaultThresholds())
	if got.UpstreamFailureCount != 5 || !approx(got.UpstreamErrorRate, 0.625) {
		t.Fatalf("upstream responsibility count/rate = %d/%v, want 5/0.625", got.UpstreamFailureCount, got.UpstreamErrorRate)
	}
	if got.AuthFailureCount != 1 || got.InsufficientBalanceCount != 1 {
		t.Fatalf("credential counts auth=%d balance=%d, want 1/1", got.AuthFailureCount, got.InsufficientBalanceCount)
	}
	if got.SampleCount != 11 || !approx(got.SuccessRate, 1.0/11.0) || !approx(got.ErrorRate, 10.0/11.0) {
		t.Fatalf("factual metrics should include every observation, got %d/%v/%v", got.SampleCount, got.SuccessRate, got.ErrorRate)
	}
	if got.QualitySampleCount != 8 || !approx(got.QualitySuccessRate, 0.125) || !approx(got.QualityErrorRate, 0.875) {
		t.Fatalf("quality metrics should exclude client/canceled/platform failures, got %d/%v/%v", got.QualitySampleCount, got.QualitySuccessRate, got.QualityErrorRate)
	}
}

func TestAggregateClientAndCanceledFailuresDoNotLowerQualityState(t *testing.T) {
	list := append(repeat(obs(true, 200, 1000, contracts.ErrorNone), 5),
		repeat(obs(false, 0, 0, contracts.ErrorClient), 20)...)
	list = append(list, repeat(obs(false, 0, 0, contracts.ErrorCanceled), 20)...)

	got := Aggregate("ch-1", contracts.Window5m, list, DefaultThresholds())
	if got.SampleCount != 45 || !approx(got.SuccessRate, 5.0/45.0) || !approx(got.ErrorRate, 40.0/45.0) {
		t.Fatalf("factual SLA must retain downstream failures: %+v", got)
	}
	if got.QualitySampleCount != 5 || got.QualitySuccessRate != 1 || got.QualityErrorRate != 0 || got.UpstreamErrorRate != 0 || got.HealthState != contracts.HealthHealthy {
		t.Fatalf("downstream failures polluted upstream quality: %+v", got)
	}
	if got.SuccessScore != 100 || got.RiskScore != 0 {
		t.Fatalf("downstream failures polluted upstream scores: %+v", got)
	}
}

func TestAggregatePlatformFailuresAreFactualOnly(t *testing.T) {
	list := append(repeat(obs(true, 200, 1000, contracts.ErrorNone), 5),
		repeat(obs(false, 90000, 120000, contracts.ErrorPlatform), 20)...)

	got := Aggregate("ch-1", contracts.Window5m, list, DefaultThresholds())
	if got.SampleCount != 25 || !approx(got.SuccessRate, 0.2) || !approx(got.ErrorRate, 0.8) {
		t.Fatalf("platform failures must remain in factual SLA: %+v", got)
	}
	if got.QualitySampleCount != 5 || got.QualitySuccessRate != 1 || got.QualityErrorRate != 0 ||
		got.UpstreamFailureCount != 0 || got.UpstreamErrorRate != 0 || got.HealthState != contracts.HealthHealthy {
		t.Fatalf("platform failures polluted upstream quality: %+v", got)
	}
	if got.TTFTP95 != 200 || got.DurationP95 != 1000 || got.QualityScore != 100 {
		t.Fatalf("platform latency or failures deducted upstream score: %+v", got)
	}
}

func TestAggregateFactualTrafficDoesNotSatisfyQualityConfidence(t *testing.T) {
	list := append(repeat(obs(true, 200, 1000, contracts.ErrorNone), 1),
		repeat(obs(false, 0, 0, contracts.ErrorClient), 20)...)

	got := Aggregate("ch-1", contracts.Window5m, list, DefaultThresholds())
	if got.SampleCount != 21 || got.QualitySampleCount != 1 {
		t.Fatalf("unexpected factual/quality populations: %+v", got)
	}
	if got.HealthState != contracts.HealthUnknown {
		t.Fatalf("client failures must not satisfy upstream quality confidence: %+v", got)
	}
}

func TestAggregateDoesNotCountErrorLabelOnSuccessfulObservation(t *testing.T) {
	observation := obs(true, 200, 1000, contracts.ErrorServer)
	got := Aggregate("ch-1", contracts.Window5m, []contracts.ChannelObservation{observation}, DefaultThresholds())
	if got.UpstreamFailureCount != 0 || got.UpstreamErrorRate != 0 {
		t.Fatalf("successful observation must not be attributed as failure: %+v", got)
	}
}

// Percentiles must ignore zero/failed latencies (a failed request with no first
// token is not a fast request) and interpolate between ranks.
func TestAggregatePercentiles(t *testing.T) {
	var list []contracts.ChannelObservation
	for _, v := range []float64{100, 200, 300, 400, 500, 600, 700, 800, 900, 1000} {
		list = append(list, obs(true, v, v*10, contracts.ErrorNone))
	}
	// Two failures with no latency must not drag the percentiles down.
	list = append(list, obs(false, 0, 0, contracts.ErrorServer))
	list = append(list, obs(false, 0, 0, contracts.ErrorServer))

	got := Aggregate("ch-1", contracts.Window5m, list, DefaultThresholds())
	// R-7 on 10 points: p50 rank=4.5 -> 550, p95 rank=8.55 -> 955.
	if !approx(got.TTFTP50, 550) {
		t.Fatalf("ttft_p50 want 550, got %v", got.TTFTP50)
	}
	if !approx(got.TTFTP95, 955) {
		t.Fatalf("ttft_p95 want 955, got %v", got.TTFTP95)
	}
	if !approx(got.DurationP95, 9550) {
		t.Fatalf("duration_p95 want 9550, got %v", got.DurationP95)
	}
}

func TestAggregateLatencyExcludesClientAndCanceledFailures(t *testing.T) {
	list := []contracts.ChannelObservation{
		obs(true, 100, 1000, contracts.ErrorNone),
		obs(true, 200, 2000, contracts.ErrorNone),
		obs(false, 100000, 200000, contracts.ErrorClient),
		obs(false, 200000, 400000, contracts.ErrorCanceled),
	}

	got := Aggregate("ch-1", contracts.Window5m, list, Thresholds{MinSamples: 1})
	// Only 100/200 and 1000/2000 are eligible. R-7 p95 is 195 and 1950.
	if !approx(got.TTFTP95, 195) || !approx(got.DurationP95, 1950) {
		t.Fatalf("client/canceled latency polluted percentiles: ttft=%v duration=%v", got.TTFTP95, got.DurationP95)
	}
	if got.UpstreamFailureCount != 0 || got.UpstreamErrorRate != 0 {
		t.Fatalf("client/canceled failures must not be upstream responsibility: %+v", got)
	}
}

func TestAggregateLatencyIncludesUpstreamFailureTiming(t *testing.T) {
	list := []contracts.ChannelObservation{
		obs(true, 100, 1000, contracts.ErrorNone),
		obs(false, 300, 3000, contracts.ErrorServer),
	}
	got := Aggregate("ch-1", contracts.Window5m, list, Thresholds{MinSamples: 1})
	if !approx(got.TTFTP95, 290) || !approx(got.DurationP95, 2900) {
		t.Fatalf("upstream failure timing should remain eligible: ttft=%v duration=%v", got.TTFTP95, got.DurationP95)
	}
}

func TestPercentileEdgeCases(t *testing.T) {
	if percentile(nil, 0.95) != 0 {
		t.Fatal("empty percentile must be 0")
	}
	if percentile([]float64{42}, 0.95) != 42 {
		t.Fatal("single-value percentile must be that value")
	}
	if got := percentile([]float64{10, 20}, 0); got != 10 {
		t.Fatalf("q=0 must be min, got %v", got)
	}
	if got := percentile([]float64{10, 20}, 1); got != 20 {
		t.Fatalf("q=1 must be max, got %v", got)
	}
}

// State transition: a clean window is healthy; a success-rate dip to the
// degraded band is degraded; a dip below the floor is unhealthy.
func TestAggregateStateBySuccessRate(t *testing.T) {
	th := DefaultThresholds() // target 0.95, floor 0.85

	healthy := repeat(obs(true, 200, 1000, contracts.ErrorNone), 20)
	if s := Aggregate("ch-1", contracts.Window5m, healthy, th); s.HealthState != contracts.HealthHealthy {
		t.Fatalf("all-success window want healthy, got %q (rate=%v)", s.HealthState, s.SuccessRate)
	}

	// 18/20 = 0.90 -> between floor and target -> degraded.
	degraded := append(repeat(obs(true, 200, 1000, contracts.ErrorNone), 18),
		repeat(obs(false, 0, 0, contracts.ErrorServer), 2)...)
	if s := Aggregate("ch-1", contracts.Window5m, degraded, th); s.HealthState != contracts.HealthDegraded {
		t.Fatalf("0.90 success want degraded, got %q (rate=%v)", s.HealthState, s.SuccessRate)
	}

	// 16/20 = 0.80 -> below floor -> unhealthy.
	unhealthy := append(repeat(obs(true, 200, 1000, contracts.ErrorNone), 16),
		repeat(obs(false, 0, 0, contracts.ErrorServer), 4)...)
	if s := Aggregate("ch-1", contracts.Window5m, unhealthy, th); s.HealthState != contracts.HealthUnhealthy {
		t.Fatalf("0.80 success want unhealthy, got %q (rate=%v)", s.HealthState, s.SuccessRate)
	}
}

// A single auth failure is a severe error: the window is unhealthy even if the
// success rate still looks acceptable.
func TestAggregateAuthErrorForcesUnhealthy(t *testing.T) {
	th := DefaultThresholds()
	list := append(repeat(obs(true, 200, 1000, contracts.ErrorNone), 19),
		obs(false, 0, 0, contracts.ErrorAuth))
	got := Aggregate("ch-1", contracts.Window5m, list, th)
	if got.HealthState != contracts.HealthUnhealthy {
		t.Fatalf("auth error must force unhealthy, got %q (rate=%v)", got.HealthState, got.SuccessRate)
	}
	if got.RiskScore <= 0 {
		t.Fatalf("auth error must raise risk score, got %v", got.RiskScore)
	}
	if got.AuthFailureCount != 1 || got.UpstreamFailureCount != 0 {
		t.Fatalf("auth must be a credential failure, not provider quality failure: %+v", got)
	}
}

// High TTFT p95 degrades a window even when the success rate is perfect.
func TestAggregateLatencyDegrades(t *testing.T) {
	th := DefaultThresholds() // ttft max 4000
	list := repeat(obs(true, 5000, 6000, contracts.ErrorNone), 20)
	got := Aggregate("ch-1", contracts.Window5m, list, th)
	if got.HealthState != contracts.HealthDegraded {
		t.Fatalf("slow ttft want degraded, got %q (p95=%v)", got.HealthState, got.TTFTP95)
	}
	if got.TTFTScore != 0 {
		t.Fatalf("ttft beyond max should score 0, got %v", got.TTFTScore)
	}
}

// Extreme latency (beyond max*severe) is unhealthy, not merely degraded.
func TestAggregateExtremeLatencyUnhealthy(t *testing.T) {
	th := DefaultThresholds() // ttft max 4000, severe x1.5 => 6000
	list := repeat(obs(true, 9000, 12000, contracts.ErrorNone), 20)
	got := Aggregate("ch-1", contracts.Window5m, list, th)
	if got.HealthState != contracts.HealthUnhealthy {
		t.Fatalf("extreme ttft want unhealthy, got %q (p95=%v)", got.HealthState, got.TTFTP95)
	}
}

func TestAggregateCostPer1KAndScore(t *testing.T) {
	th := DefaultThresholds() // cost good 0, bad 20
	o := obs(true, 200, 1000, contracts.ErrorNone)
	o.InputTokens = 500
	o.OutputTokens = 500 // 1000 tokens
	o.EstimatedCost = 10 // -> 10 per 1k
	got := Aggregate("ch-1", contracts.Window5m, repeat(o, 10), th)
	if !approx(got.EstimatedCostPer1KTokens, 10) {
		t.Fatalf("cost per 1k want 10, got %v", got.EstimatedCostPer1KTokens)
	}
	// (20-10)/(20-0)*100 = 50.
	if !approx(got.CostScore, 50) {
		t.Fatalf("cost score want 50, got %v", got.CostScore)
	}
}

// With no token data the cost score is neutral, never a free-looking 100.
func TestAggregateCostNeutralWithoutTokens(t *testing.T) {
	got := Aggregate("ch-1", contracts.Window5m, repeat(obs(true, 200, 1000, contracts.ErrorNone), 10), DefaultThresholds())
	if got.CostScore != 50 {
		t.Fatalf("cost score without tokens want neutral 50, got %v", got.CostScore)
	}
	if got.EstimatedCostPer1KTokens != 0 {
		t.Fatalf("cost per 1k without tokens want 0, got %v", got.EstimatedCostPer1KTokens)
	}
}

func TestAggregateQualityScoreUsesSchedulingDeductions(t *testing.T) {
	th := DefaultThresholds()
	observations := repeat(obs(true, th.TTFTGoodMS, th.DurationGoodMS, contracts.ErrorNone), 17)
	observations = append(observations,
		obs(false, 0, 0, contracts.ErrorServer),
		obs(false, 0, 0, contracts.ErrorServer),
		obs(false, 0, 0, contracts.ErrorServer),
	)

	got := Aggregate("ch-1", contracts.Window5m, observations, th)
	// A 15% upstream error rate consumes the complete 55-point error budget.
	// The two latency dimensions are at their good thresholds and lose nothing.
	if !approx(got.QualityScore, 45) {
		t.Fatalf("quality score must use 100-55/25/20 scheduling deductions, got %v", got.QualityScore)
	}
}

func TestAggregateQualityScoreDoesNotDeductClientFailures(t *testing.T) {
	th := DefaultThresholds()
	observations := repeat(obs(true, th.TTFTGoodMS, th.DurationGoodMS, contracts.ErrorNone), 5)
	observations = append(observations, repeat(obs(false, 0, 0, contracts.ErrorClient), 5)...)

	got := Aggregate("ch-1", contracts.Window5m, observations, th)
	if !approx(got.QualityScore, 100) {
		t.Fatalf("client failures are factual SLA only and must not deduct upstream quality, got %v", got.QualityScore)
	}
}

func repeat(o contracts.ChannelObservation, n int) []contracts.ChannelObservation {
	out := make([]contracts.ChannelObservation, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, o)
	}
	return out
}

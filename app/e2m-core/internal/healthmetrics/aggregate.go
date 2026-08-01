// Package healthmetrics turns raw per-channel observations into windowed
// ChannelHealthSnapshots: success rate, TTFT/duration percentiles, and a set of
// explainable 0..100 sub-scores plus a per-window health state. It is the data
// foundation for health-driven automatic switching.
//
// The aggregation here is deliberately pure and stateless: given the
// observations that fall inside a window it computes one snapshot. Cross-window
// memory (consecutive-degraded windows, quarantine, recovery) is owned by the
// automatic-switch orchestrator (Phase 4), not by this package, so the metrics
// layer stays easy to reason about and test.
//
// Scoring philosophy (from the design doc): the first version favours
// explainability over algorithmic sophistication. Every score is a simple,
// documented transform of a measured quantity, and every sub-score is exposed so
// a switch decision and its notification can point at the dimension that moved.
package healthmetrics

import (
	"math"
	"sort"

	"e2m.local/contracts"
	"e2m.local/core/internal/strategy"
)

// Thresholds tune aggregation: the hard gates that decide health state and the
// reference points that map a raw metric onto a 0..100 score. All latencies are
// milliseconds. A zero-value Thresholds is not useful; use DefaultThresholds.
type Thresholds struct {
	// MinSamples is the fewest observations a window needs before it yields a
	// verdict. Below it the snapshot is HealthUnknown so an idle channel is never
	// mistaken for a failing one.
	MinSamples int

	// TargetSuccessRate is the healthy floor; below it (but above
	// FloorSuccessRate) a channel is degraded. FloorSuccessRate is the hard
	// floor; below it the channel is unhealthy.
	TargetSuccessRate float64
	FloorSuccessRate  float64

	// TTFT scoring/gating: at or below Good scores 100; at or above Max scores 0
	// and marks the window degraded. Max*Severe marks it unhealthy.
	TTFTGoodMS float64
	TTFTMaxMS  float64
	// Duration scoring/gating, same shape as TTFT.
	DurationGoodMS float64
	DurationMaxMS  float64

	// Cost scoring (lower is better): at or below Good scores 100; at or above
	// Bad scores 0. When a window has no token data the cost score is neutral.
	CostGoodPer1K float64
	CostBadPer1K  float64

	// SevereMultiplier scales the latency Max gates for the unhealthy verdict: a
	// p95 beyond Max*SevereMultiplier is unhealthy, not merely degraded.
	SevereMultiplier float64
	// RateLimitUnhealthy is the rate-limit fraction at/above which a window is
	// unhealthy regardless of success rate (a throttled channel is unusable).
	RateLimitUnhealthy float64
}

// DefaultThresholds returns the first-version tuning. These are conservative,
// human-picked numbers meant to be overridable per strategy/user later, not
// learned.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MinSamples:         5,
		TargetSuccessRate:  0.95,
		FloorSuccessRate:   0.85,
		TTFTGoodMS:         800,
		TTFTMaxMS:          4000,
		DurationGoodMS:     4000,
		DurationMaxMS:      20000,
		CostGoodPer1K:      0,
		CostBadPer1K:       20,
		SevereMultiplier:   1.5,
		RateLimitUnhealthy: 0.5,
	}
}

// withDefaults fills any unset field so a partially-specified Thresholds still
// behaves sensibly (mirrors the config pattern used by the health checker).
func (t Thresholds) withDefaults() Thresholds {
	d := DefaultThresholds()
	if t.MinSamples <= 0 {
		t.MinSamples = d.MinSamples
	}
	if t.TargetSuccessRate <= 0 {
		t.TargetSuccessRate = d.TargetSuccessRate
	}
	if t.FloorSuccessRate <= 0 {
		t.FloorSuccessRate = d.FloorSuccessRate
	}
	if t.TTFTGoodMS <= 0 {
		t.TTFTGoodMS = d.TTFTGoodMS
	}
	if t.TTFTMaxMS <= 0 {
		t.TTFTMaxMS = d.TTFTMaxMS
	}
	if t.DurationGoodMS <= 0 {
		t.DurationGoodMS = d.DurationGoodMS
	}
	if t.DurationMaxMS <= 0 {
		t.DurationMaxMS = d.DurationMaxMS
	}
	if t.CostBadPer1K <= 0 {
		t.CostBadPer1K = d.CostBadPer1K
	}
	if t.SevereMultiplier <= 0 {
		t.SevereMultiplier = d.SevereMultiplier
	}
	if t.RateLimitUnhealthy <= 0 {
		t.RateLimitUnhealthy = d.RateLimitUnhealthy
	}
	return t
}

// Aggregate rolls a legacy channel-only scope into one snapshot. New callers
// should use AggregateScope so a downstream instance and model are explicit.
func Aggregate(channelID string, window contracts.HealthWindow, observations []contracts.ChannelObservation, thresholds Thresholds) contracts.ChannelHealthSnapshot {
	scope := contracts.ChannelHealthScope{ChannelID: channelID}
	if len(observations) > 0 {
		newest := observations[0]
		for _, observation := range observations[1:] {
			if observation.ObservedAt.After(newest.ObservedAt) {
				newest = observation
			}
		}
		scope.PoolID = newest.PoolID
		scope.InstanceID = newest.InstanceID
		scope.Model = newest.Model
	}
	return AggregateScope(scope, window, observations, thresholds)
}

// AggregateScope rolls observations from one downstream/channel/model scope
// into a snapshot. observations must already be limited to both the scope and
// the rolling time window; only their contents are read (order does not matter).
// Scope fields are authoritative, including for an empty/unknown snapshot.
func AggregateScope(scope contracts.ChannelHealthScope, window contracts.HealthWindow, observations []contracts.ChannelObservation, thresholds Thresholds) contracts.ChannelHealthSnapshot {
	th := thresholds.withDefaults()
	snap := contracts.ChannelHealthSnapshot{
		ChannelID:    scope.ChannelID,
		InstanceID:   scope.InstanceID,
		PoolID:       scope.PoolID,
		Model:        scope.Model,
		Capability:   scope.Capability,
		EndpointPath: scope.EndpointPath,
		Window:       window,
		SampleCount:  len(observations),
		HealthState:  contracts.HealthUnknown,
	}
	if len(observations) == 0 {
		return snap
	}

	var successes int
	var qualitySuccesses int
	var qualitySamples int
	var timeouts, rateLimits int
	ttft := make([]float64, 0, len(observations))
	dur := make([]float64, 0, len(observations))
	var totalCost float64
	var totalTokens int64

	for i := range observations {
		o := observations[i]
		qualityEligible := o.Success || o.ErrorType != contracts.ErrorClient &&
			o.ErrorType != contracts.ErrorCanceled && o.ErrorType != contracts.ErrorPlatform
		if qualityEligible {
			qualitySamples++
			if o.Success {
				qualitySuccesses++
			}
		}
		if o.Success {
			successes++
		}
		if !o.Success {
			switch o.ErrorType {
			case contracts.ErrorTimeout:
				timeouts++
			case contracts.ErrorRateLimit:
				rateLimits++
			case contracts.ErrorAuth:
				snap.AuthFailureCount++
			case contracts.ErrorInsufficientBalance:
				snap.InsufficientBalanceCount++
			}
			if isUpstreamResponsibleFailure(o) {
				snap.UpstreamFailureCount++
			}
		}
		// Client errors/cancellations and credential failures are not provider
		// latency signals. Include only successful requests or failures that are
		// attributable to the upstream; still ignore missing (zero) timings.
		latencyEligible := o.Success || isUpstreamResponsibleFailure(o)
		if latencyEligible && o.FirstTokenMS > 0 {
			ttft = append(ttft, o.FirstTokenMS)
		}
		if latencyEligible && o.TotalMS > 0 {
			dur = append(dur, o.TotalMS)
		}
		totalCost += o.EstimatedCost
		totalTokens += o.InputTokens + o.OutputTokens
	}

	// Factual SLA metrics retain every observed downstream outcome. Attribution-
	// aware quality metrics separately exclude client mistakes, cancellations,
	// and local platform failures so they cannot lower upstream quality or
	// inflate scheduling confidence.
	factualN := float64(len(observations))
	snap.SuccessRate = float64(successes) / factualN
	snap.ErrorRate = 1 - snap.SuccessRate
	snap.QualitySampleCount = qualitySamples
	qualityN := float64(qualitySamples)
	if qualityN > 0 {
		snap.QualitySuccessRate = float64(qualitySuccesses) / qualityN
		snap.QualityErrorRate = 1 - snap.QualitySuccessRate
		snap.TimeoutRate = float64(timeouts) / qualityN
		snap.RateLimitRate = float64(rateLimits) / qualityN
		snap.UpstreamErrorRate = float64(snap.UpstreamFailureCount) / qualityN
	}

	snap.TTFTP50 = percentile(ttft, 0.50)
	snap.TTFTP95 = percentile(ttft, 0.95)
	snap.DurationP50 = percentile(dur, 0.50)
	snap.DurationP95 = percentile(dur, 0.95)

	if totalTokens > 0 {
		snap.EstimatedCostPer1KTokens = totalCost / (float64(totalTokens) / 1000)
	}

	// --- sub-scores (0..100) ---
	snap.SuccessScore = clamp(snap.QualitySuccessRate*100, 0, 100)
	snap.TTFTScore = latencyScore(len(ttft), snap.TTFTP95, th.TTFTGoodMS, th.TTFTMaxMS)
	snap.DurationScore = latencyScore(len(dur), snap.DurationP95, th.DurationGoodMS, th.DurationMaxMS)
	snap.StabilityScore = stabilityScore(snap)
	snap.CostScore = costScore(totalTokens, snap.EstimatedCostPer1KTokens, th.CostGoodPer1K, th.CostBadPer1K)
	snap.RiskScore = riskScore(qualityN, snap, snap.AuthFailureCount, snap.InsufficientBalanceCount)

	// Persist the same deduction score used by automatic scheduling: start at
	// 100, then deduct at most 55 for upstream errors, 25 for TTFT, and 20 for
	// total duration. Keeping the snapshot and scheduler on one calculator avoids
	// presenting a historical score that disagrees with an ejection decision.
	snap.QualityScore = strategy.EvaluatePenalty(
		contracts.RouteStrategy{Thresholds: contracts.StrategyThresholds{
			MinSamples:       th.MinSamples,
			FloorSuccessRate: th.FloorSuccessRate,
			MaxTTFTP95MS:     th.TTFTMaxMS,
			MaxDurationP95MS: th.DurationMaxMS,
		}},
		strategy.Candidate{Snapshot: snap},
	).Score
	// Health nets quality against risk so a high-risk channel cannot read
	// healthy on quality alone.
	snap.HealthScore = clamp(snap.QualityScore-0.5*snap.RiskScore, 0, 100)

	snap.HealthState = healthState(th, snap, snap.AuthFailureCount, snap.InsufficientBalanceCount)
	return snap
}

func isUpstreamResponsibleFailure(observation contracts.ChannelObservation) bool {
	if observation.Success {
		return false
	}
	switch observation.ErrorType {
	case contracts.ErrorTimeout,
		contracts.ErrorRateLimit,
		contracts.ErrorServer,
		contracts.ErrorNetwork,
		contracts.ErrorUnknown:
		return true
	default:
		return false
	}
}

// latencyScore maps a p95 latency onto 0..100 (lower is better). With no latency
// samples it is neutral (50) so quality is driven by success rate instead of a
// misleadingly perfect or zero latency score.
func latencyScore(samples int, p95, good, bad float64) float64 {
	if samples == 0 {
		return 50
	}
	return clamp(100*(bad-p95)/(bad-good), 0, 100)
}

// costScore maps cost-per-1k-tokens onto 0..100 (cheaper is better). With no
// token data it is neutral (50) so cost strategies do not reward "no data" as
// if it were free.
func costScore(totalTokens int64, costPer1K, good, bad float64) float64 {
	if totalTokens <= 0 {
		return 50
	}
	if bad <= good {
		return 50
	}
	return clamp(100*(bad-costPer1K)/(bad-good), 0, 100)
}

// stabilityScore rewards low latency jitter and low error rate. Jitter is the
// p95/p50 ratio (1 == perfectly consistent); a ratio of 3+ scores zero on that
// half. The two halves are averaged.
func stabilityScore(snap contracts.ChannelHealthSnapshot) float64 {
	jitter := latencyJitterScore(snap.TTFTP50, snap.TTFTP95)
	consistency := clamp(snap.QualitySuccessRate*100, 0, 100)
	return clamp(0.5*jitter+0.5*consistency, 0, 100)
}

func latencyJitterScore(p50, p95 float64) float64 {
	if p50 <= 0 {
		return 100 // no latency data: do not penalise stability
	}
	ratio := p95 / p50
	// ratio 1 -> 100, ratio 3 -> 0, linear between.
	return clamp(50*(3-ratio), 0, 100)
}

// riskScore rises with the incidence of dangerous failures. Auth and balance
// errors are treated as severe (each such sample contributes fully), while
// timeouts and rate limits contribute partially. It is capped at 100.
func riskScore(n float64, snap contracts.ChannelHealthSnapshot, auths, balances int) float64 {
	if n <= 0 {
		return 0
	}
	severeRate := float64(auths+balances) / n
	risk := severeRate*100 +
		snap.RateLimitRate*40 +
		snap.TimeoutRate*40 +
		snap.QualityErrorRate*20
	return clamp(risk, 0, 100)
}

// healthState is the per-window data verdict. Cross-window escalation
// (degraded->unhealthy after N windows) and lifecycle states
// (quarantined/recovering) are the orchestrator's responsibility.
func healthState(th Thresholds, snap contracts.ChannelHealthSnapshot, auths, balances int) contracts.HealthState {
	if snap.QualitySampleCount < th.MinSamples {
		return contracts.HealthUnknown
	}
	severe := auths > 0 || balances > 0
	switch {
	case severe,
		snap.QualitySuccessRate < th.FloorSuccessRate,
		snap.RateLimitRate >= th.RateLimitUnhealthy,
		snap.TTFTP95 > th.TTFTMaxMS*th.SevereMultiplier,
		snap.DurationP95 > th.DurationMaxMS*th.SevereMultiplier:
		return contracts.HealthUnhealthy
	case snap.QualitySuccessRate < th.TargetSuccessRate,
		snap.TTFTP95 > th.TTFTMaxMS,
		snap.DurationP95 > th.DurationMaxMS:
		return contracts.HealthDegraded
	default:
		return contracts.HealthHealthy
	}
}

// percentile returns the q-quantile (q in [0,1]) of values using linear
// interpolation between closest ranks (the numpy/R-7 convention). It sorts a
// copy so the caller's slice is untouched. Empty input yields 0.
func percentile(values []float64, q float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return values[0]
	}
	sorted := make([]float64, n)
	copy(sorted, values)
	sort.Float64s(sorted)
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[n-1]
	}
	rank := q * float64(n-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo] + frac*(sorted[hi]-sorted[lo])
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

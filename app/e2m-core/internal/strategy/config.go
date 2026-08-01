package strategy

import (
	"e2m.local/contracts"
)

// resolvedStrategy is a RouteStrategy with every threshold and weight filled in.
// Callers never see zero-valued knobs: the engine resolves type defaults once so
// scoring and gating can assume complete config.
type resolvedStrategy struct {
	typ        contracts.RouteStrategyType
	weights    contracts.StrategyWeights
	thresholds contracts.StrategyThresholds
}

// defaultWeights returns the doc's per-type weight blend. The five weights sum to
// 1.0 so a strategy score stays on the 0..100 sub-score scale.
func defaultWeights(t contracts.RouteStrategyType) contracts.StrategyWeights {
	switch t {
	case contracts.StrategyCostFirst:
		// cost 0.45, success 0.25, ttft 0.15, duration 0.10, stability 0.05
		return contracts.StrategyWeights{Success: 0.25, TTFT: 0.15, Duration: 0.10, Stability: 0.05, Cost: 0.45}
	case contracts.StrategyLatencyFirst:
		// ttft 0.45, duration 0.25, success 0.20, stability 0.05, cost 0.05
		return contracts.StrategyWeights{Success: 0.20, TTFT: 0.45, Duration: 0.25, Stability: 0.05, Cost: 0.05}
	case contracts.StrategyBalanced:
		// success 0.35, ttft 0.25, duration 0.15, stability 0.15, cost 0.10
		return contracts.StrategyWeights{Success: 0.35, TTFT: 0.25, Duration: 0.15, Stability: 0.15, Cost: 0.10}
	default: // stability_first
		// success 0.55, stability 0.25, ttft 0.10, duration 0.05, cost 0.05
		return contracts.StrategyWeights{Success: 0.55, TTFT: 0.10, Duration: 0.05, Stability: 0.25, Cost: 0.05}
	}
}

// defaultThresholds mirrors the aggregator's DefaultThresholds so a channel the
// metrics layer would call unhealthy is also gated here. Kept local to avoid a
// dependency on the healthmetrics package (contracts stays the shared boundary).
func defaultThresholds() contracts.StrategyThresholds {
	return contracts.StrategyThresholds{
		MinSamples:              5,
		TargetSuccessRate:       0.95,
		FloorSuccessRate:        0.85,
		MaxTTFTP95MS:            4000,
		MaxDurationP95MS:        20000,
		ConsecutiveFailureLimit: 3,
		EjectScore:              60,
	}
}

// resolve fills defaults: the type is normalized, unset weights fall back to the
// type blend as a whole (a partial weight set is ambiguous, so it is all-or-
// nothing), and each unset threshold falls back to its first-version default.
func resolve(s contracts.RouteStrategy) resolvedStrategy {
	typ := s.Type.Normalize()
	w := s.Weights
	if w == (contracts.StrategyWeights{}) {
		w = defaultWeights(typ)
	}
	d := defaultThresholds()
	th := s.Thresholds
	if th.MinSamples <= 0 {
		th.MinSamples = d.MinSamples
	}
	if th.TargetSuccessRate <= 0 {
		th.TargetSuccessRate = d.TargetSuccessRate
	}
	if th.FloorSuccessRate <= 0 {
		th.FloorSuccessRate = d.FloorSuccessRate
	}
	if th.MaxTTFTP95MS <= 0 {
		th.MaxTTFTP95MS = d.MaxTTFTP95MS
	}
	if th.MaxDurationP95MS <= 0 {
		th.MaxDurationP95MS = d.MaxDurationP95MS
	}
	if th.ConsecutiveFailureLimit <= 0 {
		th.ConsecutiveFailureLimit = d.ConsecutiveFailureLimit
	}
	if th.EjectScore <= 0 || th.EjectScore > 100 {
		th.EjectScore = d.EjectScore
	}
	return resolvedStrategy{typ: typ, weights: w, thresholds: th}
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
